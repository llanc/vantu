package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/llanc/vantu/internal/config"
	"github.com/llanc/vantu/internal/crypto"
	"github.com/llanc/vantu/internal/i18n"
	"github.com/llanc/vantu/internal/orchestrator"
	"github.com/llanc/vantu/internal/skilldoc"
	"github.com/llanc/vantu/internal/store"
	"github.com/llanc/vantu/internal/ulid"
	"github.com/llanc/vantu/internal/update"
	"github.com/llanc/vantu/web"
)

type ctxKey string

const (
	ctxKeyUnixSocket ctxKey = "unix_socket"
	ctxKeyActor      ctxKey = "actor"
	ctxKeyScope      ctxKey = "scope"
	sessionCookie    string = "vantu_session"
)

type Envelope struct {
	OK    bool                   `json:"ok"`
	Data  any                    `json:"data,omitempty"`
	Error *orchestrator.AppError `json:"error,omitempty"`
	Meta  map[string]any         `json:"meta,omitempty"`
}

type loginAttempt struct {
	failures []time.Time
}

// Server exposes the HTTP REST/SSE API, embedded Vue 3 SPA, and optional local Unix Domain Socket for zero-config CLI access.
type Server struct {
	cfg      config.Config
	svc      *orchestrator.Service
	router   chi.Router
	limitMu  sync.Mutex
	attempts map[string]*loginAttempt
	updater  *update.Manager
	restart  func()
}

// EnableUpdates turns on version checking; restart is invoked after a successful self-update.
func (s *Server) EnableUpdates(m *update.Manager, restart func()) {
	s.updater = m
	s.restart = restart
}

func NewServer(cfg config.Config, svc *orchestrator.Service) *Server {
	s := &Server{
		cfg:      cfg,
		svc:      svc,
		attempts: make(map[string]*loginAttempt),
	}
	s.router = s.buildRouter()
	return s
}

func (s *Server) Handler() http.Handler {
	return s.router
}

// Start runs both the TCP HTTP listener (for Web UI & remote API) and the 0600 Unix Socket listener (for local CLI).
func (s *Server) Start(ctx context.Context) error {
	tcpLn, err := net.Listen("tcp", s.cfg.ListenAddr)
	if err != nil {
		return fmt.Errorf("listen %s: %w", s.cfg.ListenAddr, err)
	}

	httpSrv := &http.Server{
		Handler:           s.router,
		ReadHeaderTimeout: 15 * time.Second,
	}

	var unixSrv *http.Server
	if strings.TrimSpace(s.cfg.SocketPath) != "" {
		if err := os.MkdirAll(filepath.Dir(s.cfg.SocketPath), 0o755); err == nil {
			_ = os.Remove(s.cfg.SocketPath)
			if uLn, uErr := net.Listen("unix", s.cfg.SocketPath); uErr == nil {
				_ = os.Chmod(s.cfg.SocketPath, 0o600)
				unixSrv = &http.Server{
					Handler:           s.router,
					ReadHeaderTimeout: 15 * time.Second,
					ConnContext: func(baseCtx context.Context, _ net.Conn) context.Context {
						return context.WithValue(baseCtx, ctxKeyUnixSocket, true)
					},
				}
				go func() {
					if srvErr := unixSrv.Serve(uLn); srvErr != nil && !errors.Is(srvErr, http.ErrServerClosed) {
						log.Printf("[vantu-api] unix socket server error: %v", srvErr)
					}
				}()
			}
		}
	}

	go func() {
		<-ctx.Done()
		shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = httpSrv.Shutdown(shutCtx)
		if unixSrv != nil {
			_ = unixSrv.Shutdown(shutCtx)
			_ = os.Remove(s.cfg.SocketPath)
		}
	}()

	err = httpSrv.Serve(tcpLn)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func (s *Server) buildRouter() chi.Router {
	r := chi.NewRouter()
	r.Use(middleware.RealIP)
	r.Use(middleware.Recoverer)

	r.Route("/api/v1", func(api chi.Router) {
		api.Use(localizeMiddleware)

		// Unauthenticated endpoints
		api.Get("/healthz", s.handleHealthz)
		api.Get("/auth/status", s.handleAuthStatus)
		api.Post("/auth/setup", s.handleAuthSetup)
		api.Post("/auth/login", s.handleAuthLogin)

		// Protected endpoints (Unix Socket | Session Cookie | Bearer Token)
		api.Group(func(priv chi.Router) {
			priv.Use(s.authMiddleware)

			priv.Post("/auth/logout", s.handleAuthLogout)
			priv.Post("/auth/password", s.handleAuthPassword)
			priv.Post("/auth/admin", s.handleAdminSetPassword)

			priv.Get("/capabilities", s.handleCapabilities)
			priv.Get("/dashboard", s.handleDashboard)
			priv.Get("/doctor", s.handleDoctor)
			priv.Get("/skill", s.handleSkill)
			priv.Get("/update", s.handleUpdateInfo)
			priv.Post("/update/apply", s.handleUpdateApply)

			priv.Get("/tokens", s.handleListTokens)
			priv.Post("/tokens", s.handleCreateToken)
			priv.Delete("/tokens/{id}", s.handleDeleteToken)

			priv.Get("/containers", s.handleListContainers)

			priv.Get("/dns/providers", s.handleListDNSProviders)
			priv.Post("/dns/providers", s.handleCreateDNSProvider)
			priv.Post("/dns/providers/{id}/sync", s.handleSyncDNSProvider)
			priv.Delete("/dns/providers/{id}", s.handleDeleteDNSProvider)
			priv.Get("/dns/zones", s.handleListDNSZones)
			priv.Put("/dns/zones/{id}/default", s.handleSetDefaultZone)

			priv.Get("/nodes", s.handleListNodes)
			priv.Post("/nodes/bootstrap", s.handlePreviewBootstrap)
			priv.Post("/nodes", s.handleCreateNode)
			priv.Put("/nodes/{id}/default", s.handleSetDefaultNode)
			priv.Post("/nodes/{id}/ping", s.handlePingNode)
			priv.Post("/nodes/{id}/reconcile", s.handleReconcileNode)
			priv.Delete("/nodes/{id}", s.handleDeleteNode)

			priv.Get("/routes", s.handleListRoutes)
			priv.Post("/routes", s.handlePublishRoute)
			priv.Get("/routes/{id}", s.handleGetRoute)
			priv.Put("/routes/{id}", s.handleUpdateRoute)
			priv.Post("/routes/{id}/probe", s.handleProbeRoute)
			priv.Patch("/routes/{id}/proxy", s.handleToggleProxy)
			priv.Put("/routes/{id}/auth", s.handleSetRouteAuth)
			priv.Post("/routes/{id}/pause", s.handlePauseRoute)
			priv.Post("/routes/{id}/resume", s.handleResumeRoute)
			priv.Post("/routes/{id}/retry", s.handleRetryRoute)
			priv.Delete("/routes/{id}", s.handleDeleteRoute)

			priv.Get("/events", s.handleListEvents)
			priv.Get("/events/stream", s.handleEventsSSE)
		})
	})

	// Embedded Vue 3 SPA static handler
	distFS, err := web.DistFS()
	if err == nil {
		fileServer := http.FileServer(http.FS(distFS))
		r.NotFound(func(w http.ResponseWriter, req *http.Request) {
			if strings.HasPrefix(req.URL.Path, "/api/") {
				writeError(w, &orchestrator.AppError{
					Code:       "NOT_FOUND",
					Message:    "API 路径不存在: " + req.URL.Path,
					HTTPStatus: http.StatusNotFound,
				})
				return
			}
			cleanPath := strings.TrimPrefix(path.Clean(req.URL.Path), "/")
			if cleanPath != "" && cleanPath != "." {
				if f, openErr := distFS.Open(cleanPath); openErr == nil {
					_ = f.Close()
					fileServer.ServeHTTP(w, req)
					return
				}
			}
			// SPA fallback: serve index.html
			indexBytes, readErr := fs.ReadFile(distFS, "index.html")
			if readErr != nil {
				http.NotFound(w, req)
				return
			}
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(indexBytes)
		})
	}

	return r
}

// --- Localization ---

type bufferedResponse struct {
	header http.Header
	status int
	body   bytes.Buffer
}

func (b *bufferedResponse) Header() http.Header         { return b.header }
func (b *bufferedResponse) WriteHeader(code int)        { b.status = code }
func (b *bufferedResponse) Write(p []byte) (int, error) { return b.body.Write(p) }

// localizeMiddleware translates Chinese messages in JSON responses for English clients.
// SSE streams are excluded here and localized per event by handleEventsSSE.
func localizeMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		lang := i18n.FromRequest(r)
		if lang == i18n.ZH || strings.HasSuffix(r.URL.Path, "/events/stream") {
			next.ServeHTTP(w, r)
			return
		}
		buf := &bufferedResponse{header: w.Header(), status: http.StatusOK}
		next.ServeHTTP(buf, r)
		body := buf.body.Bytes()
		if strings.HasPrefix(buf.header.Get("Content-Type"), "application/json") {
			body = i18n.LocalizeJSON(lang, body)
			buf.header.Del("Content-Length")
		}
		w.WriteHeader(buf.status)
		_, _ = w.Write(body)
	})
}

// --- Authentication & Rate Limit ---

func (s *Server) authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		actor, scope, ok := s.resolveIdentity(r)
		if !ok {
			writeError(w, &orchestrator.AppError{
				Code:       "AUTH_REQUIRED",
				Message:    "请先登录管理员控制台或提供有效的 Bearer API Token",
				Suggestion: "执行 vantu login 或设置环境变量 VANTU_TOKEN",
				HTTPStatus: http.StatusUnauthorized,
			})
			return
		}

		isReadMethod := r.Method == http.MethodGet || r.Method == http.MethodHead || r.Method == http.MethodOptions
		if !isReadMethod && scope != "admin" {
			writeError(w, &orchestrator.AppError{
				Code:       "AUTH_REQUIRED",
				Message:    "当前 API Token 仅为只读权限 (readonly)，禁止执行变更操作",
				Suggestion: "请使用具备 admin 权限的 Token",
				HTTPStatus: http.StatusForbidden,
			})
			return
		}

		ctx := context.WithValue(r.Context(), ctxKeyActor, actor)
		ctx = context.WithValue(ctx, ctxKeyScope, scope)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func (s *Server) resolveIdentity(r *http.Request) (actor string, scope string, ok bool) {
	// 1. Local Unix Socket (0600 root/owner trusted local IPC)
	if isUnix, _ := r.Context().Value(ctxKeyUnixSocket).(bool); isUnix {
		return "cli:local-socket", "admin", true
	}

	// 2. Bearer API Token (Authorization header or ?token= for SSE)
	authHdr := strings.TrimSpace(r.Header.Get("Authorization"))
	rawToken := ""
	if strings.HasPrefix(strings.ToLower(authHdr), "bearer ") {
		rawToken = strings.TrimSpace(authHdr[7:])
	} else if qTok := strings.TrimSpace(r.URL.Query().Get("token")); qTok != "" {
		rawToken = qTok
	}

	if rawToken != "" {
		hash := crypto.HashTokenSHA256(rawToken)
		if tok, err := s.svc.Store().LookupAPIToken(r.Context(), hash); err == nil && tok != nil {
			return "token:" + tok.Name, tok.Scope, true
		}
		if username, valid := s.svc.Store().ValidateSession(r.Context(), hash); valid {
			return "web:" + username, "admin", true
		}
	}

	// 3. HttpOnly Session Cookie
	if c, err := r.Cookie(sessionCookie); err == nil && c.Value != "" {
		hash := crypto.HashTokenSHA256(c.Value)
		if username, valid := s.svc.Store().ValidateSession(r.Context(), hash); valid {
			return "web:" + username, "admin", true
		}
	}

	return "", "", false
}

func actorFrom(r *http.Request) string {
	if v, ok := r.Context().Value(ctxKeyActor).(string); ok && v != "" {
		return v
	}
	return "admin"
}

func (s *Server) checkLoginAllowed(ip string) bool {
	s.limitMu.Lock()
	defer s.limitMu.Unlock()
	cutoff := time.Now().Add(-5 * time.Minute)
	entry, ok := s.attempts[ip]
	if !ok {
		return true
	}
	kept := entry.failures[:0]
	for _, t := range entry.failures {
		if t.After(cutoff) {
			kept = append(kept, t)
		}
	}
	entry.failures = kept
	return len(entry.failures) < 8
}

func (s *Server) recordLoginFailure(ip string) {
	s.limitMu.Lock()
	defer s.limitMu.Unlock()
	entry, ok := s.attempts[ip]
	if !ok {
		entry = &loginAttempt{}
		s.attempts[ip] = entry
	}
	entry.failures = append(entry.failures, time.Now())
}

func (s *Server) clearLoginFailures(ip string) {
	s.limitMu.Lock()
	defer s.limitMu.Unlock()
	delete(s.attempts, ip)
}

// --- Handlers ---

func (s *Server) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	writeOK(w, map[string]any{"status": "ok", "version": config.Version})
}

func (s *Server) handleAuthStatus(w http.ResponseWriter, r *http.Request) {
	initOK, err := s.svc.Store().IsInitialized(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	actor, scope, authed := s.resolveIdentity(r)
	writeOK(w, map[string]any{
		"initialized":   initOK,
		"authenticated": authed,
		"actor":         actor,
		"scope":         scope,
		"version":       config.Version,
	})
}

func (s *Server) handleAuthSetup(w http.ResponseWriter, r *http.Request) {
	initOK, err := s.svc.Store().IsInitialized(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	if initOK {
		writeError(w, &orchestrator.AppError{
			Code:       "VALIDATION_ERROR",
			Message:    "系统已完成初始化，请直接登录",
			HTTPStatus: http.StatusConflict,
		})
		return
	}

	var body struct {
		Password string `json:"password"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeErr(w, err)
		return
	}
	if len(body.Password) < 6 {
		writeError(w, &orchestrator.AppError{
			Code:       "VALIDATION_ERROR",
			Message:    "管理员密码长度至少需要 6 位",
			HTTPStatus: http.StatusBadRequest,
		})
		return
	}

	hash, err := crypto.HashPassword(body.Password)
	if err != nil {
		writeErr(w, err)
		return
	}
	if err := s.svc.Store().SetAdminPassword(r.Context(), hash); err != nil {
		writeErr(w, err)
		return
	}
	user, err := s.svc.Store().GetAdmin(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}

	tok, err := s.issueSessionCookie(w, r, user.Username)
	if err != nil {
		writeErr(w, err)
		return
	}

	s.svc.Store().AppendAudit(r.Context(), store.AuditEvent{
		Severity:    "info",
		Category:    "auth",
		TargetLabel: "管理员",
		Message:     "已完成小凡兔控制台首次管理员初始化",
		Actor:       "setup-wizard",
	})

	writeOK(w, map[string]any{
		"session_token": tok,
	})
}

func (s *Server) handleAuthLogin(w http.ResponseWriter, r *http.Request) {
	ip := clientIP(r)
	if !s.checkLoginAllowed(ip) {
		writeError(w, &orchestrator.AppError{
			Code:       "RATE_LIMITED",
			Message:    "登录失败次数过多，请等待 5 分钟后再试",
			HTTPStatus: http.StatusTooManyRequests,
		})
		return
	}

	var body struct {
		Password string `json:"password"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeErr(w, err)
		return
	}

	user, err := s.svc.Store().GetAdmin(r.Context())
	if err != nil || !crypto.VerifyPassword(body.Password, user.PasswordHash) {
		s.recordLoginFailure(ip)
		writeError(w, &orchestrator.AppError{
			Code:       "AUTH_REQUIRED",
			Message:    "密码错误",
			HTTPStatus: http.StatusUnauthorized,
		})
		return
	}

	s.clearLoginFailures(ip)
	tok, err := s.issueSessionCookie(w, r, user.Username)
	if err != nil {
		writeErr(w, err)
		return
	}

	s.svc.Store().AppendAudit(r.Context(), store.AuditEvent{
		Severity:    "info",
		Category:    "auth",
		TargetLabel: "管理员",
		Message:     fmt.Sprintf("管理员登录成功 (%s)", ip),
		Actor:       "web:" + user.Username,
	})

	writeOK(w, map[string]any{
		"session_token": tok,
	})
}

func (s *Server) handleAuthLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookie); err == nil && c.Value != "" {
		_ = s.svc.Store().DeleteSession(r.Context(), crypto.HashTokenSHA256(c.Value))
	}
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
	writeOK(w, map[string]any{"logged_out": true})
}

func (s *Server) handleAuthPassword(w http.ResponseWriter, r *http.Request) {
	var body struct {
		CurrentPassword string `json:"current_password"`
		NewPassword     string `json:"new_password"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeErr(w, err)
		return
	}
	if len(body.NewPassword) < 6 {
		writeError(w, &orchestrator.AppError{
			Code:       "VALIDATION_ERROR",
			Message:    "新密码长度至少需要 6 位",
			HTTPStatus: http.StatusBadRequest,
		})
		return
	}

	user, err := s.svc.Store().GetAdmin(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	if !crypto.VerifyPassword(body.CurrentPassword, user.PasswordHash) {
		writeError(w, &orchestrator.AppError{
			Code:       "AUTH_REQUIRED",
			Message:    "当前密码校验不通过",
			HTTPStatus: http.StatusUnauthorized,
		})
		return
	}
	newHash, err := crypto.HashPassword(body.NewPassword)
	if err != nil {
		writeErr(w, err)
		return
	}
	if err := s.svc.Store().SetAdminPassword(r.Context(), newHash); err != nil {
		writeErr(w, err)
		return
	}
	writeOK(w, map[string]any{"updated": true})
}

func (s *Server) handleAdminSetPassword(w http.ResponseWriter, r *http.Request) {
	isUnix, _ := r.Context().Value(ctxKeyUnixSocket).(bool)
	initOK, err := s.svc.Store().IsInitialized(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	if initOK && !isUnix {
		writeError(w, &orchestrator.AppError{
			Code:       "AUTH_REQUIRED",
			Message:    "免旧密码重置仅限本机 Unix Socket CLI 执行",
			Suggestion: "请在服务器本机运行 vantu admin passwd，或在控制台设置页验证原密码修改",
			HTTPStatus: http.StatusForbidden,
		})
		return
	}

	var body struct {
		Password string `json:"password"`
		Generate bool   `json:"generate"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeErr(w, err)
		return
	}

	pwd := body.Password
	generated := ""
	if pwd == "" && body.Generate {
		sec, gErr := crypto.GenerateRandomSecret("vtu_adm_", 10)
		if gErr != nil {
			writeErr(w, gErr)
			return
		}
		pwd = sec
		generated = sec
	}
	if len(pwd) < 6 {
		writeError(w, &orchestrator.AppError{
			Code:       "VALIDATION_ERROR",
			Message:    "管理员密码长度至少需要 6 位",
			Suggestion: "请传入 --password <至少6位> 或使用 --generate 自动生成强密码",
			HTTPStatus: http.StatusBadRequest,
		})
		return
	}

	hash, err := crypto.HashPassword(pwd)
	if err != nil {
		writeErr(w, err)
		return
	}
	if err := s.svc.Store().SetAdminPassword(r.Context(), hash); err != nil {
		writeErr(w, err)
		return
	}

	msg := "已通过 CLI 初始化管理员账号"
	if initOK {
		msg = "已通过本机 CLI 重置管理员密码"
	}
	s.svc.Store().AppendAudit(r.Context(), store.AuditEvent{
		Severity:    "info",
		Category:    "auth",
		TargetLabel: "管理员",
		Message:     msg,
		Actor:       actorFrom(r),
	})

	resp := map[string]any{
		"updated": true,
	}
	if generated != "" {
		resp["generated_password"] = generated
	}
	writeOK(w, resp)
}

func (s *Server) issueSessionCookie(w http.ResponseWriter, r *http.Request, username string) (string, error) {
	rawTok, err := crypto.GenerateRandomSecret("vsess_", 24)
	if err != nil {
		return "", err
	}
	exp := time.Now().UTC().Add(30 * 24 * time.Hour)
	if err := s.svc.Store().CreateSession(r.Context(), crypto.HashTokenSHA256(rawTok), username, exp); err != nil {
		return "", err
	}
	isHTTPS := r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    rawTok,
		Path:     "/",
		Expires:  exp,
		MaxAge:   30 * 24 * 3600,
		HttpOnly: true,
		Secure:   isHTTPS,
		SameSite: http.SameSiteLaxMode,
	})
	return rawTok, nil
}

func (s *Server) handleCapabilities(w http.ResponseWriter, r *http.Request) {
	rep, err := s.svc.Capabilities(r.Context(), true)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeOK(w, rep)
}

func (s *Server) handleDashboard(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	caps, err := s.svc.Capabilities(ctx, true)
	if err != nil {
		writeErr(w, err)
		return
	}
	routes, err := s.svc.Store().ListRoutes(ctx)
	if err != nil {
		writeErr(w, err)
		return
	}
	providers, err := s.svc.Store().ListDNSProviders(ctx)
	if err != nil {
		writeErr(w, err)
		return
	}
	tokens, err := s.svc.Store().ListAPITokens(ctx)
	if err != nil {
		writeErr(w, err)
		return
	}
	audit, err := s.svc.Store().ListAuditEvents(ctx, 40)
	if err != nil {
		writeErr(w, err)
		return
	}

	nodesOnline := 0
	for _, n := range caps.Nodes {
		if n.HealthStatus == "online" || n.HealthStatus == "degraded" || n.HealthStatus == "drift" {
			nodesOnline++
		}
	}

	minCertDays := -1
	minCertDomain := ""
	degradedCount := 0
	driftCount := 0
	for _, rt := range routes {
		if rt.OriginTLSOK && rt.OriginCertDays >= 0 {
			if minCertDays == -1 || rt.OriginCertDays < minCertDays {
				minCertDays = rt.OriginCertDays
				minCertDomain = rt.FullDomain
			}
		}
		if rt.LifecycleState == "degraded" || rt.LifecycleState == "error" {
			degradedCount++
		}
		if rt.LifecycleState == "config_drift" {
			driftCount++
		}
	}

	writeOK(w, map[string]any{
		"capabilities": caps,
		"routes":       routes,
		"containers":   caps.Containers,
		"nodes":        caps.Nodes,
		"zones":        caps.Zones,
		"providers":    providers,
		"tokens":       tokens,
		"events":       audit,
		"summary": map[string]any{
			"active_routes":        caps.ActiveRoutes,
			"total_routes":         caps.TotalRoutes,
			"unexposed_containers": caps.UnexposedContainers,
			"container_count":      caps.ContainerCount,
			"nodes_online":         nodesOnline,
			"nodes_total":          len(caps.Nodes),
			"min_cert_days":        minCertDays,
			"min_cert_domain":      minCertDomain,
			"degraded_count":       degradedCount,
			"drift_count":          driftCount,
		},
	})
}

func (s *Server) handleDoctor(w http.ResponseWriter, r *http.Request) {
	rep, err := s.svc.Doctor(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	writeOK(w, rep)
}

func (s *Server) handleSkill(w http.ResponseWriter, r *http.Request) {
	scheme := "http"
	if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
		scheme = "https"
	}
	endpoint := scheme + "://" + r.Host
	writeOK(w, map[string]any{
		"endpoint": endpoint,
		"skill_md": skilldoc.Render(endpoint, i18n.FromRequest(r)),
	})
}

func (s *Server) handleUpdateInfo(w http.ResponseWriter, r *http.Request) {
	if s.updater == nil {
		writeError(w, &orchestrator.AppError{
			Code:       "NOT_FOUND",
			Message:    "当前实例未启用版本更新检查",
			HTTPStatus: http.StatusNotFound,
		})
		return
	}
	writeOK(w, s.updater.Info(r.Context(), r.URL.Query().Get("refresh") == "1"))
}

func (s *Server) handleUpdateApply(w http.ResponseWriter, r *http.Request) {
	if s.updater == nil || s.restart == nil {
		writeError(w, &orchestrator.AppError{
			Code:       "NOT_FOUND",
			Message:    "当前实例未启用一键更新",
			HTTPStatus: http.StatusNotFound,
		})
		return
	}
	from := config.Version
	to, err := s.updater.Apply(r.Context())
	if err != nil {
		writeError(w, &orchestrator.AppError{
			Code:       "UPDATE_FAILED",
			Message:    err.Error(),
			Suggestion: "可前往 GitHub Releases 手动下载新版本替换二进制",
			HTTPStatus: http.StatusConflict,
		})
		return
	}
	s.svc.Store().AppendAudit(r.Context(), store.AuditEvent{
		Severity:    "info",
		Category:    "system",
		TargetLabel: to,
		Message:     fmt.Sprintf("已从 %s 更新至 %s，服务即将重启", from, to),
		Actor:       actorFrom(r),
	})
	writeOK(w, map[string]any{
		"updated":    true,
		"from":       from,
		"to":         to,
		"restarting": true,
	})
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
	go func() {
		time.Sleep(800 * time.Millisecond)
		s.restart()
	}()
}

func (s *Server) handleListTokens(w http.ResponseWriter, r *http.Request) {
	list, err := s.svc.Store().ListAPITokens(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	writeOK(w, list)
}

func (s *Server) handleCreateToken(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name  string `json:"name"`
		Scope string `json:"scope"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeErr(w, err)
		return
	}
	name := strings.TrimSpace(body.Name)
	if name == "" {
		name = "ai-agent"
	}
	scope := strings.TrimSpace(body.Scope)
	if scope == "" {
		scope = "admin"
	}
	if scope != "admin" && scope != "readonly" {
		writeError(w, &orchestrator.AppError{
			Code:       "VALIDATION_ERROR",
			Message:    "scope 仅支持 admin 或 readonly",
			HTTPStatus: http.StatusBadRequest,
		})
		return
	}

	fullTok, prefix, shaHex, err := crypto.GenerateAPIToken()
	if err != nil {
		writeErr(w, err)
		return
	}
	rec := store.APIToken{
		ID:          ulid.New(),
		Name:        name,
		TokenPrefix: prefix,
		TokenSHA256: shaHex,
		Scope:       scope,
		CreatedAt:   time.Now().UTC(),
	}
	if err := s.svc.Store().CreateAPIToken(r.Context(), rec); err != nil {
		writeErr(w, err)
		return
	}
	s.svc.Store().AppendAudit(r.Context(), store.AuditEvent{
		Severity:    "info",
		Category:    "auth",
		TargetID:    rec.ID,
		TargetLabel: rec.Name,
		Message:     fmt.Sprintf("已签发 API Token %s (%s 权限)", rec.TokenPrefix+"...", rec.Scope),
		Actor:       actorFrom(r),
	})
	writeOK(w, map[string]any{
		"token":      rec,
		"plain_text": fullTok,
	})
}

func (s *Server) handleDeleteToken(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := s.svc.Store().DeleteAPIToken(r.Context(), id); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, &orchestrator.AppError{Code: "NOT_FOUND", Message: "API Token 不存在", HTTPStatus: http.StatusNotFound})
			return
		}
		writeErr(w, err)
		return
	}
	writeOK(w, map[string]any{"deleted": id})
}

func (s *Server) handleListContainers(w http.ResponseWriter, r *http.Request) {
	list, available, err := s.svc.ListContainers(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	writeOK(w, map[string]any{
		"docker_available": available,
		"containers":       list,
	})
}

func (s *Server) handleListDNSProviders(w http.ResponseWriter, r *http.Request) {
	list, err := s.svc.Store().ListDNSProviders(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	writeOK(w, list)
}

func (s *Server) handleCreateDNSProvider(w http.ResponseWriter, r *http.Request) {
	var in orchestrator.AddDNSProviderInput
	if err := decodeJSON(r, &in); err != nil {
		writeErr(w, err)
		return
	}
	rec, zones, err := s.svc.AddDNSProvider(r.Context(), in, actorFrom(r))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeOK(w, map[string]any{
		"provider": rec,
		"zones":    zones,
	})
}

func (s *Server) handleSyncDNSProvider(w http.ResponseWriter, r *http.Request) {
	zones, err := s.svc.SyncDNSZones(r.Context(), chi.URLParam(r, "id"), actorFrom(r))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeOK(w, map[string]any{"zones": zones})
}

func (s *Server) handleDeleteDNSProvider(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := s.svc.DeleteDNSProvider(r.Context(), id, actorFrom(r)); err != nil {
		writeErr(w, err)
		return
	}
	writeOK(w, map[string]any{"deleted": id})
}

func (s *Server) handleListDNSZones(w http.ResponseWriter, r *http.Request) {
	zones, err := s.svc.Store().ListDNSZones(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	writeOK(w, zones)
}

func (s *Server) handleSetDefaultZone(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := s.svc.Store().SetDefaultZone(r.Context(), id); err != nil {
		writeErr(w, err)
		return
	}
	zones, _ := s.svc.Store().ListDNSZones(r.Context())
	writeOK(w, zones)
}

func (s *Server) handleListNodes(w http.ResponseWriter, r *http.Request) {
	nodes, err := s.svc.Store().ListEdgeNodes(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	writeOK(w, nodes)
}

func (s *Server) handlePreviewBootstrap(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name        string `json:"name"`
		PublicIPv4  string `json:"public_ipv4"`
		TailscaleIP string `json:"tailscale_ip"`
		AuthSecret  string `json:"auth_secret"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeErr(w, err)
		return
	}
	bundle, err := s.svc.GenerateEdgeBootstrap(body.Name, body.PublicIPv4, body.TailscaleIP, body.AuthSecret)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeOK(w, bundle)
}

func (s *Server) handleCreateNode(w http.ResponseWriter, r *http.Request) {
	var in orchestrator.AddEdgeNodeInput
	if err := decodeJSON(r, &in); err != nil {
		writeErr(w, err)
		return
	}
	node, err := s.svc.AddEdgeNode(r.Context(), in, actorFrom(r))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeOK(w, node)
}

func (s *Server) handleSetDefaultNode(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := s.svc.Store().SetDefaultEdgeNode(r.Context(), id); err != nil {
		writeErr(w, err)
		return
	}
	nodes, _ := s.svc.Store().ListEdgeNodes(r.Context())
	writeOK(w, nodes)
}

func (s *Server) handlePingNode(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if s.svc.Prober() == nil {
		node, err := s.svc.Store().GetEdgeNode(r.Context(), id)
		if err != nil {
			writeErr(w, err)
			return
		}
		writeOK(w, node)
		return
	}
	node, err := s.svc.Prober().ProbeNode(r.Context(), id)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeOK(w, node)
}

func (s *Server) handleReconcileNode(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	node, count, err := s.svc.ReconcileNode(r.Context(), id, actorFrom(r))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeOK(w, map[string]any{
		"node":          node,
		"pushed_routes": count,
	})
}

func (s *Server) handleDeleteNode(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := s.svc.DeleteEdgeNode(r.Context(), id, actorFrom(r)); err != nil {
		writeErr(w, err)
		return
	}
	writeOK(w, map[string]any{"deleted": id})
}

func (s *Server) handleListRoutes(w http.ResponseWriter, r *http.Request) {
	routes, err := s.svc.Store().ListRoutes(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	writeOK(w, routes)
}

func (s *Server) handlePublishRoute(w http.ResponseWriter, r *http.Request) {
	var in orchestrator.PublishInput
	if err := decodeJSON(r, &in); err != nil {
		writeErr(w, err)
		return
	}
	if r.URL.Query().Get("dry_run") == "true" {
		in.DryRun = true
	}
	res, err := s.svc.Publish(r.Context(), in, actorFrom(r))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeOK(w, res)
}

func (s *Server) handleUpdateRoute(w http.ResponseWriter, r *http.Request) {
	var up orchestrator.RouteUpdate
	if err := decodeJSON(r, &up); err != nil {
		writeErr(w, err)
		return
	}
	res, err := s.svc.UpdateRoute(r.Context(), chi.URLParam(r, "id"), up, actorFrom(r))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeOK(w, res)
}

func (s *Server) handleGetRoute(w http.ResponseWriter, r *http.Request) {
	detail, err := s.svc.GetRouteDetail(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeOK(w, detail)
}

func (s *Server) handleProbeRoute(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if s.svc.Prober() != nil {
		if _, err := s.svc.Prober().ProbeRoute(r.Context(), id); err != nil {
			writeErr(w, err)
			return
		}
	}
	detail, err := s.svc.GetRouteDetail(r.Context(), id)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeOK(w, detail)
}

func (s *Server) handleToggleProxy(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Proxied bool `json:"proxied"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeErr(w, err)
		return
	}
	rt, err := s.svc.ToggleProxy(r.Context(), chi.URLParam(r, "id"), body.Proxied, actorFrom(r))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeOK(w, rt)
}

func (s *Server) handleSetRouteAuth(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeErr(w, err)
		return
	}
	rt, err := s.svc.SetRouteAuth(r.Context(), chi.URLParam(r, "id"), body.Username, body.Password, actorFrom(r))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeOK(w, rt)
}

func (s *Server) handlePauseRoute(w http.ResponseWriter, r *http.Request) {
	rt, err := s.svc.PauseRoute(r.Context(), chi.URLParam(r, "id"), actorFrom(r))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeOK(w, rt)
}

func (s *Server) handleResumeRoute(w http.ResponseWriter, r *http.Request) {
	rt, err := s.svc.ResumeRoute(r.Context(), chi.URLParam(r, "id"), actorFrom(r))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeOK(w, rt)
}

func (s *Server) handleRetryRoute(w http.ResponseWriter, r *http.Request) {
	rt, err := s.svc.RetryRoute(r.Context(), chi.URLParam(r, "id"), actorFrom(r))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeOK(w, rt)
}

func (s *Server) handleDeleteRoute(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	deleteDNS := r.URL.Query().Get("delete_dns") != "false"
	if err := s.svc.DeleteRoute(r.Context(), id, deleteDNS, actorFrom(r)); err != nil {
		writeErr(w, err)
		return
	}
	writeOK(w, map[string]any{"deleted": id, "delete_dns": deleteDNS})
}

func (s *Server) handleListEvents(w http.ResponseWriter, r *http.Request) {
	list, err := s.svc.Store().ListAuditEvents(r.Context(), 60)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeOK(w, list)
}

func (s *Server) handleEventsSSE(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	lang := i18n.FromRequest(r)
	ch := s.svc.Hub().Subscribe(r.Context())
	ticker := time.NewTicker(20 * time.Second)
	defer ticker.Stop()

	_, _ = fmt.Fprintf(w, "event: connected\ndata: {\"ok\":true}\n\n")
	flusher.Flush()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
			_, _ = fmt.Fprintf(w, ": heartbeat %d\n\n", time.Now().Unix())
			flusher.Flush()
		case ev, open := <-ch:
			if !open {
				return
			}
			b, err := json.Marshal(ev)
			if err != nil {
				continue
			}
			b = bytes.TrimRight(i18n.LocalizeJSON(lang, b), "\n")
			_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", ev.Type, string(b))
			flusher.Flush()
		}
	}
}

// --- JSON Helpers ---

func decodeJSON(r *http.Request, dst any) error {
	defer r.Body.Close()
	dec := json.NewDecoder(io.LimitReader(r.Body, 2<<20))
	if err := dec.Decode(dst); err != nil {
		return &orchestrator.AppError{
			Code:       "VALIDATION_ERROR",
			Message:    "请求 JSON 格式无效: " + err.Error(),
			HTTPStatus: http.StatusBadRequest,
		}
	}
	return nil
}

func writeOK(w http.ResponseWriter, data any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(Envelope{
		OK:   true,
		Data: data,
		Meta: map[string]any{
			"version":   config.Version,
			"timestamp": time.Now().UTC().Format(time.RFC3339),
		},
	})
}

func writeError(w http.ResponseWriter, appErr *orchestrator.AppError) {
	status := appErr.HTTPStatus
	if status == 0 {
		status = http.StatusInternalServerError
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(Envelope{
		OK:    false,
		Error: appErr,
		Meta: map[string]any{
			"version":   config.Version,
			"timestamp": time.Now().UTC().Format(time.RFC3339),
		},
	})
}

func writeErr(w http.ResponseWriter, err error) {
	var appErr *orchestrator.AppError
	if errors.As(err, &appErr) {
		writeError(w, appErr)
		return
	}
	writeError(w, &orchestrator.AppError{
		Code:       "INTERNAL_ERROR",
		Message:    err.Error(),
		HTTPStatus: http.StatusInternalServerError,
	})
}

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err == nil && host != "" {
		return host
	}
	return r.RemoteAddr
}
