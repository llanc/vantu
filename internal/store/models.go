package store

import "time"

type AdminUser struct {
	ID           int64     `json:"id"`
	Username     string    `json:"username"`
	PasswordHash string    `json:"-"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

type APIToken struct {
	ID          string     `json:"id"`
	Name        string     `json:"name"`
	TokenPrefix string     `json:"token_prefix"`
	TokenSHA256 string     `json:"-"`
	Scope       string     `json:"scope"` // "admin" | "readonly"
	ExpiresAt   *time.Time `json:"expires_at,omitempty"`
	LastUsedAt  *time.Time `json:"last_used_at,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
}

type DNSProviderRecord struct {
	ID             string     `json:"id"`
	Name           string     `json:"name"`
	ProviderType   string     `json:"provider_type"` // "cloudflare" | "dnspod"
	CredentialsEnc string     `json:"-"`
	MaskedCred     string     `json:"masked_cred"`
	Status         string     `json:"status"`
	LastCheckedAt  *time.Time `json:"last_checked_at,omitempty"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
}

type DNSZone struct {
	ID            string    `json:"id"`
	ProviderID    string    `json:"provider_id"`
	ProviderName  string    `json:"provider_name,omitempty"`
	ProviderType  string    `json:"provider_type,omitempty"`
	ZoneName      string    `json:"zone_name"`
	RemoteZoneID  string    `json:"remote_zone_id"`
	SupportsProxy bool      `json:"supports_proxy"`
	IsDefault     bool      `json:"is_default"`
	RouteCount    int       `json:"route_count"`
	SyncedAt      time.Time `json:"synced_at"`
}

type EdgeNode struct {
	ID            string     `json:"id"`
	Name          string     `json:"name"`
	Region        string     `json:"region"`
	PublicIPv4    string     `json:"public_ipv4"`
	PublicIPv6    string     `json:"public_ipv6"`
	TailscaleIP   string     `json:"tailscale_ip"`
	CaddyAdminURL string     `json:"caddy_admin_url"`
	AuthSecretEnc string     `json:"-"`
	MaskedSecret  string     `json:"masked_secret"`
	IsDefault     bool       `json:"is_default"`
	Enabled       bool       `json:"enabled"`
	HealthStatus  string     `json:"health_status"`  // "online" | "degraded" | "offline" | "drift" | "unknown"
	TSConnType    string     `json:"ts_conn_type"`   // "direct" | "derp" | "offline" | "unknown"
	TSDERPRegion  string     `json:"ts_derp_region"` // e.g. "tok"
	TSRTTMs       int        `json:"ts_rtt_ms"`
	CaddyVersion  string     `json:"caddy_version"`
	RouteCount    int        `json:"route_count"`
	LastSeenAt    *time.Time `json:"last_seen_at,omitempty"`
	CreatedAt     time.Time  `json:"created_at"`
	UpdatedAt     time.Time  `json:"updated_at"`
}

type Route struct {
	ID                string     `json:"id"`
	Subdomain         string     `json:"subdomain"`
	FullDomain        string     `json:"full_domain"`
	ZoneID            string     `json:"zone_id"`
	ZoneName          string     `json:"zone_name,omitempty"`
	SupportsProxy     bool       `json:"supports_proxy"`
	EdgeNodeID        string     `json:"edge_node_id"`
	EdgeNodeName      string     `json:"edge_node_name,omitempty"`
	EdgeNodePublicIP  string     `json:"edge_node_public_ip,omitempty"`
	EdgeNodeTSRTTMs   int        `json:"edge_node_ts_rtt_ms"`
	EdgeNodeTSType    string     `json:"edge_node_ts_type,omitempty"`
	SourceType        string     `json:"source_type"` // "docker" | "manual"
	ContainerName     string     `json:"container_name"`
	ContainerID       string     `json:"container_id"`
	ForwardMode       string     `json:"forward_mode"`    // "direct" | "relay"
	UpstreamScheme    string     `json:"upstream_scheme"` // "http" | "https"
	LocalTargetHost   string     `json:"local_target_host"`
	LocalTargetPort   int        `json:"local_target_port"`
	EffectiveDial     string     `json:"effective_dial"`
	RelayTokenHMAC    string     `json:"-"`
	Preset            string     `json:"preset"` // "web" | "sse" | "webdav" | "https_insecure"
	MaxBodyMB         int        `json:"max_body_mb"`
	ReadTimeoutSec    int        `json:"read_timeout_sec"`
	DNSRecordID       string     `json:"dns_record_id"`
	DNSProxied        bool       `json:"dns_proxied"`
	Enabled           bool       `json:"enabled"`
	LifecycleState    string     `json:"lifecycle_state"`
	LastError         string     `json:"last_error"`
	LocalHealthy      bool       `json:"local_healthy"`
	CaddySynced       bool       `json:"caddy_synced"`
	OriginTLSOK       bool       `json:"origin_tls_ok"`
	OriginCertIssuer  string     `json:"origin_cert_issuer"`
	OriginCertExpires *time.Time `json:"origin_cert_expires,omitempty"`
	OriginCertDays    int        `json:"origin_cert_days"`
	PublicCertIssuer  string     `json:"public_cert_issuer"`
	PublicHTTPCode    int        `json:"public_http_code"`
	PublicLatencyMs   int        `json:"public_latency_ms"`
	LastProbedAt      *time.Time `json:"last_probed_at,omitempty"`
	Note              string     `json:"note"`
	CreatedAt         time.Time  `json:"created_at"`
	UpdatedAt         time.Time  `json:"updated_at"`
}

type ProbeSample struct {
	ID              int64     `json:"id"`
	RouteID         string    `json:"route_id"`
	IsOK            bool      `json:"is_ok"`
	TSRTTMs         int       `json:"ts_rtt_ms"`
	PublicLatencyMs int       `json:"public_latency_ms"`
	PublicHTTPCode  int       `json:"public_http_code"`
	FailStage       string    `json:"fail_stage"`
	CheckedAt       time.Time `json:"checked_at"`
}

type AuditEvent struct {
	ID          int64     `json:"id"`
	Severity    string    `json:"severity"` // "info" | "warn" | "error"
	Category    string    `json:"category"` // "route" | "node" | "dns" | "cert" | "auth"
	TargetID    string    `json:"target_id"`
	TargetLabel string    `json:"target_label"`
	Message     string    `json:"message"`
	DetailJSON  string    `json:"detail_json"`
	Actor       string    `json:"actor"`
	CreatedAt   time.Time `json:"created_at"`
}
