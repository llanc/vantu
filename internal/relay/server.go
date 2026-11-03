package relay

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/xiaofantu/vantu/internal/crypto"
	"github.com/xiaofantu/vantu/internal/discovery"
	"github.com/xiaofantu/vantu/internal/store"
	"github.com/xiaofantu/vantu/internal/tailscale"
)

const (
	HeaderRouteID  = "X-Vantu-Route"
	HeaderRelayKey = "X-Vantu-Relay-Key"
)

type cachedTarget struct {
	host      string
	port      int
	expiresAt time.Time
}

// Server implements Vantu's single-port multiplexed Local Relay gateway (<Tailscale_IP>:18080).
// It allows Edge Caddy to securely reach unexposed Docker containers (no -p or 127.0.0.1-only)
// without exposing container ports on host network interfaces.
type Server struct {
	store         *store.Store
	vault         *crypto.Vault
	docker        *discovery.DockerWatcher
	tsInspector   *tailscale.Inspector
	bindIP        string
	port          int
	stdTransport  *http.Transport
	tlsSkipTrans  *http.Transport

	mu          sync.RWMutex
	boundAddr   string
	running     bool
	httpSrv     *http.Server
	targetCache map[string]cachedTarget
}

func NewServer(
	st *store.Store,
	vault *crypto.Vault,
	docker *discovery.DockerWatcher,
	ts *tailscale.Inspector,
	bindIP string,
	port int,
) *Server {
	baseDialer := &net.Dialer{
		Timeout:   5 * time.Second,
		KeepAlive: 30 * time.Second,
	}
	stdTrans := &http.Transport{
		DialContext:           baseDialer.DialContext,
		ForceAttemptHTTP2:     false,
		MaxIdleConns:          128,
		MaxIdleConnsPerHost:   32,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   8 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	}
	insecureTrans := stdTrans.Clone()
	insecureTrans.TLSClientConfig = &tls.Config{
		InsecureSkipVerify: true, //nolint:gosec // Explicitly requested for https_insecure homelab upstreams (PVE/DSM/Portainer).
	}

	return &Server{
		store:        st,
		vault:        vault,
		docker:       docker,
		tsInspector:  ts,
		bindIP:       strings.TrimSpace(bindIP),
		port:         port,
		stdTransport: stdTrans,
		tlsSkipTrans: insecureTrans,
		targetCache:  make(map[string]cachedTarget),
	}
}

// Start binds the relay listener and serves until ctx is cancelled.
func (s *Server) Start(ctx context.Context) error {
	ln, actualAddr, err := s.listen(ctx)
	if err != nil {
		return err
	}

	srv := &http.Server{
		Handler:           http.HandlerFunc(s.serveHTTP),
		ReadHeaderTimeout: 15 * time.Second,
	}

	s.mu.Lock()
	s.boundAddr = actualAddr
	s.running = true
	s.httpSrv = srv
	s.mu.Unlock()

	go func() {
		<-ctx.Done()
		shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutCtx)
		s.mu.Lock()
		s.running = false
		s.mu.Unlock()
	}()

	err = srv.Serve(ln)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func (s *Server) listen(ctx context.Context) (net.Listener, string, error) {
	candidates := make([]string, 0, 3)
	if s.bindIP != "" {
		candidates = append(candidates, net.JoinHostPort(s.bindIP, strconv.Itoa(s.port)))
	} else {
		if s.tsInspector != nil {
			self := s.tsInspector.SelfStatus(ctx)
			if self.IPv4 != "" {
				candidates = append(candidates, net.JoinHostPort(self.IPv4, strconv.Itoa(s.port)))
			}
		}
		// Safe loopback fallback when Tailscale interface is not present on the local host (e.g. dev/CI or container bridge).
		candidates = append(candidates, net.JoinHostPort("127.0.0.1", strconv.Itoa(s.port)))
	}

	var lastErr error
	for _, addr := range candidates {
		ln, err := net.Listen("tcp", addr)
		if err == nil {
			return ln, ln.Addr().String(), nil
		}
		lastErr = err
	}
	return nil, "", fmt.Errorf("bind local relay failed: %w", lastErr)
}

func (s *Server) Running() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.running
}

func (s *Server) BoundAddr() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.boundAddr
}

func (s *Server) BoundPort() int {
	addr := s.BoundAddr()
	if addr == "" {
		return s.port
	}
	_, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		return s.port
	}
	if p, err := strconv.Atoi(portStr); err == nil && p > 0 {
		return p
	}
	return s.port
}

func (s *Server) serveHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/_vantu/relay/healthz" && r.Header.Get(HeaderRouteID) == "" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true,"service":"vantu-local-relay"}`))
		return
	}

	routeID := strings.TrimSpace(r.Header.Get(HeaderRouteID))
	relayKey := strings.TrimSpace(r.Header.Get(HeaderRelayKey))
	if routeID == "" || relayKey == "" || !s.vault.VerifyRelayKey(routeID, relayKey) {
		http.Error(w, "forbidden: invalid vantu relay signature", http.StatusForbidden)
		return
	}

	rt, err := s.store.GetRoute(r.Context(), routeID)
	if err != nil || rt == nil {
		http.Error(w, "not found: unknown vantu route", http.StatusNotFound)
		return
	}
	if !rt.Enabled {
		http.Error(w, "service unavailable: route is paused", http.StatusServiceUnavailable)
		return
	}
	if rt.ForwardMode != "relay" {
		http.Error(w, "bad gateway: route is not configured for relay mode", http.StatusBadGateway)
		return
	}

	host, port := s.resolveUpstreamTarget(r.Context(), rt)
	scheme := "http"
	if rt.UpstreamScheme == "https" || rt.Preset == "https_insecure" {
		scheme = "https"
	}

	targetURL := &url.URL{
		Scheme: scheme,
		Host:   net.JoinHostPort(host, strconv.Itoa(port)),
	}

	proxy := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(targetURL)
			pr.Out.Host = pr.In.Host
			// Strip internal Vantu authentication headers so upstream never sees them.
			pr.Out.Header.Del(HeaderRouteID)
			pr.Out.Header.Del(HeaderRelayKey)
			pr.SetXForwarded()
		},
		Transport: s.selectTransport(rt),
		ErrorHandler: func(rw http.ResponseWriter, req *http.Request, proxyErr error) {
			if !errors.Is(proxyErr, context.Canceled) {
				log.Printf("[vantu-relay] upstream error for %s (%s): %v", rt.FullDomain, targetURL.String(), proxyErr)
			}
			http.Error(rw, "vantu relay: local upstream unreachable", http.StatusBadGateway)
		},
	}

	if rt.Preset == "sse" {
		proxy.FlushInterval = -1
	}

	proxy.ServeHTTP(w, r)
}

func (s *Server) selectTransport(rt *store.Route) http.RoundTripper {
	if rt.Preset == "https_insecure" || rt.UpstreamScheme == "https" {
		return s.tlsSkipTrans
	}
	return s.stdTransport
}

func (s *Server) resolveUpstreamTarget(ctx context.Context, rt *store.Route) (string, int) {
	if rt.SourceType != "docker" || rt.ContainerName == "" || s.docker == nil || !s.docker.Available() {
		return rt.LocalTargetHost, rt.LocalTargetPort
	}

	now := time.Now()
	s.mu.RLock()
	if c, ok := s.targetCache[rt.ContainerName]; ok && now.Before(c.expiresAt) {
		s.mu.RUnlock()
		return c.host, c.port
	}
	s.mu.RUnlock()

	host, port := s.docker.ResolveContainerTarget(ctx, rt.ContainerName, rt.LocalTargetHost, rt.LocalTargetPort)
	s.mu.Lock()
	s.targetCache[rt.ContainerName] = cachedTarget{
		host:      host,
		port:      port,
		expiresAt: now.Add(3 * time.Second),
	}
	s.mu.Unlock()
	return host, port
}
