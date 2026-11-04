package store_test

import (
	"database/sql"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/llanc/vantu/internal/store"
)

// Databases created by v0.1.x lack the basic auth and proxy option columns and must be upgraded on open.
func TestOpenUpgradesLegacyRoutesTable(t *testing.T) {
	ctx := t.Context()
	dbPath := filepath.Join(t.TempDir(), "legacy.db")
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.CreateDNSProvider(ctx, store.DNSProviderRecord{ID: "p1", Name: "cf", ProviderType: "cloudflare", Status: "ok"}); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertDNSZones(ctx, "p1", []store.DNSZone{{ID: "z1", ProviderID: "p1", ZoneName: "example.com", RemoteZoneID: "rz1"}}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateEdgeNode(ctx, store.EdgeNode{ID: "n1", Name: "edge", PublicIPv4: "1.2.3.4", CaddyAdminURL: "http://x:2019", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	for _, r := range []store.Route{
		{ID: "r-https", Subdomain: "pve", FullDomain: "pve.example.com", ZoneID: "z1", EdgeNodeID: "n1", UpstreamScheme: "https", Preset: "web", ForwardMode: "direct"},
		{ID: "r-http", Subdomain: "app", FullDomain: "app.example.com", ZoneID: "z1", EdgeNodeID: "n1", UpstreamScheme: "http", Preset: "web", ForwardMode: "direct"},
	} {
		if err := st.UpsertRoute(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	_ = st.Close()

	raw, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, col := range []string{"basic_auth_user", "basic_auth_hash", "proxy_options"} {
		if _, err := raw.Exec("ALTER TABLE routes DROP COLUMN " + col); err != nil {
			t.Fatalf("drop %s: %v", col, err)
		}
	}
	_ = raw.Close()

	st, err = store.Open(dbPath)
	if err != nil {
		t.Fatalf("reopen legacy db: %v", err)
	}
	defer st.Close()
	routes, err := st.ListRoutes(ctx)
	if err != nil {
		t.Fatalf("list routes after upgrade: %v", err)
	}
	got := map[string]bool{}
	for _, r := range routes {
		got[r.ID] = r.Proxy.TLSSkipVerify
	}
	if !got["r-https"] || got["r-http"] {
		t.Fatalf("legacy HTTPS routes must keep skipping verification, got %v", got)
	}
}
