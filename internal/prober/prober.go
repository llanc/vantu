package prober

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"math"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/llanc/vantu/internal/caddy"
	"github.com/llanc/vantu/internal/crypto"
	"github.com/llanc/vantu/internal/discovery"
	"github.com/llanc/vantu/internal/events"
	"github.com/llanc/vantu/internal/store"
	"github.com/llanc/vantu/internal/tailscale"
)

// Options configures optional overrides (primarily used for deterministic E2E testing without root :443).
type Options struct {
	Interval           time.Duration
	OriginPortOverride int
	PublicPortOverride int
	RootCAs            *x509.CertPool
	PublicDialContext  func(ctx context.Context, network, addr string) (net.Conn, error)
}

type nodeSnapshot struct {
	node         store.EdgeNode
	secret       string
	caddyOK      bool
	caddyVersion string
	ping         tailscale.PeerPingResult
}

// Prober executes periodic and on-demand route health & certificate inspections.
type Prober struct {
	store       *store.Store
	vault       *crypto.Vault
	docker      *discovery.DockerWatcher
	tsInspector *tailscale.Inspector
	hub         *events.Hub
	opts        Options
}

func New(
	st *store.Store,
	vault *crypto.Vault,
	docker *discovery.DockerWatcher,
	ts *tailscale.Inspector,
	hub *events.Hub,
	opts Options,
) *Prober {
	if opts.Interval <= 0 {
		opts.Interval = 60 * time.Second
	}
	return &Prober{
		store:       st,
		vault:       vault,
		docker:      docker,
		tsInspector: ts,
		hub:         hub,
		opts:        opts,
	}
}

// Start runs background periodic probing until ctx is cancelled.
func (p *Prober) Start(ctx context.Context) {
	ticker := time.NewTicker(p.opts.Interval)
	defer ticker.Stop()

	// Run an initial warm-up pass shortly after daemon boot.
	initTimer := time.NewTimer(3 * time.Second)
	defer initTimer.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-initTimer.C:
			_, _ = p.ProbeAll(ctx)
		case <-ticker.C:
			_, _ = p.ProbeAll(ctx)
		}
	}
}

// ProbeNode inspects Tailscale tunnel health, Caddy Admin API status, and route drift on a single Edge Node.
func (p *Prober) ProbeNode(ctx context.Context, nodeIDOrName string) (*store.EdgeNode, error) {
	node, err := p.store.GetEdgeNode(ctx, nodeIDOrName)
	if err != nil {
		return nil, err
	}

	snap := p.inspectNode(ctx, *node)
	health := snap.node.HealthStatus

	if snap.caddyOK {
		// Check if any enabled routes bound to this node are missing from Caddy memory.
		routes, _ := p.store.ListRoutes(ctx)
		cli := caddy.NewClient(node.CaddyAdminURL, snap.secret)
		for _, r := range routes {
			if r.EdgeNodeID == node.ID && r.Enabled {
				if has, err := cli.HasRoute(ctx, r.ID); err == nil && !has {
					health = "drift"
					break
				}
			}
		}
	}

	if err := p.store.UpdateEdgeNodeHealth(
		ctx, node.ID, health, snap.ping.ConnType, snap.ping.DERPRegion, snap.ping.RTTMs, snap.caddyVersion,
	); err != nil {
		return nil, err
	}

	updated, err := p.store.GetEdgeNode(ctx, node.ID)
	if err == nil && p.hub != nil {
		p.hub.Publish("nodes_updated", map[string]any{"node_id": node.ID, "health_status": health})
	}
	return updated, err
}

// ProbeRoute inspects local reachability, edge Caddy sync, origin TLS certificate, and public HTTPS response for a route.
func (p *Prober) ProbeRoute(ctx context.Context, routeIDOrDomain string) (*store.Route, error) {
	rt, err := p.store.GetRoute(ctx, routeIDOrDomain)
	if err != nil {
		return nil, err
	}
	node, err := p.store.GetEdgeNode(ctx, rt.EdgeNodeID)
	if err != nil {
		return nil, err
	}

	snap := p.inspectNode(ctx, *node)
	_ = p.store.UpdateEdgeNodeHealth(
		ctx, node.ID, snap.node.HealthStatus, snap.ping.ConnType, snap.ping.DERPRegion, snap.ping.RTTMs, snap.caddyVersion,
	)

	updatedRoute, sample := p.evaluateRoute(ctx, *rt, snap)
	if err := p.store.SaveProbeBatch(ctx, []store.Route{updatedRoute}, []store.ProbeSample{sample}); err != nil {
		return nil, err
	}

	if updatedRoute.LifecycleState == "drift" || updatedRoute.LifecycleState == "config_drift" {
		_ = p.store.UpdateEdgeNodeHealth(ctx, node.ID, "drift", snap.ping.ConnType, snap.ping.DERPRegion, snap.ping.RTTMs, snap.caddyVersion)
	}

	fresh, err := p.store.GetRoute(ctx, rt.ID)
	if err == nil && p.hub != nil {
		p.hub.Publish("probe_completed", map[string]any{"route": fresh, "sample": sample})
	}
	return fresh, err
}

// ProbeAll inspects all registered Edge Nodes and Routes in a bounded concurrent batch.
func (p *Prober) ProbeAll(ctx context.Context) ([]store.Route, error) {
	nodes, err := p.store.ListEdgeNodes(ctx)
	if err != nil {
		return nil, err
	}

	nodeMap := make(map[string]nodeSnapshot, len(nodes))
	for _, n := range nodes {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		snap := p.inspectNode(ctx, n)
		nodeMap[n.ID] = snap
		_ = p.store.UpdateEdgeNodeHealth(
			ctx, n.ID, snap.node.HealthStatus, snap.ping.ConnType, snap.ping.DERPRegion, snap.ping.RTTMs, snap.caddyVersion,
		)
	}

	routes, err := p.store.ListRoutes(ctx)
	if err != nil {
		return nil, err
	}
	if len(routes) == 0 {
		return routes, nil
	}

	updates := make([]store.Route, len(routes))
	samples := make([]store.ProbeSample, len(routes))
	driftNodes := make(map[string]bool)
	var driftMu sync.Mutex

	sem := make(chan struct{}, 6)
	var wg sync.WaitGroup
	for i, rt := range routes {
		snap, ok := nodeMap[rt.EdgeNodeID]
		if !ok {
			continue
		}
		wg.Add(1)
		go func(idx int, route store.Route, ns nodeSnapshot) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				return
			}
			defer func() { <-sem }()

			upd, sm := p.evaluateRoute(ctx, route, ns)
			updates[idx] = upd
			samples[idx] = sm
			if upd.Enabled && !upd.CaddySynced && ns.caddyOK {
				driftMu.Lock()
				driftNodes[ns.node.ID] = true
				driftMu.Unlock()
			}
		}(i, rt, snap)
	}
	wg.Wait()

	if ctx.Err() != nil {
		return nil, ctx.Err()
	}

	if err := p.store.SaveProbeBatch(ctx, updates, samples); err != nil {
		return nil, err
	}

	for nid := range driftNodes {
		if ns, ok := nodeMap[nid]; ok {
			_ = p.store.UpdateEdgeNodeHealth(ctx, nid, "drift", ns.ping.ConnType, ns.ping.DERPRegion, ns.ping.RTTMs, ns.caddyVersion)
		}
	}

	freshRoutes, err := p.store.ListRoutes(ctx)
	if err == nil && p.hub != nil {
		p.hub.Publish("routes_updated", map[string]any{"count": len(freshRoutes)})
	}
	return freshRoutes, err
}

func (p *Prober) inspectNode(ctx context.Context, n store.EdgeNode) nodeSnapshot {
	adminHostPort := extractHostPort(n.CaddyAdminURL, n.TailscaleIP, 2019)
	var ping tailscale.PeerPingResult
	if p.tsInspector != nil {
		ping = p.tsInspector.ProbePeer(ctx, n.TailscaleIP, adminHostPort)
	} else {
		ping = tcpPingFallback(ctx, adminHostPort)
	}

	secret, _ := p.vault.Decrypt(n.AuthSecretEnc)
	cli := caddy.NewClient(n.CaddyAdminURL, secret)
	ver, caddyErr := cli.Ping(ctx)
	caddyOK := caddyErr == nil

	health := "online"
	switch {
	case !ping.Reachable && !caddyOK:
		health = "offline"
	case !caddyOK:
		health = "degraded"
	case ping.ConnType == "derp":
		health = "degraded"
	}
	if caddyOK && !ping.Reachable {
		ping.Reachable = true
		ping.ConnType = "direct"
		if ping.RTTMs < 0 {
			ping.RTTMs = 1
		}
	}

	n.HealthStatus = health
	return nodeSnapshot{
		node:         n,
		secret:       secret,
		caddyOK:      caddyOK,
		caddyVersion: ver,
		ping:         ping,
	}
}

func (p *Prober) evaluateRoute(ctx context.Context, rt store.Route, ns nodeSnapshot) (store.Route, store.ProbeSample) {
	prevState := rt.LifecycleState

	// Hop 1: Local container / service TCP health.
	host, port := rt.LocalTargetHost, rt.LocalTargetPort
	if rt.SourceType == "docker" && rt.ContainerName != "" && p.docker != nil && p.docker.Available() {
		host, port = p.docker.ResolveContainerTarget(ctx, rt.ContainerName, host, port)
	}
	rt.LocalHealthy = probeLocalTCP(ctx, host, port)

	// If route is paused by user, skip active Caddy/TLS fail judgements.
	if !rt.Enabled {
		rt.CaddySynced = false
		rt.LifecycleState = "paused"
		rt.LastError = ""
		return rt, store.ProbeSample{
			RouteID:         rt.ID,
			IsOK:            true,
			TSRTTMs:         ns.ping.RTTMs,
			PublicLatencyMs: -1,
			PublicHTTPCode:  0,
			FailStage:       "paused",
		}
	}

	// Hop 3: Edge Caddy @id sync check.
	if ns.caddyOK {
		cli := caddy.NewClient(ns.node.CaddyAdminURL, ns.secret)
		has, err := cli.HasRoute(ctx, rt.ID)
		rt.CaddySynced = err == nil && has
	} else {
		rt.CaddySynced = false
	}

	// Hop 4 (Track B): Direct TCP + TLS SNI dial to Edge Node public IP:443 (penetrates Cloudflare Orange Cloud).
	originPort := 443
	if p.opts.OriginPortOverride > 0 {
		originPort = p.opts.OriginPortOverride
	}
	originOK, originIssuer, originExp, originDays, originHTTPCode, originErr := p.probeOriginTLSAndHTTP(
		ctx, ns.node.PublicIPv4, originPort, rt.FullDomain,
	)
	rt.OriginTLSOK = originOK
	if originIssuer != "" {
		rt.OriginCertIssuer = originIssuer
	}
	if originExp != nil {
		rt.OriginCertExpires = originExp
		rt.OriginCertDays = originDays
	} else if !originOK {
		rt.OriginCertDays = -1
	}

	// Hop 5 (Track A): Public E2E HTTPS visitor probe (https://<FullDomain>).
	pubCode, pubLatency, pubIssuer, pubErr := p.probePublicHTTPS(ctx, rt.FullDomain)
	if pubCode > 0 {
		rt.PublicHTTPCode = pubCode
		rt.PublicLatencyMs = pubLatency
	} else if originHTTPCode > 0 {
		// If public DNS is still propagating right after publish, surface origin HTTP status as fallback indicator.
		rt.PublicHTTPCode = originHTTPCode
		rt.PublicLatencyMs = pubLatency
	} else {
		rt.PublicHTTPCode = 0
		rt.PublicLatencyMs = -1
	}
	if pubIssuer != "" {
		rt.PublicCertIssuer = pubIssuer
	} else if !rt.DNSProxied && originIssuer != "" {
		rt.PublicCertIssuer = originIssuer
	}

	// Synthesize deterministic LifecycleState & FailStage.
	newlyCreated := time.Since(rt.CreatedAt) < 5*time.Minute
	failStage := ""
	isOK := false

	switch {
	case !ns.caddyOK:
		rt.LifecycleState = "degraded"
		failStage = "edge_caddy"
		rt.LastError = fmt.Sprintf("边缘节点 %s Caddy Admin API 不可达，请检查 Tailscale 隧道与容器状态", ns.node.Name)
	case !rt.CaddySynced:
		rt.LifecycleState = "config_drift"
		failStage = "caddy_drift"
		rt.LastError = fmt.Sprintf("边缘节点 %s 内存缺少该路由配置（可能重启未挂载持久化），点击一键对账修复即可恢复", ns.node.Name)
	case !rt.LocalHealthy:
		rt.LifecycleState = "degraded"
		failStage = "local_upstream"
		rt.LastError = fmt.Sprintf("本地目标服务 %s:%d 连接失败，请检查容器或内网进程是否运行", host, port)
	case !rt.OriginTLSOK:
		failStage = "origin_tls"
		if newlyCreated {
			rt.LifecycleState = "issuing_tls"
			rt.LastError = "边缘 Caddy 正在向 Let's Encrypt 申请源站证书，通常 10~40 秒自动就绪"
		} else {
			rt.LifecycleState = "degraded"
			rt.LastError = fmt.Sprintf("边缘源站 :443 证书异常：%s", originErr)
		}
	case pubCode >= 500 || (pubCode == 0 && originHTTPCode >= 500):
		rt.LifecycleState = "degraded"
		failStage = "upstream_5xx"
		code := pubCode
		if code == 0 {
			code = originHTTPCode
		}
		rt.LastError = fmt.Sprintf("公网网关返回 HTTP %d，请检查本地反代协议预设或转发模式", code)
	case pubCode == 0 && originHTTPCode > 0 && originHTTPCode < 500:
		if newlyCreated {
			rt.LifecycleState = "issuing_tls"
			failStage = "dns_propagating"
			rt.LastError = "边缘源站与证书已就绪，等待公网 DNS 全球缓存刷新中"
			isOK = true
		} else {
			rt.LifecycleState = "degraded"
			failStage = "public_e2e"
			rt.LastError = fmt.Sprintf("边缘源站正常，但公网域名访问失败：%s", pubErr)
		}
	case pubCode == 0:
		failStage = "public_e2e"
		if newlyCreated {
			rt.LifecycleState = "issuing_tls"
			rt.LastError = "正在等待公网 DNS 与 TLS 链路生效..."
		} else {
			rt.LifecycleState = "degraded"
			rt.LastError = fmt.Sprintf("公网 HTTPS 访问失败：%s", pubErr)
		}
	default:
		rt.LifecycleState = "active"
		rt.LastError = ""
		isOK = true
	}

	// Emit audit log when certificate is nearing expiry (<14 days) or route transitions into drift/degraded.
	if rt.OriginTLSOK && rt.OriginCertDays >= 0 && rt.OriginCertDays <= 14 && prevState == "active" {
		p.store.AppendAudit(ctx, store.AuditEvent{
			Severity:    "warn",
			Category:    "cert",
			TargetID:    rt.ID,
			TargetLabel: rt.FullDomain,
			Message:     fmt.Sprintf("源站真实 TLS 证书仅剩 %d 天到期 (%s)", rt.OriginCertDays, rt.OriginCertIssuer),
			Actor:       "prober",
		})
	}
	if prevState == "active" && (rt.LifecycleState == "degraded" || rt.LifecycleState == "config_drift") {
		p.store.AppendAudit(ctx, store.AuditEvent{
			Severity:    "warn",
			Category:    "route",
			TargetID:    rt.ID,
			TargetLabel: rt.FullDomain,
			Message:     rt.LastError,
			Actor:       "prober",
		})
	}

	return rt, store.ProbeSample{
		RouteID:         rt.ID,
		IsOK:            isOK,
		TSRTTMs:         ns.ping.RTTMs,
		PublicLatencyMs: rt.PublicLatencyMs,
		PublicHTTPCode:  rt.PublicHTTPCode,
		FailStage:       failStage,
	}
}

func (p *Prober) probeOriginTLSAndHTTP(
	ctx context.Context,
	edgePublicIP string,
	port int,
	fullDomain string,
) (tlsOK bool, issuer string, expires *time.Time, daysLeft int, httpCode int, errMsg string) {
	daysLeft = -1
	if strings.TrimSpace(edgePublicIP) == "" {
		return false, "", nil, -1, 0, "节点未配置公网 IP"
	}
	addr := net.JoinHostPort(strings.TrimSpace(edgePublicIP), strconv.Itoa(port))
	dialer := &net.Dialer{Timeout: 4 * time.Second}

	rawConn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return false, "", nil, -1, 0, fmt.Sprintf("TCP 连接 %s 失败 (%v)", addr, err)
	}

	tlsCfg := &tls.Config{
		ServerName: fullDomain,
		RootCAs:    p.opts.RootCAs,
		NextProtos: []string{"http/1.1"},
	}
	tlsConn := tls.Client(rawConn, tlsCfg)
	_ = tlsConn.SetDeadline(time.Now().Add(5 * time.Second))
	if err := tlsConn.HandshakeContext(ctx); err != nil {
		_ = tlsConn.Close()
		// Inspect untrusted/staging certificate metadata if possible without marking OriginTLSOK=true.
		insIssuer, insExp, insDays := inspectUntrustedCert(ctx, addr, fullDomain)
		return false, insIssuer, insExp, insDays, 0, err.Error()
	}
	defer tlsConn.Close()

	state := tlsConn.ConnectionState()
	if len(state.PeerCertificates) > 0 {
		leaf := state.PeerCertificates[0]
		issuer = formatCertIssuer(leaf)
		exp := leaf.NotAfter.UTC()
		expires = &exp
		daysLeft = int(math.Floor(time.Until(exp).Hours() / 24))
		if daysLeft < 0 {
			return false, issuer, expires, daysLeft, 0, "源站 TLS 证书已过期"
		}
	}

	// Send lightweight HTTP/1.1 HEAD/GET over the already-established origin TLS connection to verify upstream responsiveness.
	tr := &http.Transport{
		DialTLSContext: func(reqCtx context.Context, _, _ string) (net.Conn, error) {
			c, dErr := dialer.DialContext(reqCtx, "tcp", addr)
			if dErr != nil {
				return nil, dErr
			}
			tc := tls.Client(c, tlsCfg.Clone())
			if hErr := tc.HandshakeContext(reqCtx); hErr != nil {
				_ = tc.Close()
				return nil, hErr
			}
			return tc, nil
		},
	}
	defer tr.CloseIdleConnections()

	hc := &http.Client{
		Transport: tr,
		Timeout:   5 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://"+fullDomain+"/", nil)
	if err == nil {
		req.Header.Set("User-Agent", "Vantu-Prober/0.1")
		if resp, rErr := hc.Do(req); rErr == nil {
			httpCode = resp.StatusCode
			_ = resp.Body.Close()
		}
	}

	return true, issuer, expires, daysLeft, httpCode, ""
}

func (p *Prober) probePublicHTTPS(ctx context.Context, fullDomain string) (httpCode int, latencyMs int, issuer string, errMsg string) {
	dialFn := p.opts.PublicDialContext
	if dialFn == nil {
		d := &net.Dialer{Timeout: 5 * time.Second}
		dialFn = d.DialContext
	}

	targetURL := "https://" + fullDomain + "/"
	if p.opts.PublicPortOverride > 0 {
		targetURL = fmt.Sprintf("https://%s:%d/", fullDomain, p.opts.PublicPortOverride)
	}

	tr := &http.Transport{
		DialContext:       dialFn,
		ForceAttemptHTTP2: true,
		TLSClientConfig: &tls.Config{
			ServerName: fullDomain,
			RootCAs:    p.opts.RootCAs,
		},
	}
	defer tr.CloseIdleConnections()

	hc := &http.Client{
		Transport: tr,
		Timeout:   7 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	start := time.Now()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, targetURL, nil)
	if err != nil {
		return 0, -1, "", err.Error()
	}
	req.Header.Set("User-Agent", "Vantu-Prober/0.1")

	resp, err := hc.Do(req)
	if err != nil {
		return 0, -1, "", err.Error()
	}
	defer resp.Body.Close()

	ms := int(time.Since(start).Milliseconds())
	if ms < 1 {
		ms = 1
	}
	if resp.TLS != nil && len(resp.TLS.PeerCertificates) > 0 {
		issuer = formatCertIssuer(resp.TLS.PeerCertificates[0])
	}
	return resp.StatusCode, ms, issuer, ""
}

func inspectUntrustedCert(ctx context.Context, addr, serverName string) (string, *time.Time, int) {
	d := &net.Dialer{Timeout: 3 * time.Second}
	c, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return "", nil, -1
	}
	tc := tls.Client(c, &tls.Config{
		ServerName:         serverName,
		InsecureSkipVerify: true, //nolint:gosec // Diagnostic metadata read-only; caller still sets OriginTLSOK=false.
	})
	defer tc.Close()
	_ = tc.SetDeadline(time.Now().Add(3 * time.Second))
	if err := tc.HandshakeContext(ctx); err != nil {
		return "", nil, -1
	}
	st := tc.ConnectionState()
	if len(st.PeerCertificates) == 0 {
		return "", nil, -1
	}
	leaf := st.PeerCertificates[0]
	exp := leaf.NotAfter.UTC()
	days := int(math.Floor(time.Until(exp).Hours() / 24))
	return formatCertIssuer(leaf) + " (Untrusted)", &exp, days
}

func formatCertIssuer(cert *x509.Certificate) string {
	if len(cert.Issuer.Organization) > 0 && cert.Issuer.Organization[0] != "" {
		org := cert.Issuer.Organization[0]
		if cert.Issuer.CommonName != "" && !strings.Contains(cert.Issuer.CommonName, org) {
			return org + " " + cert.Issuer.CommonName
		}
		return org
	}
	if cert.Issuer.CommonName != "" {
		return cert.Issuer.CommonName
	}
	return "TLS CA"
}

func probeLocalTCP(ctx context.Context, host string, port int) bool {
	if host == "" || port <= 0 {
		return false
	}
	d := net.Dialer{Timeout: 2500 * time.Millisecond}
	conn, err := d.DialContext(ctx, "tcp", net.JoinHostPort(host, strconv.Itoa(port)))
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

func extractHostPort(rawURL, fallbackHost string, fallbackPort int) string {
	if u, err := url.Parse(rawURL); err == nil && u.Host != "" {
		if _, _, err := net.SplitHostPort(u.Host); err == nil {
			return u.Host
		}
		return net.JoinHostPort(u.Host, strconv.Itoa(fallbackPort))
	}
	if fallbackHost != "" {
		return net.JoinHostPort(fallbackHost, strconv.Itoa(fallbackPort))
	}
	return ""
}

func tcpPingFallback(ctx context.Context, hostPort string) tailscale.PeerPingResult {
	if hostPort == "" {
		return tailscale.PeerPingResult{Reachable: false, ConnType: "offline", RTTMs: -1}
	}
	start := time.Now()
	d := net.Dialer{Timeout: 3 * time.Second}
	conn, err := d.DialContext(ctx, "tcp", hostPort)
	if err != nil {
		return tailscale.PeerPingResult{Reachable: false, ConnType: "offline", RTTMs: -1}
	}
	_ = conn.Close()
	ms := int(time.Since(start).Milliseconds())
	if ms < 1 {
		ms = 1
	}
	return tailscale.PeerPingResult{Reachable: true, ConnType: "direct", RTTMs: ms}
}
