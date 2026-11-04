package store_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/llanc/vantu/internal/store"
	"github.com/llanc/vantu/internal/ulid"
)

func TestStoreCRUDAndWalLifecycle(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "vantu-test.db")

	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()

	initOK, err := st.IsInitialized(ctx)
	if err != nil || initOK {
		t.Fatalf("expected uninitialized store, got %v, err=%v", initOK, err)
	}

	if err := st.SetAdminPassword(ctx, "$argon2id$hash"); err != nil {
		t.Fatalf("create admin: %v", err)
	}
	initOK, _ = st.IsInitialized(ctx)
	if !initOK {
		t.Fatal("expected initialized store after SetAdminPassword")
	}
	if err := st.SetAdminPassword(ctx, "$argon2id$hash2"); err != nil {
		t.Fatalf("update admin: %v", err)
	}
	admin, err := st.GetAdmin(ctx)
	if err != nil || admin.PasswordHash != "$argon2id$hash2" || admin.Username != "admin" {
		t.Fatalf("expected single updated admin, got %+v, err=%v", admin, err)
	}

	// DNS Provider & Zone
	provID := ulid.New()
	if err := st.CreateDNSProvider(ctx, store.DNSProviderRecord{
		ID:             provID,
		Name:           "Cloudflare",
		ProviderType:   "cloudflare",
		CredentialsEnc: "enc:v1:abc",
		MaskedCred:     "cf_****1234",
		Status:         "active",
	}); err != nil {
		t.Fatalf("create dns provider: %v", err)
	}

	zoneID := ulid.New()
	if err := st.UpsertDNSZones(ctx, provID, []store.DNSZone{
		{ID: zoneID, ProviderID: provID, ZoneName: "xiaofantu.com", RemoteZoneID: "z_1", SupportsProxy: true},
	}); err != nil {
		t.Fatalf("upsert zones: %v", err)
	}

	zones, err := st.ListDNSZones(ctx)
	if err != nil || len(zones) != 1 || !zones[0].IsDefault {
		t.Fatalf("unexpected zones: %+v, err=%v", zones, err)
	}

	// Edge Node
	nodeID := ulid.New()
	if err := st.CreateEdgeNode(ctx, store.EdgeNode{
		ID:            nodeID,
		Name:          "tokyo-vps",
		Region:        "Tokyo",
		PublicIPv4:    "45.76.210.18",
		TailscaleIP:   "100.116.63.43",
		CaddyAdminURL: "http://100.116.63.43:2019",
		AuthSecretEnc: "enc:v1:sec",
		MaskedSecret:  "vtu****9999",
		Enabled:       true,
		HealthStatus:  "online",
	}); err != nil {
		t.Fatalf("create node: %v", err)
	}

	// Route
	routeID := ulid.New()
	rt := store.Route{
		ID:               routeID,
		Subdomain:        "api",
		FullDomain:       "api.xiaofantu.com",
		ZoneID:           zoneID,
		EdgeNodeID:       nodeID,
		SourceType:       "docker",
		ContainerName:    "sub2api",
		ForwardMode:      "relay",
		UpstreamScheme:   "http",
		LocalTargetHost:  "172.18.0.4",
		LocalTargetPort:  3000,
		EffectiveDial:    "100.103.81.50:18080",
		Preset:           "sse",
		ReadTimeoutSec:   600,
		DNSRecordID:      "rec_123",
		DNSProxied:       true,
		Enabled:          true,
		LifecycleState:   "active",
		LocalHealthy:     true,
		CaddySynced:      true,
		OriginTLSOK:      true,
		OriginCertIssuer: "Let's Encrypt",
		OriginCertDays:   88,
		PublicHTTPCode:   200,
		PublicLatencyMs:  42,
	}
	if err := st.UpsertRoute(ctx, rt); err != nil {
		t.Fatalf("upsert route: %v", err)
	}

	// Guard: cannot delete node while route is bound
	if err := st.DeleteEdgeNode(ctx, nodeID); err == nil {
		t.Fatal("expected error deleting edge node with active route")
	}

	// Probe History
	nowExp := time.Now().UTC().Add(85 * 24 * time.Hour)
	rt.OriginCertExpires = &nowExp
	if err := st.SaveProbeBatch(ctx, []store.Route{rt}, []store.ProbeSample{
		{RouteID: routeID, IsOK: true, TSRTTMs: 38, PublicLatencyMs: 42, PublicHTTPCode: 200},
	}); err != nil {
		t.Fatalf("save probe batch: %v", err)
	}

	hist, err := st.ListProbeHistory(ctx, routeID, 36)
	if err != nil || len(hist) != 1 || !hist[0].IsOK {
		t.Fatalf("unexpected history: %+v, err=%v", hist, err)
	}

	if err := st.DeleteRoute(ctx, routeID); err != nil {
		t.Fatalf("delete route: %v", err)
	}
	if err := st.DeleteEdgeNode(ctx, nodeID); err != nil {
		t.Fatalf("delete node after route removal: %v", err)
	}
}
