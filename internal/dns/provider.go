package dns

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
)

// ErrExternalRecordConflict indicates an unmanaged DNS record already exists for the target domain.
var ErrExternalRecordConflict = errors.New("external dns record conflict")

// RemoteZone describes a root domain zone returned by a DNS provider.
type RemoteZone struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	SupportsProxy bool   `json:"supports_proxy"`
}

// EnsureRecordInput contains parameters for creating or updating a DNS A/AAAA record.
type EnsureRecordInput struct {
	RemoteZoneID     string
	ZoneName         string
	Subdomain        string // "@" for apex or prefix like "api"
	FullDomain       string
	RecordType       string // "A" or "AAAA"
	Value            string // Edge Node Public IP
	Proxied          bool
	ExistingRecordID string // Stored Vantu record ID if updating
	AdoptExisting    bool   // Take over pre-existing unmanaged record if true
}

// Provider defines the unified multi-vendor DNS driver contract.
type Provider interface {
	VerifyAndListZones(ctx context.Context) ([]RemoteZone, error)
	EnsureRecord(ctx context.Context, in EnsureRecordInput) (recordID string, err error)
	SetProxied(ctx context.Context, remoteZoneID, fullDomain, value, recordID string, proxied bool) error
	DeleteRecord(ctx context.Context, remoteZoneID, recordID string) error
}

// Credentials holds union fields stored inside encrypted JSON in dns_providers.credentials_enc.
type Credentials struct {
	APIToken  string `json:"api_token,omitempty"`  // Cloudflare API Token
	BaseURL   string `json:"base_url,omitempty"`   // Optional custom API endpoint (testing / enterprise proxy)
	SecretID  string `json:"secret_id,omitempty"`  // Tencent Cloud / DNSPod SecretId
	SecretKey string `json:"secret_key,omitempty"` // Tencent Cloud / DNSPod SecretKey
}

// Build constructs the concrete Provider implementation from providerType and decrypted JSON credentials.
func Build(providerType, decryptedJSON string) (Provider, error) {
	var cred Credentials
	if err := json.Unmarshal([]byte(decryptedJSON), &cred); err != nil {
		return nil, fmt.Errorf("parse dns credentials: %w", err)
	}
	switch providerType {
	case "cloudflare":
		if cred.APIToken == "" {
			return nil, errors.New("cloudflare api_token is required")
		}
		return NewCloudflareProvider(cred.APIToken, cred.BaseURL), nil
	case "dnspod":
		if cred.SecretID == "" || cred.SecretKey == "" {
			return nil, errors.New("dnspod secret_id and secret_key are required")
		}
		return NewDNSPodProvider(cred.SecretID, cred.SecretKey, cred.BaseURL), nil
	default:
		return nil, fmt.Errorf("unsupported dns provider type: %s", providerType)
	}
}
