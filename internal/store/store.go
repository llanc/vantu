package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

var ErrNotFound = errors.New("record not found")

// Store wraps a single lightweight SQLite instance in WAL mode (KISS principle).
type Store struct {
	db     *sql.DB
	dbPath string
}

// Open initializes the SQLite database file, configures WAL pragmas, and runs migrations.
func Open(dbPath string) (*Store, error) {
	if dbPath != ":memory:" {
		if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
			return nil, fmt.Errorf("create db directory: %w", err)
		}
	}

	dsn := dbPath
	if dbPath != ":memory:" {
		dsn = fmt.Sprintf("file:%s?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=foreign_keys(ON)", dbPath)
	}

	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	if dbPath == ":memory:" {
		db.SetMaxOpenConns(1)
	}

	pragmas := []string{
		"PRAGMA journal_mode = WAL;",
		"PRAGMA synchronous = NORMAL;",
		"PRAGMA busy_timeout = 5000;",
		"PRAGMA foreign_keys = ON;",
	}
	for _, p := range pragmas {
		if _, err := db.Exec(p); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("exec %s: %w", p, err)
		}
	}

	s := &Store{db: db, dbPath: dbPath}
	if err := s.migrate(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error {
	return s.db.Close()
}

func (s *Store) DBSizeBytes() int64 {
	if s.dbPath == ":memory:" {
		return 0
	}
	var total int64
	for _, suffix := range []string{"", "-wal", "-shm"} {
		if st, err := os.Stat(s.dbPath + suffix); err == nil {
			total += st.Size()
		}
	}
	return total
}

func (s *Store) migrate() error {
	schema := `
CREATE TABLE IF NOT EXISTS admin_users (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    username        TEXT NOT NULL UNIQUE DEFAULT 'admin',
    password_hash   TEXT NOT NULL,
    created_at      DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at      DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS web_sessions (
    token_sha256    TEXT PRIMARY KEY,
    username        TEXT NOT NULL,
    expires_at      DATETIME NOT NULL,
    created_at      DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS api_tokens (
    id              TEXT PRIMARY KEY,
    name            TEXT NOT NULL,
    token_prefix    TEXT NOT NULL,
    token_sha256    TEXT NOT NULL UNIQUE,
    scope           TEXT NOT NULL DEFAULT 'admin',
    expires_at      DATETIME,
    last_used_at    DATETIME,
    created_at      DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS dns_providers (
    id              TEXT PRIMARY KEY,
    name            TEXT NOT NULL,
    provider_type   TEXT NOT NULL,
    credentials_enc TEXT NOT NULL,
    masked_cred     TEXT NOT NULL DEFAULT '',
    status          TEXT NOT NULL DEFAULT 'active',
    last_checked_at DATETIME,
    created_at      DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at      DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS dns_zones (
    id              TEXT PRIMARY KEY,
    provider_id     TEXT NOT NULL REFERENCES dns_providers(id) ON DELETE CASCADE,
    zone_name       TEXT NOT NULL,
    remote_zone_id  TEXT NOT NULL,
    supports_proxy  INTEGER NOT NULL DEFAULT 1,
    is_default      INTEGER NOT NULL DEFAULT 0,
    synced_at       DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(provider_id, zone_name)
);

CREATE TABLE IF NOT EXISTS edge_nodes (
    id                  TEXT PRIMARY KEY,
    name                TEXT NOT NULL UNIQUE,
    region              TEXT NOT NULL DEFAULT '',
    public_ipv4         TEXT NOT NULL,
    public_ipv6         TEXT NOT NULL DEFAULT '',
    tailscale_ip        TEXT NOT NULL,
    caddy_admin_url     TEXT NOT NULL,
    auth_secret_enc     TEXT NOT NULL,
    masked_secret       TEXT NOT NULL DEFAULT '',
    is_default          INTEGER NOT NULL DEFAULT 0,
    enabled             INTEGER NOT NULL DEFAULT 1,
    health_status       TEXT NOT NULL DEFAULT 'unknown',
    ts_conn_type        TEXT NOT NULL DEFAULT 'unknown',
    ts_derp_region      TEXT NOT NULL DEFAULT '',
    ts_rtt_ms           INTEGER NOT NULL DEFAULT -1,
    caddy_version       TEXT NOT NULL DEFAULT '',
    last_seen_at        DATETIME,
    created_at          DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at          DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS routes (
    id                  TEXT PRIMARY KEY,
    subdomain           TEXT NOT NULL,
    full_domain         TEXT NOT NULL UNIQUE,
    zone_id             TEXT NOT NULL REFERENCES dns_zones(id) ON DELETE RESTRICT,
    edge_node_id        TEXT NOT NULL REFERENCES edge_nodes(id) ON DELETE RESTRICT,
    source_type         TEXT NOT NULL,
    container_name      TEXT NOT NULL DEFAULT '',
    container_id        TEXT NOT NULL DEFAULT '',
    forward_mode        TEXT NOT NULL DEFAULT 'direct',
    upstream_scheme     TEXT NOT NULL DEFAULT 'http',
    local_target_host   TEXT NOT NULL,
    local_target_port   INTEGER NOT NULL,
    effective_dial      TEXT NOT NULL,
    relay_token_hmac    TEXT NOT NULL DEFAULT '',
    preset              TEXT NOT NULL DEFAULT 'web',
    max_body_mb         INTEGER NOT NULL DEFAULT 0,
    read_timeout_sec    INTEGER NOT NULL DEFAULT 60,
    dns_record_id       TEXT NOT NULL DEFAULT '',
    dns_proxied         INTEGER NOT NULL DEFAULT 0,
    enabled             INTEGER NOT NULL DEFAULT 1,
    lifecycle_state     TEXT NOT NULL DEFAULT 'pending',
    last_error          TEXT NOT NULL DEFAULT '',
    local_healthy       INTEGER NOT NULL DEFAULT 0,
    caddy_synced        INTEGER NOT NULL DEFAULT 0,
    origin_tls_ok       INTEGER NOT NULL DEFAULT 0,
    origin_cert_issuer  TEXT NOT NULL DEFAULT '',
    origin_cert_expires DATETIME,
    origin_cert_days    INTEGER NOT NULL DEFAULT -1,
    public_cert_issuer  TEXT NOT NULL DEFAULT '',
    public_http_code    INTEGER NOT NULL DEFAULT 0,
    public_latency_ms   INTEGER NOT NULL DEFAULT -1,
    last_probed_at      DATETIME,
    note                TEXT NOT NULL DEFAULT '',
    created_at          DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at          DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS probe_history (
    id                  INTEGER PRIMARY KEY AUTOINCREMENT,
    route_id            TEXT NOT NULL REFERENCES routes(id) ON DELETE CASCADE,
    is_ok               INTEGER NOT NULL,
    ts_rtt_ms           INTEGER NOT NULL,
    public_latency_ms   INTEGER NOT NULL,
    public_http_code    INTEGER NOT NULL,
    fail_stage          TEXT NOT NULL DEFAULT '',
    checked_at          DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_probe_history_route_time ON probe_history(route_id, checked_at DESC);
CREATE INDEX IF NOT EXISTS idx_probe_history_cleanup ON probe_history(checked_at);

CREATE TABLE IF NOT EXISTS audit_events (
    id                  INTEGER PRIMARY KEY AUTOINCREMENT,
    severity            TEXT NOT NULL,
    category            TEXT NOT NULL,
    target_id           TEXT NOT NULL DEFAULT '',
    target_label        TEXT NOT NULL DEFAULT '',
    message             TEXT NOT NULL,
    detail_json         TEXT NOT NULL DEFAULT '{}',
    actor               TEXT NOT NULL DEFAULT 'system',
    created_at          DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_audit_events_time ON audit_events(created_at DESC);
`
	_, err := s.db.Exec(schema)
	return err
}

// --- Admin & Sessions ---

func (s *Store) IsInitialized(ctx context.Context) (bool, error) {
	var cnt int
	if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM admin_users").Scan(&cnt); err != nil {
		return false, err
	}
	return cnt > 0, nil
}

// SetAdminPassword sets the password of the single console administrator,
// creating it on first use. Legacy databases may hold several rows; the
// earliest one is treated as the administrator.
func (s *Store) SetAdminPassword(ctx context.Context, passwordHash string) error {
	now := time.Now().UTC()
	res, err := s.db.ExecContext(ctx,
		"UPDATE admin_users SET password_hash = ?, updated_at = ? WHERE id = (SELECT MIN(id) FROM admin_users)",
		passwordHash, now)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n > 0 {
		return nil
	}
	_, err = s.db.ExecContext(ctx,
		"INSERT INTO admin_users (username, password_hash, created_at, updated_at) VALUES ('admin', ?, ?, ?)",
		passwordHash, now, now)
	return err
}

func (s *Store) GetAdmin(ctx context.Context) (*AdminUser, error) {
	var u AdminUser
	err := s.db.QueryRowContext(ctx,
		"SELECT id, username, password_hash, created_at, updated_at FROM admin_users ORDER BY id LIMIT 1").
		Scan(&u.ID, &u.Username, &u.PasswordHash, &u.CreatedAt, &u.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &u, err
}

func (s *Store) CreateSession(ctx context.Context, tokenSHA256, username string, expiresAt time.Time) error {
	_, _ = s.db.ExecContext(ctx, "DELETE FROM web_sessions WHERE expires_at < ?", time.Now().UTC())
	_, err := s.db.ExecContext(ctx,
		"INSERT INTO web_sessions (token_sha256, username, expires_at, created_at) VALUES (?, ?, ?, ?)",
		tokenSHA256, username, expiresAt.UTC(), time.Now().UTC())
	return err
}

func (s *Store) ValidateSession(ctx context.Context, tokenSHA256 string) (string, bool) {
	var username string
	var exp time.Time
	err := s.db.QueryRowContext(ctx,
		"SELECT username, expires_at FROM web_sessions WHERE token_sha256 = ?", tokenSHA256).Scan(&username, &exp)
	if err != nil || time.Now().UTC().After(exp) {
		return "", false
	}
	return username, true
}

func (s *Store) DeleteSession(ctx context.Context, tokenSHA256 string) error {
	_, err := s.db.ExecContext(ctx, "DELETE FROM web_sessions WHERE token_sha256 = ?", tokenSHA256)
	return err
}

// --- API Tokens ---

func (s *Store) CreateAPIToken(ctx context.Context, t APIToken) error {
	_, err := s.db.ExecContext(ctx, `
INSERT INTO api_tokens (id, name, token_prefix, token_sha256, scope, expires_at, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?)`,
		t.ID, t.Name, t.TokenPrefix, t.TokenSHA256, t.Scope, t.ExpiresAt, time.Now().UTC())
	return err
}

func (s *Store) ListAPITokens(ctx context.Context) ([]APIToken, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT id, name, token_prefix, scope, expires_at, last_used_at, created_at
FROM api_tokens ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]APIToken, 0)
	for rows.Next() {
		var t APIToken
		var exp, used sql.NullTime
		if err := rows.Scan(&t.ID, &t.Name, &t.TokenPrefix, &t.Scope, &exp, &used, &t.CreatedAt); err != nil {
			return nil, err
		}
		if exp.Valid {
			tm := exp.Time
			t.ExpiresAt = &tm
		}
		if used.Valid {
			tm := used.Time
			t.LastUsedAt = &tm
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (s *Store) LookupAPIToken(ctx context.Context, sha256Hex string) (*APIToken, error) {
	var t APIToken
	var exp, used sql.NullTime
	err := s.db.QueryRowContext(ctx, `
SELECT id, name, token_prefix, scope, expires_at, last_used_at, created_at
FROM api_tokens WHERE token_sha256 = ?`, sha256Hex).Scan(
		&t.ID, &t.Name, &t.TokenPrefix, &t.Scope, &exp, &used, &t.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if exp.Valid {
		tm := exp.Time
		if time.Now().UTC().After(tm) {
			return nil, ErrNotFound
		}
		t.ExpiresAt = &tm
	}
	_, _ = s.db.ExecContext(ctx, "UPDATE api_tokens SET last_used_at = ? WHERE id = ?", time.Now().UTC(), t.ID)
	return &t, nil
}

func (s *Store) DeleteAPIToken(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, "DELETE FROM api_tokens WHERE id = ?", id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// --- DNS Providers & Zones ---

func (s *Store) CreateDNSProvider(ctx context.Context, p DNSProviderRecord) error {
	now := time.Now().UTC()
	_, err := s.db.ExecContext(ctx, `
INSERT INTO dns_providers (id, name, provider_type, credentials_enc, masked_cred, status, last_checked_at, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		p.ID, p.Name, p.ProviderType, p.CredentialsEnc, p.MaskedCred, p.Status, now, now, now)
	return err
}

func (s *Store) GetDNSProvider(ctx context.Context, id string) (*DNSProviderRecord, error) {
	var p DNSProviderRecord
	var chk sql.NullTime
	err := s.db.QueryRowContext(ctx, `
SELECT id, name, provider_type, credentials_enc, masked_cred, status, last_checked_at, created_at, updated_at
FROM dns_providers WHERE id = ?`, id).Scan(
		&p.ID, &p.Name, &p.ProviderType, &p.CredentialsEnc, &p.MaskedCred, &p.Status, &chk, &p.CreatedAt, &p.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if chk.Valid {
		tm := chk.Time
		p.LastCheckedAt = &tm
	}
	return &p, err
}

func (s *Store) ListDNSProviders(ctx context.Context) ([]DNSProviderRecord, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT id, name, provider_type, credentials_enc, masked_cred, status, last_checked_at, created_at, updated_at
FROM dns_providers ORDER BY created_at ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]DNSProviderRecord, 0)
	for rows.Next() {
		var p DNSProviderRecord
		var chk sql.NullTime
		if err := rows.Scan(&p.ID, &p.Name, &p.ProviderType, &p.CredentialsEnc, &p.MaskedCred, &p.Status, &chk, &p.CreatedAt, &p.UpdatedAt); err != nil {
			return nil, err
		}
		if chk.Valid {
			tm := chk.Time
			p.LastCheckedAt = &tm
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *Store) DeleteDNSProvider(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, "DELETE FROM dns_providers WHERE id = ?", id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) UpsertDNSZones(ctx context.Context, providerID string, zones []DNSZone) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var existingDefaults int
	_ = tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM dns_zones WHERE is_default = 1").Scan(&existingDefaults)

	now := time.Now().UTC()
	for idx, z := range zones {
		isDef := 0
		if existingDefaults == 0 && idx == 0 {
			isDef = 1
			existingDefaults = 1
		}
		sup := 0
		if z.SupportsProxy {
			sup = 1
		}
		_, err := tx.ExecContext(ctx, `
INSERT INTO dns_zones (id, provider_id, zone_name, remote_zone_id, supports_proxy, is_default, synced_at)
VALUES (?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(provider_id, zone_name) DO UPDATE SET
    remote_zone_id = excluded.remote_zone_id,
    supports_proxy = excluded.supports_proxy,
    synced_at = excluded.synced_at`,
			z.ID, providerID, z.ZoneName, z.RemoteZoneID, sup, isDef, now)
		if err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) ListDNSZones(ctx context.Context) ([]DNSZone, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT z.id, z.provider_id, p.name, p.provider_type, z.zone_name, z.remote_zone_id, z.supports_proxy, z.is_default, z.synced_at,
       (SELECT COUNT(*) FROM routes r WHERE r.zone_id = z.id) AS route_count
FROM dns_zones z
JOIN dns_providers p ON p.id = z.provider_id
ORDER BY z.is_default DESC, z.zone_name ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]DNSZone, 0)
	for rows.Next() {
		var z DNSZone
		var sup, def int
		if err := rows.Scan(&z.ID, &z.ProviderID, &z.ProviderName, &z.ProviderType, &z.ZoneName, &z.RemoteZoneID, &sup, &def, &z.SyncedAt, &z.RouteCount); err != nil {
			return nil, err
		}
		z.SupportsProxy = sup == 1
		z.IsDefault = def == 1
		out = append(out, z)
	}
	return out, rows.Err()
}

func (s *Store) GetDNSZone(ctx context.Context, id string) (*DNSZone, error) {
	zones, err := s.ListDNSZones(ctx)
	if err != nil {
		return nil, err
	}
	for _, z := range zones {
		if z.ID == id || z.ZoneName == id {
			return &z, nil
		}
	}
	return nil, ErrNotFound
}

func (s *Store) SetDefaultZone(ctx context.Context, zoneID string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, "UPDATE dns_zones SET is_default = 0"); err != nil {
		return err
	}
	res, err := tx.ExecContext(ctx, "UPDATE dns_zones SET is_default = 1 WHERE id = ?", zoneID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return tx.Commit()
}

// --- Edge Nodes ---

func (s *Store) CreateEdgeNode(ctx context.Context, n EdgeNode) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var cnt int
	_ = tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM edge_nodes").Scan(&cnt)
	if cnt == 0 {
		n.IsDefault = true
	}
	if n.IsDefault {
		_, _ = tx.ExecContext(ctx, "UPDATE edge_nodes SET is_default = 0")
	}

	now := time.Now().UTC()
	_, err = tx.ExecContext(ctx, `
INSERT INTO edge_nodes (
    id, name, region, public_ipv4, public_ipv6, tailscale_ip, caddy_admin_url,
    auth_secret_enc, masked_secret, is_default, enabled, health_status,
    ts_conn_type, ts_derp_region, ts_rtt_ms, caddy_version, last_seen_at, created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		n.ID, n.Name, n.Region, n.PublicIPv4, n.PublicIPv6, n.TailscaleIP, n.CaddyAdminURL,
		n.AuthSecretEnc, n.MaskedSecret, boolToInt(n.IsDefault), boolToInt(n.Enabled), n.HealthStatus,
		n.TSConnType, n.TSDERPRegion, n.TSRTTMs, n.CaddyVersion, n.LastSeenAt, now, now)
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) ListEdgeNodes(ctx context.Context) ([]EdgeNode, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT id, name, region, public_ipv4, public_ipv6, tailscale_ip, caddy_admin_url,
       auth_secret_enc, masked_secret, is_default, enabled, health_status,
       ts_conn_type, ts_derp_region, ts_rtt_ms, caddy_version, last_seen_at, created_at, updated_at,
       (SELECT COUNT(*) FROM routes r WHERE r.edge_node_id = edge_nodes.id) AS route_count
FROM edge_nodes
ORDER BY is_default DESC, created_at ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]EdgeNode, 0)
	for rows.Next() {
		var n EdgeNode
		var def, en int
		var seen sql.NullTime
		if err := rows.Scan(
			&n.ID, &n.Name, &n.Region, &n.PublicIPv4, &n.PublicIPv6, &n.TailscaleIP, &n.CaddyAdminURL,
			&n.AuthSecretEnc, &n.MaskedSecret, &def, &en, &n.HealthStatus,
			&n.TSConnType, &n.TSDERPRegion, &n.TSRTTMs, &n.CaddyVersion, &seen, &n.CreatedAt, &n.UpdatedAt,
			&n.RouteCount,
		); err != nil {
			return nil, err
		}
		n.IsDefault = def == 1
		n.Enabled = en == 1
		if seen.Valid {
			tm := seen.Time
			n.LastSeenAt = &tm
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

func (s *Store) GetEdgeNode(ctx context.Context, idOrName string) (*EdgeNode, error) {
	nodes, err := s.ListEdgeNodes(ctx)
	if err != nil {
		return nil, err
	}
	if idOrName == "" {
		for _, n := range nodes {
			if n.IsDefault {
				return &n, nil
			}
		}
		if len(nodes) > 0 {
			return &nodes[0], nil
		}
		return nil, ErrNotFound
	}
	for _, n := range nodes {
		if n.ID == idOrName || n.Name == idOrName {
			return &n, nil
		}
	}
	return nil, ErrNotFound
}

func (s *Store) UpdateEdgeNodeHealth(ctx context.Context, id, health, connType, derpRegion string, rttMs int, caddyVer string) error {
	var seen any
	if health == "online" || health == "degraded" || health == "drift" {
		seen = time.Now().UTC()
	}
	_, err := s.db.ExecContext(ctx, `
UPDATE edge_nodes
SET health_status = ?, ts_conn_type = ?, ts_derp_region = ?, ts_rtt_ms = ?,
    caddy_version = CASE WHEN ? != '' THEN ? ELSE caddy_version END,
    last_seen_at = COALESCE(?, last_seen_at),
    updated_at = ?
WHERE id = ?`,
		health, connType, derpRegion, rttMs, caddyVer, caddyVer, seen, time.Now().UTC(), id)
	return err
}

func (s *Store) SetDefaultEdgeNode(ctx context.Context, id string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, "UPDATE edge_nodes SET is_default = 0"); err != nil {
		return err
	}
	res, err := tx.ExecContext(ctx, "UPDATE edge_nodes SET is_default = 1 WHERE id = ?", id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return tx.Commit()
}

func (s *Store) DeleteEdgeNode(ctx context.Context, id string) error {
	var routeCount int
	if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM routes WHERE edge_node_id = ?", id).Scan(&routeCount); err != nil {
		return err
	}
	if routeCount > 0 {
		return fmt.Errorf("节点下仍绑定 %d 条反代路由，请先迁移或删除对应路由", routeCount)
	}
	res, err := s.db.ExecContext(ctx, "DELETE FROM edge_nodes WHERE id = ?", id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// --- Routes ---

const selectRoutesSQL = `
SELECT r.id, r.subdomain, r.full_domain, r.zone_id, z.zone_name, z.supports_proxy,
       r.edge_node_id, n.name, n.public_ipv4, n.ts_rtt_ms, n.ts_conn_type,
       r.source_type, r.container_name, r.container_id, r.forward_mode, r.upstream_scheme,
       r.local_target_host, r.local_target_port, r.effective_dial, r.relay_token_hmac,
       r.preset, r.max_body_mb, r.read_timeout_sec, r.dns_record_id, r.dns_proxied, r.enabled,
       r.lifecycle_state, r.last_error, r.local_healthy, r.caddy_synced, r.origin_tls_ok,
       r.origin_cert_issuer, r.origin_cert_expires, r.origin_cert_days, r.public_cert_issuer,
       r.public_http_code, r.public_latency_ms, r.last_probed_at, r.note, r.created_at, r.updated_at
FROM routes r
JOIN dns_zones z ON z.id = r.zone_id
JOIN edge_nodes n ON n.id = r.edge_node_id
`

func scanRoute(scanner interface{ Scan(dest ...any) error }) (Route, error) {
	var r Route
	var supProxy, proxied, en, locOK, cadOK, tlsOK int
	var certExp, probed sql.NullTime
	err := scanner.Scan(
		&r.ID, &r.Subdomain, &r.FullDomain, &r.ZoneID, &r.ZoneName, &supProxy,
		&r.EdgeNodeID, &r.EdgeNodeName, &r.EdgeNodePublicIP, &r.EdgeNodeTSRTTMs, &r.EdgeNodeTSType,
		&r.SourceType, &r.ContainerName, &r.ContainerID, &r.ForwardMode, &r.UpstreamScheme,
		&r.LocalTargetHost, &r.LocalTargetPort, &r.EffectiveDial, &r.RelayTokenHMAC,
		&r.Preset, &r.MaxBodyMB, &r.ReadTimeoutSec, &r.DNSRecordID, &proxied, &en,
		&r.LifecycleState, &r.LastError, &locOK, &cadOK, &tlsOK,
		&r.OriginCertIssuer, &certExp, &r.OriginCertDays, &r.PublicCertIssuer,
		&r.PublicHTTPCode, &r.PublicLatencyMs, &probed, &r.Note, &r.CreatedAt, &r.UpdatedAt,
	)
	if err != nil {
		return r, err
	}
	r.SupportsProxy = supProxy == 1
	r.DNSProxied = proxied == 1
	r.Enabled = en == 1
	r.LocalHealthy = locOK == 1
	r.CaddySynced = cadOK == 1
	r.OriginTLSOK = tlsOK == 1
	if certExp.Valid {
		tm := certExp.Time
		r.OriginCertExpires = &tm
	}
	if probed.Valid {
		tm := probed.Time
		r.LastProbedAt = &tm
	}
	return r, nil
}

func (s *Store) ListRoutes(ctx context.Context) ([]Route, error) {
	rows, err := s.db.QueryContext(ctx, selectRoutesSQL+" ORDER BY r.created_at DESC")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]Route, 0)
	for rows.Next() {
		r, err := scanRoute(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Store) GetRoute(ctx context.Context, idOrDomain string) (*Route, error) {
	row := s.db.QueryRowContext(ctx, selectRoutesSQL+" WHERE r.id = ? OR r.full_domain = ? LIMIT 1", idOrDomain, idOrDomain)
	r, err := scanRoute(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &r, err
}

func (s *Store) UpsertRoute(ctx context.Context, r Route) error {
	now := time.Now().UTC()
	_, err := s.db.ExecContext(ctx, `
INSERT INTO routes (
    id, subdomain, full_domain, zone_id, edge_node_id,
    source_type, container_name, container_id, forward_mode, upstream_scheme,
    local_target_host, local_target_port, effective_dial, relay_token_hmac,
    preset, max_body_mb, read_timeout_sec, dns_record_id, dns_proxied, enabled,
    lifecycle_state, last_error, local_healthy, caddy_synced, origin_tls_ok,
    origin_cert_issuer, origin_cert_expires, origin_cert_days, public_cert_issuer,
    public_http_code, public_latency_ms, last_probed_at, note, created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(id) DO UPDATE SET
    subdomain = excluded.subdomain,
    full_domain = excluded.full_domain,
    zone_id = excluded.zone_id,
    edge_node_id = excluded.edge_node_id,
    source_type = excluded.source_type,
    container_name = excluded.container_name,
    container_id = excluded.container_id,
    forward_mode = excluded.forward_mode,
    upstream_scheme = excluded.upstream_scheme,
    local_target_host = excluded.local_target_host,
    local_target_port = excluded.local_target_port,
    effective_dial = excluded.effective_dial,
    relay_token_hmac = excluded.relay_token_hmac,
    preset = excluded.preset,
    max_body_mb = excluded.max_body_mb,
    read_timeout_sec = excluded.read_timeout_sec,
    dns_record_id = excluded.dns_record_id,
    dns_proxied = excluded.dns_proxied,
    enabled = excluded.enabled,
    lifecycle_state = excluded.lifecycle_state,
    last_error = excluded.last_error,
    caddy_synced = excluded.caddy_synced,
    note = excluded.note,
    updated_at = excluded.updated_at`,
		r.ID, r.Subdomain, r.FullDomain, r.ZoneID, r.EdgeNodeID,
		r.SourceType, r.ContainerName, r.ContainerID, r.ForwardMode, r.UpstreamScheme,
		r.LocalTargetHost, r.LocalTargetPort, r.EffectiveDial, r.RelayTokenHMAC,
		r.Preset, r.MaxBodyMB, r.ReadTimeoutSec, r.DNSRecordID, boolToInt(r.DNSProxied), boolToInt(r.Enabled),
		r.LifecycleState, r.LastError, boolToInt(r.LocalHealthy), boolToInt(r.CaddySynced), boolToInt(r.OriginTLSOK),
		r.OriginCertIssuer, r.OriginCertExpires, r.OriginCertDays, r.PublicCertIssuer,
		r.PublicHTTPCode, r.PublicLatencyMs, r.LastProbedAt, r.Note, now, now,
	)
	return err
}

// SaveProbeBatch updates route probe snapshots and appends probe_history in a single lightweight transaction.
func (s *Store) SaveProbeBatch(ctx context.Context, updates []Route, samples []ProbeSample) error {
	if len(updates) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	now := time.Now().UTC()
	for _, r := range updates {
		_, err := tx.ExecContext(ctx, `
UPDATE routes SET
    lifecycle_state = ?,
    last_error = ?,
    local_healthy = ?,
    caddy_synced = ?,
    origin_tls_ok = ?,
    origin_cert_issuer = ?,
    origin_cert_expires = ?,
    origin_cert_days = ?,
    public_cert_issuer = ?,
    public_http_code = ?,
    public_latency_ms = ?,
    last_probed_at = ?,
    updated_at = ?
WHERE id = ?`,
			r.LifecycleState, r.LastError, boolToInt(r.LocalHealthy), boolToInt(r.CaddySynced), boolToInt(r.OriginTLSOK),
			r.OriginCertIssuer, r.OriginCertExpires, r.OriginCertDays, r.PublicCertIssuer,
			r.PublicHTTPCode, r.PublicLatencyMs, now, now, r.ID)
		if err != nil {
			return err
		}
	}

	for _, sm := range samples {
		_, err := tx.ExecContext(ctx, `
INSERT INTO probe_history (route_id, is_ok, ts_rtt_ms, public_latency_ms, public_http_code, fail_stage, checked_at)
VALUES (?, ?, ?, ?, ?, ?, ?)`,
			sm.RouteID, boolToInt(sm.IsOK), sm.TSRTTMs, sm.PublicLatencyMs, sm.PublicHTTPCode, sm.FailStage, now)
		if err != nil {
			return err
		}
	}

	// Opportunistically prune history older than 7 days to keep DB file tiny.
	_, _ = tx.ExecContext(ctx, "DELETE FROM probe_history WHERE checked_at < ?", now.Add(-7*24*time.Hour))
	return tx.Commit()
}

func (s *Store) ListProbeHistory(ctx context.Context, routeID string, limit int) ([]ProbeSample, error) {
	if limit <= 0 || limit > 200 {
		limit = 36
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT id, route_id, is_ok, ts_rtt_ms, public_latency_ms, public_http_code, fail_stage, checked_at
FROM probe_history WHERE route_id = ?
ORDER BY checked_at DESC LIMIT ?`, routeID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	tmp := make([]ProbeSample, 0, limit)
	for rows.Next() {
		var sm ProbeSample
		var ok int
		if err := rows.Scan(&sm.ID, &sm.RouteID, &ok, &sm.TSRTTMs, &sm.PublicLatencyMs, &sm.PublicHTTPCode, &sm.FailStage, &sm.CheckedAt); err != nil {
			return nil, err
		}
		sm.IsOK = ok == 1
		tmp = append(tmp, sm)
	}
	// Reverse to chronological left-to-right order for sparkline/uptime bars.
	for i, j := 0, len(tmp)-1; i < j; i, j = i+1, j-1 {
		tmp[i], tmp[j] = tmp[j], tmp[i]
	}
	return tmp, rows.Err()
}

func (s *Store) DeleteRoute(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, "DELETE FROM routes WHERE id = ?", id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// --- Audit Events ---

func (s *Store) AppendAudit(ctx context.Context, ev AuditEvent) {
	if ev.DetailJSON == "" {
		ev.DetailJSON = "{}"
	}
	if ev.Actor == "" {
		ev.Actor = "system"
	}
	_, _ = s.db.ExecContext(ctx, `
INSERT INTO audit_events (severity, category, target_id, target_label, message, detail_json, actor, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		ev.Severity, ev.Category, ev.TargetID, ev.TargetLabel, ev.Message, ev.DetailJSON, ev.Actor, time.Now().UTC())
}

func (s *Store) ListAuditEvents(ctx context.Context, limit int) ([]AuditEvent, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT id, severity, category, target_id, target_label, message, detail_json, actor, created_at
FROM audit_events ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]AuditEvent, 0)
	for rows.Next() {
		var ev AuditEvent
		if err := rows.Scan(&ev.ID, &ev.Severity, &ev.Category, &ev.TargetID, &ev.TargetLabel, &ev.Message, &ev.DetailJSON, &ev.Actor, &ev.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, ev)
	}
	return out, rows.Err()
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
