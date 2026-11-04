package orchestrator_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/llanc/vantu/internal/api"
	"github.com/llanc/vantu/internal/config"
	"github.com/llanc/vantu/internal/crypto"
	"github.com/llanc/vantu/internal/discovery"
	"github.com/llanc/vantu/internal/events"
	"github.com/llanc/vantu/internal/orchestrator"
	"github.com/llanc/vantu/internal/prober"
	"github.com/llanc/vantu/internal/relay"
	"github.com/llanc/vantu/internal/store"
	"github.com/llanc/vantu/internal/tailscale"
)

// TestFullLifecyclePublishRelayProbeAndDriftReconcile verifies the entire Vantu control & data plane:
// Mock Cloudflare v4 + Mock Edge Caddy Admin + Real TLS Origin Proxy + Real Vantu Local Relay + Real Upstream + 5-Hop Prober + REST API.
func TestFullLifecyclePublishRelayProbeAndDriftReconcile(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// 1. Start a real local Upstream HTTP/SSE service (simulating unexposed local app).
	upstreamSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get(relay.HeaderRelayKey) != "" {
			t.Errorf("security leak: internal X-Vantu-Relay-Key was not stripped before hitting upstream")
		}
		if r.URL.Path == "/sse" {
			w.Header().Set("Content-Type", "text/event-stream")
			fl, _ := w.(http.Flusher)
			_, _ = fmt.Fprintf(w, "data: token-1\n\n")
			if fl != nil {
				fl.Flush()
			}
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"service":"sub2api","host":%q}`, r.Host)
	}))
	defer upstreamSrv.Close()
	_, upPortStr, _ := net.SplitHostPort(upstreamSrv.Listener.Addr().String())
	upPort, _ := strconv.Atoi(upPortStr)

	// 2. Start Mock Cloudflare v4 API Server.
	var cfMu sync.Mutex
	cfRecords := make(map[string]map[string]any)
	mockCF := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer cf_test_token_ok" {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"success":false,"errors":[{"code":10000,"message":"Authentication error"}]}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		cfMu.Lock()
		defer cfMu.Unlock()

		switch {
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/zones") && !strings.Contains(r.URL.Path, "dns_records"):
			_, _ = w.Write([]byte(`{"success":true,"result":[{"id":"zone_xft_1","name":"xiaofantu.com","status":"active"}]}`))
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/dns_records"):
			name := r.URL.Query().Get("name")
			var list []map[string]any
			for _, rec := range cfRecords {
				if rec["name"] == name {
					list = append(list, rec)
				}
			}
			b, _ := json.Marshal(map[string]any{"success": true, "result": list})
			_, _ = w.Write(b)
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/dns_records"):
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			body["id"] = fmt.Sprintf("cf_rec_%d", len(cfRecords)+1)
			cfRecords[body["id"].(string)] = body
			b, _ := json.Marshal(map[string]any{"success": true, "result": body})
			_, _ = w.Write(b)
		case r.Method == http.MethodPatch && strings.Contains(r.URL.Path, "/dns_records/"):
			id := filepath.Base(r.URL.Path)
			rec, ok := cfRecords[id]
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				_, _ = w.Write([]byte(`{"success":false,"errors":[{"code":81044,"message":"Record does not exist"}]}`))
				return
			}
			var patch map[string]any
			_ = json.NewDecoder(r.Body).Decode(&patch)
			for k, v := range patch {
				rec[k] = v
			}
			b, _ := json.Marshal(map[string]any{"success": true, "result": rec})
			_, _ = w.Write(b)
		case r.Method == http.MethodDelete && strings.Contains(r.URL.Path, "/dns_records/"):
			id := filepath.Base(r.URL.Path)
			delete(cfRecords, id)
			_, _ = w.Write([]byte(`{"success":true,"result":{"id":"` + id + `"}}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer mockCF.Close()

	// 3. Start Mock Edge Caddy Admin API + Real Edge TLS Data-Plane Server!
	const edgeSecret = "vtu_edge_secret_test_123"
	var caddyMu sync.RWMutex
	caddyRoutes := make(map[string]map[string]any) // @id -> route payload

	mockCaddyAdmin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+edgeSecret {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Server", "Caddy/v2.8.4")
		w.Header().Set("Content-Type", "application/json")

		caddyMu.Lock()
		defer caddyMu.Unlock()

		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/config/":
			_, _ = w.Write([]byte(`{"apps":{"http":{"servers":{"vantu_srv":{"listen":[":443"],"routes":[]}}}}}`))
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/config/apps/http/servers/"):
			_, _ = w.Write([]byte(`{"listen":[":443"],"routes":[]}`))
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/routes"):
			var payload map[string]any
			_ = json.NewDecoder(r.Body).Decode(&payload)
			id, _ := payload["@id"].(string)
			caddyRoutes[id] = payload
			_, _ = w.Write([]byte(`{}`))
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/id/"):
			id := strings.TrimPrefix(r.URL.Path, "/id/")
			rt, ok := caddyRoutes[id]
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				_, _ = w.Write([]byte(`{"error":"unknown object ID"}`))
				return
			}
			b, _ := json.Marshal(rt)
			_, _ = w.Write(b)
		case r.Method == http.MethodPatch && strings.HasPrefix(r.URL.Path, "/id/"):
			id := strings.TrimPrefix(r.URL.Path, "/id/")
			var payload map[string]any
			_ = json.NewDecoder(r.Body).Decode(&payload)
			caddyRoutes[id] = payload
			_, _ = w.Write([]byte(`{}`))
		case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/id/"):
			id := strings.TrimPrefix(r.URL.Path, "/id/")
			delete(caddyRoutes, id)
			_, _ = w.Write([]byte(`{}`))
		default:
			_, _ = w.Write([]byte(`{}`))
		}
	}))
	defer mockCaddyAdmin.Close()

	// Generate a trusted CA + Leaf Cert ("Let's Encrypt E6" for *.xiaofantu.com, valid 89 days) for Mock Edge HTTPS Data Plane.
	rootPool, serverTLS := generateTestOriginCert(t, "*.xiaofantu.com", 89*24*time.Hour)
	edgeHTTPS := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		caddyMu.RLock()
		var matched map[string]any
		for _, rt := range caddyRoutes {
			// Match host
			rawB, _ := json.Marshal(rt)
			if strings.Contains(string(rawB), strings.Split(r.Host, ":")[0]) {
				matched = rt
				break
			}
		}
		caddyMu.RUnlock()
		if matched == nil {
			http.Error(w, "edge caddy: no route matched", http.StatusBadGateway)
			return
		}

		// Extract dial & headers from Caddy reverse_proxy block and forward to Vantu Local Relay!
		handles, _ := matched["handle"].([]any)
		rev := handles[len(handles)-1].(map[string]any)
		ups := rev["upstreams"].([]any)[0].(map[string]any)
		dialAddr := ups["dial"].(string)

		var setHdrs map[string]any
		if hdrs, ok := rev["headers"].(map[string]any); ok {
			if reqH, ok := hdrs["request"].(map[string]any); ok {
				setHdrs, _ = reqH["set"].(map[string]any)
			}
		}

		targetURL, _ := url.Parse("http://" + dialAddr)
		rp := httputil.NewSingleHostReverseProxy(targetURL)
		origDir := rp.Director
		rp.Director = func(outReq *http.Request) {
			origDir(outReq)
			outReq.Host = r.Host
			for k, vals := range setHdrs {
				if arr, ok := vals.([]any); ok && len(arr) > 0 {
					outReq.Header.Set(k, fmt.Sprint(arr[0]))
				}
			}
		}
		if fi, ok := rev["flush_interval"].(float64); ok && fi == -1 {
			rp.FlushInterval = -1
		}
		rp.ServeHTTP(w, r)
	}))
	edgeHTTPS.TLS = serverTLS
	edgeHTTPS.StartTLS()
	defer edgeHTTPS.Close()

	_, edgeHTTPSPortStr, _ := net.SplitHostPort(edgeHTTPS.Listener.Addr().String())
	edgeHTTPSPort, _ := strconv.Atoi(edgeHTTPSPortStr)

	// 4. Boot Vantu Store, Vault, Local Relay (random free port), Prober, Orchestrator, and API Server!
	dataDir := t.TempDir()
	freeRelayLn, _ := net.Listen("tcp", "127.0.0.1:0")
	_, relayPortStr, _ := net.SplitHostPort(freeRelayLn.Addr().String())
	relayPort, _ := strconv.Atoi(relayPortStr)
	_ = freeRelayLn.Close()

	cfg := config.Config{
		DataDir:       dataDir,
		DBPath:        filepath.Join(dataDir, "vantu.db"),
		SocketPath:    filepath.Join(dataDir, "vantu.sock"),
		RelayBindIP:   "127.0.0.1",
		RelayPort:     relayPort,
		TailscaleIP:   "127.0.0.1",
		ProbeInterval: 1 * time.Hour,
	}

	vault, err := crypto.NewVaultFromKeyOrFile("e2e-master-secret", filepath.Join(dataDir, "master.key"))
	if err != nil {
		t.Fatalf("vault: %v", err)
	}
	st, err := store.Open(cfg.DBPath)
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	defer st.Close()

	hub := events.NewHub()
	docker := discovery.NewDockerWatcher("/nonexistent/docker.sock")
	ts := tailscale.NewInspector("/nonexistent/ts.sock", "127.0.0.1")

	relaySrv := relay.NewServer(st, vault, docker, ts, "127.0.0.1", relayPort)
	go func() {
		_ = relaySrv.Start(ctx)
	}()
	// Wait until Local Relay is bound
	for range 50 {
		if relaySrv.Running() {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !relaySrv.Running() {
		t.Fatal("local relay failed to start")
	}

	// Verify Local Relay rejects unauthenticated TCP/HTTP probes with 403 Forbidden!
	unauthResp, err := http.Get("http://" + relaySrv.BoundAddr() + "/secret")
	if err != nil || unauthResp.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403 Forbidden on unauthenticated relay request, got %v (err=%v)", unauthResp, err)
	}
	_ = unauthResp.Body.Close()

	pr := prober.New(st, vault, docker, ts, hub, prober.Options{
		OriginPortOverride: edgeHTTPSPort,
		PublicPortOverride: edgeHTTPSPort,
		RootCAs:            rootPool,
		PublicDialContext: func(dCtx context.Context, network, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(dCtx, network, edgeHTTPS.Listener.Addr().String())
		},
	})

	apiLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	cfg.ListenAddr = apiLn.Addr().String()

	svc := orchestrator.NewService(cfg, st, vault, docker, ts, relaySrv, pr, hub)
	apiSrv := api.NewServer(cfg, svc)
	apiHTTP := httptest.NewUnstartedServer(apiSrv.Handler())
	apiHTTP.Listener = apiLn
	apiHTTP.Start()
	defer apiHTTP.Close()

	// 5. Setup Admin + Add Cloudflare DNS Provider + Add Edge Node via Service/API.
	if _, zones, err := svc.AddDNSProvider(ctx, orchestrator.AddDNSProviderInput{
		Name:         "Mock Cloudflare",
		ProviderType: "cloudflare",
		APIToken:     "cf_test_token_ok",
		BaseURL:      mockCF.URL,
	}, "test"); err != nil || len(zones) != 1 {
		t.Fatalf("AddDNSProvider failed: %v, zones=%+v", err, zones)
	}

	node, err := svc.AddEdgeNode(ctx, orchestrator.AddEdgeNodeInput{
		Name:          "tokyo-vps",
		Region:        "Tokyo",
		PublicIPv4:    "127.0.0.1",
		TailscaleIP:   "127.0.0.1",
		CaddyAdminURL: mockCaddyAdmin.URL,
		AuthSecret:    edgeSecret,
		IsDefault:     true,
	}, "test")
	if err != nil {
		t.Fatalf("AddEdgeNode failed: %v", err)
	}

	// 6. Test Dry-Run first (must produce 0 DNS records and 0 Caddy routes!).
	dryRes, err := svc.Publish(ctx, orchestrator.PublishInput{
		Target:      fmt.Sprintf("127.0.0.1:%d", upPort),
		Domain:      "api.xiaofantu.com",
		Preset:      "sse",
		ForwardMode: "relay",
		DNSProxied:  true,
		DryRun:      true,
	}, "test")
	if err != nil || !dryRes.DryRun {
		t.Fatalf("dry-run failed: %v", err)
	}
	if len(cfRecords) != 0 || len(caddyRoutes) != 0 {
		t.Fatalf("dry-run leaked side effects: cf=%d caddy=%d", len(cfRecords), len(caddyRoutes))
	}

	// 7. Execute real Publish!
	pubRes, err := svc.Publish(ctx, orchestrator.PublishInput{
		Target:      fmt.Sprintf("127.0.0.1:%d", upPort),
		Domain:      "api.xiaofantu.com",
		Preset:      "sse",
		ForwardMode: "relay",
		DNSProxied:  true,
	}, "test")
	if err != nil {
		t.Fatalf("Publish failed: %v", err)
	}

	rt := pubRes.Route
	if rt.LifecycleState != "active" {
		t.Fatalf("expected active route state, got %q (last_error=%q)", rt.LifecycleState, rt.LastError)
	}
	if !rt.OriginTLSOK || rt.OriginCertDays < 85 || !strings.Contains(rt.OriginCertIssuer, "Let's Encrypt") {
		t.Fatalf("unexpected origin TLS probe result: ok=%v days=%d issuer=%q", rt.OriginTLSOK, rt.OriginCertDays, rt.OriginCertIssuer)
	}
	if rt.PublicHTTPCode != 200 {
		t.Fatalf("expected public HTTP 200, got %d", rt.PublicHTTPCode)
	}

	// 8. Verify real end-to-end SSE request from Public Visitor -> Edge HTTPS -> Vantu Local Relay -> Local Upstream!
	visitorTr := &http.Transport{
		DialContext: func(dCtx context.Context, network, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(dCtx, network, edgeHTTPS.Listener.Addr().String())
		},
		TLSClientConfig: &tls.Config{RootCAs: rootPool, ServerName: "api.xiaofantu.com"},
	}
	visitorCli := &http.Client{Transport: visitorTr, Timeout: 5 * time.Second}
	sseResp, err := visitorCli.Get("https://api.xiaofantu.com/sse")
	if err != nil {
		t.Fatalf("visitor SSE request failed: %v", err)
	}
	sseBody, _ := io.ReadAll(sseResp.Body)
	_ = sseResp.Body.Close()
	if !strings.Contains(string(sseBody), "data: token-1") {
		t.Fatalf("unexpected SSE body: %q", string(sseBody))
	}

	// 9. Simulate Edge Caddy restart config drift & heal via ReconcileNode!
	caddyMu.Lock()
	clear(caddyRoutes)
	caddyMu.Unlock()

	drifted, err := pr.ProbeRoute(ctx, rt.ID)
	if err != nil || drifted.LifecycleState != "config_drift" {
		t.Fatalf("expected config_drift after wiping edge caddy memory, got %+v (err=%v)", drifted, err)
	}

	_, pushedCount, err := svc.ReconcileNode(ctx, node.ID, "test")
	if err != nil || pushedCount != 1 {
		t.Fatalf("ReconcileNode failed: pushed=%d err=%v", pushedCount, err)
	}
	healed, err := pr.ProbeRoute(ctx, rt.ID)
	if err != nil || healed.LifecycleState != "active" {
		t.Fatalf("expected active state after reconcile, got %q (err=%v)", healed.LifecycleState, err)
	}

	// 10. Toggle Cloudflare Proxy OFF, Pause, Resume, and Delete!
	toggled, err := svc.ToggleProxy(ctx, rt.ID, false, "test")
	if err != nil || toggled.DNSProxied {
		t.Fatalf("ToggleProxy failed: %+v err=%v", toggled, err)
	}
	paused, err := svc.PauseRoute(ctx, rt.ID, "test")
	if err != nil || paused.LifecycleState != "paused" {
		t.Fatalf("PauseRoute failed: %+v err=%v", paused, err)
	}
	resumed, err := svc.ResumeRoute(ctx, rt.ID, "test")
	if err != nil || resumed.LifecycleState != "active" {
		t.Fatalf("ResumeRoute failed: %+v err=%v", resumed, err)
	}
	if err := svc.DeleteRoute(ctx, rt.ID, true, "test"); err != nil {
		t.Fatalf("DeleteRoute failed: %v", err)
	}
	if len(cfRecords) != 0 || len(caddyRoutes) != 0 {
		t.Fatalf("expected DNS and Caddy clean after DeleteRoute, got cf=%d caddy=%d", len(cfRecords), len(caddyRoutes))
	}

	// 11. Verify Self-Proxy (`--target self`) security guard & end-to-end self-proxy!
	if _, selfErr := svc.Publish(ctx, orchestrator.PublishInput{Target: "self"}, "test"); selfErr == nil {
		t.Fatal("expected self-publish to be rejected when admin password is not initialized")
	}
	adminHash, _ := crypto.HashPassword("VantuSelf#2026")
	if err := st.CreateOrUpdateAdmin(ctx, "admin", adminHash); err != nil {
		t.Fatalf("CreateOrUpdateAdmin failed: %v", err)
	}
	selfPub, err := svc.Publish(ctx, orchestrator.PublishInput{Target: "self"}, "test")
	if err != nil {
		t.Fatalf("self Publish failed after admin init: %v", err)
	}
	if selfPub.Route.FullDomain != "vantu.xiaofantu.com" || selfPub.Route.SourceType != "self" || selfPub.Route.Preset != "sse" {
		t.Fatalf("unexpected self route metadata: %+v", selfPub.Route)
	}
	selfVisitorTr := &http.Transport{
		DialContext: func(dCtx context.Context, network, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(dCtx, network, edgeHTTPS.Listener.Addr().String())
		},
		TLSClientConfig: &tls.Config{RootCAs: rootPool, ServerName: "vantu.xiaofantu.com"},
	}
	selfVisitorCli := &http.Client{Transport: selfVisitorTr, Timeout: 5 * time.Second}
	hzResp, err := selfVisitorCli.Get("https://vantu.xiaofantu.com/api/v1/healthz")
	if err != nil || hzResp.StatusCode != 200 {
		t.Fatalf("self-proxied healthz failed: resp=%v err=%v", hzResp, err)
	}
	_ = hzResp.Body.Close()
	if err := svc.DeleteRoute(ctx, selfPub.Route.ID, true, "test"); err != nil {
		t.Fatalf("DeleteRoute self failed: %v", err)
	}
}

func generateTestOriginCert(t *testing.T, dnsWildcard string, validFor time.Duration) (*x509.CertPool, *tls.Config) {
	t.Helper()
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	caTpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{Organization: []string{"Let's Encrypt"}, CommonName: "Let's Encrypt E6"},
		NotBefore:             time.Now().Add(-1 * time.Hour),
		NotAfter:              time.Now().Add(365 * 24 * time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTpl, caTpl, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	caCert, _ := x509.ParseCertificate(caDER)

	leafKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	leafTpl := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: dnsWildcard},
		DNSNames:     []string{dnsWildcard, "xiaofantu.com"},
		NotBefore:    time.Now().Add(-10 * time.Minute),
		NotAfter:     time.Now().Add(validFor),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	leafDER, err := x509.CreateCertificate(rand.Reader, leafTpl, caCert, &leafKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}

	pool := x509.NewCertPool()
	pool.AddCert(caCert)

	tlsCert := tls.Certificate{
		Certificate: [][]byte{leafDER, caDER},
		PrivateKey:  leafKey,
	}
	return pool, &tls.Config{Certificates: []tls.Certificate{tlsCert}}
}
