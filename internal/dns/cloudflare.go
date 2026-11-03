package dns

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const defaultCloudflareBaseURL = "https://api.cloudflare.com/client/v4"

type CloudflareProvider struct {
	token   string
	baseURL string
	client  *http.Client
}

func NewCloudflareProvider(apiToken, baseURL string) *CloudflareProvider {
	if strings.TrimSpace(baseURL) == "" {
		baseURL = defaultCloudflareBaseURL
	}
	return &CloudflareProvider{
		token:   strings.TrimSpace(apiToken),
		baseURL: strings.TrimRight(baseURL, "/"),
		client:  &http.Client{Timeout: 12 * time.Second},
	}
}

type cfEnvelope[T any] struct {
	Success bool `json:"success"`
	Errors  []struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"errors"`
	Result T `json:"result"`
}

type cfZone struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Status string `json:"status"`
}

type cfRecord struct {
	ID      string `json:"id"`
	Type    string `json:"type"`
	Name    string `json:"name"`
	Content string `json:"content"`
	Proxied bool   `json:"proxied"`
	Comment string `json:"comment"`
}

func (p *CloudflareProvider) VerifyAndListZones(ctx context.Context) ([]RemoteZone, error) {
	var env cfEnvelope[[]cfZone]
	if err := p.doJSON(ctx, http.MethodGet, "/zones?per_page=50&status=active", nil, &env); err != nil {
		return nil, err
	}
	out := make([]RemoteZone, 0, len(env.Result))
	for _, z := range env.Result {
		out = append(out, RemoteZone{
			ID:            z.ID,
			Name:          z.Name,
			SupportsProxy: true,
		})
	}
	return out, nil
}

func (p *CloudflareProvider) EnsureRecord(ctx context.Context, in EnsureRecordInput) (string, error) {
	recType := in.RecordType
	if recType == "" {
		recType = "A"
	}
	ttl := 1 // Automatic TTL on Cloudflare (required when proxied=true, also optimal for unproxied)

	body := map[string]any{
		"type":    recType,
		"name":    in.FullDomain,
		"content": in.Value,
		"proxied": in.Proxied,
		"ttl":     ttl,
		"comment": "managed-by-vantu",
	}

	// 1. If we already hold a known Vantu DNS Record ID, update it directly.
	if in.ExistingRecordID != "" {
		var upd cfEnvelope[cfRecord]
		path := fmt.Sprintf("/zones/%s/dns_records/%s", in.RemoteZoneID, in.ExistingRecordID)
		if err := p.doJSON(ctx, http.MethodPatch, path, body, &upd); err == nil && upd.Result.ID != "" {
			return upd.Result.ID, nil
		}
	}

	// 2. Check if a record with the same name + type already exists in Cloudflare.
	q := url.Values{}
	q.Set("name", in.FullDomain)
	q.Set("type", recType)
	var listEnv cfEnvelope[[]cfRecord]
	if err := p.doJSON(ctx, http.MethodGet, fmt.Sprintf("/zones/%s/dns_records?%s", in.RemoteZoneID, q.Encode()), nil, &listEnv); err != nil {
		return "", err
	}

	if len(listEnv.Result) > 0 {
		existing := listEnv.Result[0]
		isVantuManaged := strings.Contains(existing.Comment, "vantu")
		if !isVantuManaged && !in.AdoptExisting && existing.Content != in.Value {
			return "", fmt.Errorf("%w: Cloudflare 已存在同名记录 %s -> %s（如需接管请勾选接管已有解析或加 --adopt-dns）",
				ErrExternalRecordConflict, in.FullDomain, existing.Content)
		}
		var upd cfEnvelope[cfRecord]
		path := fmt.Sprintf("/zones/%s/dns_records/%s", in.RemoteZoneID, existing.ID)
		if err := p.doJSON(ctx, http.MethodPatch, path, body, &upd); err != nil {
			return "", err
		}
		return upd.Result.ID, nil
	}

	// 3. Create fresh DNS record.
	var created cfEnvelope[cfRecord]
	if err := p.doJSON(ctx, http.MethodPost, fmt.Sprintf("/zones/%s/dns_records", in.RemoteZoneID), body, &created); err != nil {
		return "", err
	}
	return created.Result.ID, nil
}

func (p *CloudflareProvider) SetProxied(ctx context.Context, remoteZoneID, fullDomain, value, recordID string, proxied bool) error {
	if recordID == "" {
		return fmt.Errorf("missing cloudflare dns_record_id for %s", fullDomain)
	}
	body := map[string]any{
		"proxied": proxied,
		"ttl":     1,
	}
	var env cfEnvelope[cfRecord]
	path := fmt.Sprintf("/zones/%s/dns_records/%s", remoteZoneID, recordID)
	return p.doJSON(ctx, http.MethodPatch, path, body, &env)
}

func (p *CloudflareProvider) DeleteRecord(ctx context.Context, remoteZoneID, recordID string) error {
	if recordID == "" {
		return nil
	}
	var env cfEnvelope[map[string]any]
	path := fmt.Sprintf("/zones/%s/dns_records/%s", remoteZoneID, recordID)
	err := p.doJSON(ctx, http.MethodDelete, path, nil, &env)
	if err != nil && strings.Contains(err.Error(), "81044") {
		// Record already deleted externally; treat as idempotent success.
		return nil
	}
	return err
}

func (p *CloudflareProvider) doJSON(ctx context.Context, method, path string, reqBody any, out any) error {
	var reader io.Reader
	if reqBody != nil {
		b, err := json.Marshal(reqBody)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(b)
	}

	req, err := http.NewRequestWithContext(ctx, method, p.baseURL+path, reader)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+p.token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := p.client.Do(req)
	if err != nil {
		return fmt.Errorf("cloudflare request failed: %w", err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}

	var check struct {
		Success bool `json:"success"`
		Errors  []struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"errors"`
	}
	_ = json.Unmarshal(raw, &check)
	if resp.StatusCode >= 300 || !check.Success {
		if len(check.Errors) > 0 {
			return fmt.Errorf("cloudflare api error (%d): %s", check.Errors[0].Code, check.Errors[0].Message)
		}
		return fmt.Errorf("cloudflare http status %d: %s", resp.StatusCode, string(raw))
	}

	if out != nil {
		if err := json.Unmarshal(raw, out); err != nil {
			return fmt.Errorf("decode cloudflare response: %w", err)
		}
	}
	return nil
}
