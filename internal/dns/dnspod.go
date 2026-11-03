package dns

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const defaultDNSPodEndpoint = "https://dnspod.tencentcloudapi.com"

// DNSPodProvider implements Tencent Cloud DNSPod API 3.0 with TC3-HMAC-SHA256 signing.
type DNSPodProvider struct {
	secretID  string
	secretKey string
	endpoint  string
	client    *http.Client
}

func NewDNSPodProvider(secretID, secretKey, endpoint string) *DNSPodProvider {
	if strings.TrimSpace(endpoint) == "" {
		endpoint = defaultDNSPodEndpoint
	}
	return &DNSPodProvider{
		secretID:  strings.TrimSpace(secretID),
		secretKey: strings.TrimSpace(secretKey),
		endpoint:  strings.TrimRight(endpoint, "/"),
		client:    &http.Client{Timeout: 12 * time.Second},
	}
}

func (p *DNSPodProvider) VerifyAndListZones(ctx context.Context) ([]RemoteZone, error) {
	var resp struct {
		Response struct {
			DomainList []struct {
				DomainId int64  `json:"DomainId"`
				Name     string `json:"Name"`
			} `json:"DomainList"`
			Error *struct {
				Code    string `json:"Code"`
				Message string `json:"Message"`
			} `json:"Error"`
		} `json:"Response"`
	}
	if err := p.callTC3(ctx, "DescribeDomainList", map[string]any{"Limit": 100}, &resp); err != nil {
		return nil, err
	}
	if resp.Response.Error != nil {
		return nil, fmt.Errorf("dnspod %s: %s", resp.Response.Error.Code, resp.Response.Error.Message)
	}
	out := make([]RemoteZone, 0, len(resp.Response.DomainList))
	for _, d := range resp.Response.DomainList {
		out = append(out, RemoteZone{
			ID:            strconv.FormatInt(d.DomainId, 10),
			Name:          d.Name,
			SupportsProxy: false,
		})
	}
	return out, nil
}

func (p *DNSPodProvider) EnsureRecord(ctx context.Context, in EnsureRecordInput) (string, error) {
	sub := in.Subdomain
	if sub == "" {
		sub = "@"
	}
	recType := in.RecordType
	if recType == "" {
		recType = "A"
	}

	if in.ExistingRecordID != "" {
		recID, err := strconv.ParseUint(in.ExistingRecordID, 10, 64)
		if err == nil {
			var modResp struct {
				Response struct {
					RecordId uint64 `json:"RecordId"`
					Error    *struct {
						Code    string `json:"Code"`
						Message string `json:"Message"`
					} `json:"Error"`
				} `json:"Response"`
			}
			err := p.callTC3(ctx, "ModifyRecord", map[string]any{
				"Domain":     in.ZoneName,
				"SubDomain":  sub,
				"RecordType": recType,
				"RecordLine": "默认",
				"Value":      in.Value,
				"RecordId":   recID,
			}, &modResp)
			if err == nil && modResp.Response.Error == nil {
				return strconv.FormatUint(modResp.Response.RecordId, 10), nil
			}
		}
	}

	var createResp struct {
		Response struct {
			RecordId uint64 `json:"RecordId"`
			Error    *struct {
				Code    string `json:"Code"`
				Message string `json:"Message"`
			} `json:"Error"`
		} `json:"Response"`
	}
	if err := p.callTC3(ctx, "CreateRecord", map[string]any{
		"Domain":     in.ZoneName,
		"SubDomain":  sub,
		"RecordType": recType,
		"RecordLine": "默认",
		"Value":      in.Value,
		"Remark":     "managed-by-vantu",
	}, &createResp); err != nil {
		return "", err
	}
	if createResp.Response.Error != nil {
		return "", fmt.Errorf("dnspod %s: %s", createResp.Response.Error.Code, createResp.Response.Error.Message)
	}
	return strconv.FormatUint(createResp.Response.RecordId, 10), nil
}

func (p *DNSPodProvider) SetProxied(_ context.Context, _, _, _, _ string, proxied bool) error {
	if proxied {
		return fmt.Errorf("DNSPod 纯解析厂商不支持 Cloudflare 小黄云 CDN 开关")
	}
	return nil
}

func (p *DNSPodProvider) DeleteRecord(ctx context.Context, remoteZoneID, recordID string) error {
	if recordID == "" {
		return nil
	}
	recID, err := strconv.ParseUint(recordID, 10, 64)
	if err != nil {
		return nil
	}
	domID, _ := strconv.ParseUint(remoteZoneID, 10, 64)
	var delResp struct {
		Response struct {
			Error *struct {
				Code    string `json:"Code"`
				Message string `json:"Message"`
			} `json:"Error"`
		} `json:"Response"`
	}
	req := map[string]any{"RecordId": recID}
	if domID > 0 {
		req["DomainId"] = domID
	}
	return p.callTC3(ctx, "DeleteRecord", req, &delResp)
}

func (p *DNSPodProvider) callTC3(ctx context.Context, action string, payload any, out any) error {
	bodyBytes, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	u, err := url.Parse(p.endpoint)
	if err != nil {
		return err
	}
	host := u.Host
	now := time.Now().UTC()
	timestamp := now.Unix()
	date := now.Format("2006-01-02")
	service := "dnspod"

	hashedPayload := sha256Hex(bodyBytes)
	canonicalHeaders := fmt.Sprintf("content-type:application/json; charset=utf-8\nhost:%s\nx-tc-action:%s\n", host, strings.ToLower(action))
	signedHeaders := "content-type;host;x-tc-action"
	canonicalRequest := fmt.Sprintf("POST\n/\n\n%s\n%s\n%s", canonicalHeaders, signedHeaders, hashedPayload)

	credentialScope := fmt.Sprintf("%s/%s/tc3_request", date, service)
	stringToSign := fmt.Sprintf("TC3-HMAC-SHA256\n%d\n%s\n%s", timestamp, credentialScope, sha256Hex([]byte(canonicalRequest)))

	secretDate := hmacSHA256([]byte("TC3"+p.secretKey), date)
	secretService := hmacSHA256(secretDate, service)
	secretSigning := hmacSHA256(secretService, "tc3_request")
	signature := hex.EncodeToString(hmacSHA256(secretSigning, stringToSign))

	authHeader := fmt.Sprintf("TC3-HMAC-SHA256 Credential=%s/%s, SignedHeaders=%s, Signature=%s",
		p.secretID, credentialScope, signedHeaders, signature)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.endpoint+"/", bytes.NewReader(bodyBytes))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", authHeader)
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	req.Header.Set("Host", host)
	req.Header.Set("X-TC-Action", action)
	req.Header.Set("X-TC-Version", "2021-03-23")
	req.Header.Set("X-TC-Timestamp", strconv.FormatInt(timestamp, 10))

	resp, err := p.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode >= 300 {
		return fmt.Errorf("dnspod http %d: %s", resp.StatusCode, string(raw))
	}
	return json.Unmarshal(raw, out)
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func hmacSHA256(key []byte, msg string) []byte {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(msg))
	return mac.Sum(nil)
}
