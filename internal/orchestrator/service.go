package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/llanc/vantu/internal/caddy"
	"github.com/llanc/vantu/internal/config"
	"github.com/llanc/vantu/internal/crypto"
	"github.com/llanc/vantu/internal/discovery"
	"github.com/llanc/vantu/internal/dns"
	"github.com/llanc/vantu/internal/events"
	"github.com/llanc/vantu/internal/prober"
	"github.com/llanc/vantu/internal/relay"
	"github.com/llanc/vantu/internal/store"
	"github.com/llanc/vantu/internal/tailscale"
	"github.com/llanc/vantu/internal/ulid"
)

// AppError carries deterministic machine-readable error codes, remediation hints, HTTP statuses, and CLI exit codes.
type AppError struct {
	Code       string `json:"code"`
	Message    string `json:"message"`
	Suggestion string `json:"suggestion,omitempty"`
	HTTPStatus int    `json:"-"`
	ExitCode   int    `json:"-"`
}

func (e *AppError) Error() string {
	return e.Message
}

func newAppErr(code, msg, suggestion string, httpStatus, exitCode int) *AppError {
	return &AppError{
		Code:       code,
		Message:    msg,
		Suggestion: suggestion,
		HTTPStatus: httpStatus,
		ExitCode:   exitCode,
	}
}

// StepProgress describes a single step in the publish / teardown workflow.
type StepProgress struct {
	Step   string `json:"step"`
	Title  string `json:"title"`
	Status string `json:"status"` // "planned" | "running" | "done" | "error"
	Detail string `json:"detail"`
}

// PublishInput accepts either Web UI structured fields or CLI shorthand target/domain strings.
type PublishInput struct {
	RouteID        string `json:"route_id,omitempty"`
	Target         string `json:"target,omitempty"` // e.g. "container:sub2api", "127.0.0.1:8080", "https://192.168.1.2:8006"
	SourceType     string `json:"source_type,omitempty"`
	ContainerName  string `json:"container_name,omitempty"`
	UpstreamScheme string `json:"upstream_scheme,omitempty"`
	LocalHost      string `json:"local_host,omitempty"`
	LocalPort      int    `json:"local_port,omitempty"`

	Domain     string `json:"domain,omitempty"`    // Full domain ("api.xiaofantu.com") or subdomain ("api")
	Subdomain  string `json:"subdomain,omitempty"` // Optional explicit subdomain
	ZoneID     string `json:"zone_id,omitempty"`
	EdgeNodeID string `json:"edge_node_id,omitempty"`

	ForwardMode    string `json:"forward_mode,omitempty"` // "auto" | "direct" | "relay"
	Preset         string `json:"preset,omitempty"`       // "web" | "sse" | "webdav" | "https_insecure"
	DNSProxied     bool   `json:"dns_proxied"`
	AdoptDNS       bool   `json:"adopt_dns"`
	MaxBodyMB      int    `json:"max_body_mb,omitempty"`
	ReadTimeoutSec int    `json:"read_timeout_sec,omitempty"`
	Note           string `json:"note,omitempty"`

	SkipLocalCheck bool          `json:"skip_local_check,omitempty"`
	DryRun         bool          `json:"dry_run,omitempty"`
	WaitTLS        bool          `json:"wait_tls,omitempty"`
	WaitTimeout    time.Duration `json:"-"`
}

// PublishResult is returned by Publish (both dry-run preview and real execution).
type PublishResult struct {
	DryRun       bool           `json:"dry_run"`
	Route        store.Route    `json:"route"`
	Steps        []StepProgress `json:"steps"`
	CaddyPayload map[string]any `json:"caddy_payload"`
	CLICommand   string         `json:"cli_command"`
}

// AddDNSProviderInput creates and verifies a DNS provider.
type AddDNSProviderInput struct {
	Name         string `json:"name"`
	ProviderType string `json:"provider_type"` // "cloudflare" | "dnspod"
	APIToken     string `json:"api_token,omitempty"`
	SecretID     string `json:"secret_id,omitempty"`
	SecretKey    string `json:"secret_key,omitempty"`
	BaseURL      string `json:"base_url,omitempty"`
}

// AddEdgeNodeInput registers an Edge VPS running Caddy 2.8+.
type AddEdgeNodeInput struct {
	Name          string `json:"name"`
	Region        string `json:"region"`
	PublicIPv4    string `json:"public_ipv4"`
	PublicIPv6    string `json:"public_ipv6"`
	TailscaleIP   string `json:"tailscale_ip"`
	CaddyAdminURL string `json:"caddy_admin_url"`
	AuthSecret    string `json:"auth_secret"`
	IsDefault     bool   `json:"is_default"`
	SkipVerify    bool   `json:"skip_verify"`
}

// CapabilitiesReport provides AI agents and Web UI with complete system readiness context in a single call.
type CapabilitiesReport struct {
	Version             string                          `json:"version"`
	Initialized         bool                            `json:"initialized"`
	DBSizeBytes         int64                           `json:"db_size_bytes"`
	DockerAvailable     bool                            `json:"docker_available"`
	ContainerCount      int                             `json:"container_count"`
	UnexposedContainers int                             `json:"unexposed_containers"`
	Tailscale           tailscale.SelfInfo              `json:"tailscale"`
	RelayRunning        bool                            `json:"relay_running"`
	RelayAddr           string                          `json:"relay_addr"`
	DefaultZone         *store.DNSZone                  `json:"default_zone,omitempty"`
	DefaultNode         *store.EdgeNode                 `json:"default_node,omitempty"`
	Zones               []store.DNSZone                 `json:"zones"`
	Nodes               []store.EdgeNode                `json:"nodes"`
	ActiveRoutes        int                             `json:"active_routes"`
	TotalRoutes         int                             `json:"total_routes"`
	Presets             []string                        `json:"presets"`
	Containers          []discovery.DiscoveredContainer `json:"containers,omitempty"`
}

// RouteDetail bundles a route with its probe history and rendered Caddy JSON block for the Inspector Drawer.
type RouteDetail struct {
	Route        store.Route         `json:"route"`
	History      []store.ProbeSample `json:"history"`
	CaddyPayload map[string]any      `json:"caddy_payload"`
	CLICommand   string              `json:"cli_command"`
}

// DoctorCheck represents a single diagnostic item in `vantu doctor`.
type DoctorCheck struct {
	Component string `json:"component"`
	Status    string `json:"status"` // "ok" | "warn" | "error"
	Detail    string `json:"detail"`
	Hint      string `json:"hint,omitempty"`
}

// DoctorReport aggregates environment health checks.
type DoctorReport struct {
	Healthy bool          `json:"healthy"`
	Checks  []DoctorCheck `json:"checks"`
}

// Service coordinates Store, Vault, Docker, Tailscale, Relay, DNS Providers, Edge Caddy, and Prober.
type Service struct {
	cfg         config.Config
	store       *store.Store
	vault       *crypto.Vault
	docker      *discovery.DockerWatcher
	tsInspector *tailscale.Inspector
	relay       *relay.Server
	prober      *prober.Prober
	hub         *events.Hub
}

func NewService(
	cfg config.Config,
	st *store.Store,
	vault *crypto.Vault,
	docker *discovery.DockerWatcher,
	ts *tailscale.Inspector,
	rl *relay.Server,
	pr *prober.Prober,
	hub *events.Hub,
) *Service {
	return &Service{
		cfg:         cfg,
		store:       st,
		vault:       vault,
		docker:      docker,
		tsInspector: ts,
		relay:       rl,
		prober:      pr,
		hub:         hub,
	}
}

func (s *Service) Store() *store.Store    { return s.store }
func (s *Service) Vault() *crypto.Vault   { return s.vault }
func (s *Service) Prober() *prober.Prober { return s.prober }
func (s *Service) Hub() *events.Hub       { return s.hub }

// Capabilities returns full environment readiness for Web UI & AI CLI (`vantu capabilities`).
func (s *Service) Capabilities(ctx context.Context, includeContainers bool) (*CapabilitiesReport, error) {
	initOK, err := s.store.IsInitialized(ctx)
	if err != nil {
		return nil, err
	}
	zones, err := s.store.ListDNSZones(ctx)
	if err != nil {
		return nil, err
	}
	nodes, err := s.store.ListEdgeNodes(ctx)
	if err != nil {
		return nil, err
	}
	routes, err := s.store.ListRoutes(ctx)
	if err != nil {
		return nil, err
	}

	containers, dockerOK, _ := s.ListContainers(ctx)
	unexposed := 0
	for _, c := range containers {
		if len(c.PublishedDomains) == 0 {
			unexposed++
		}
	}

	var defZone *store.DNSZone
	for i := range zones {
		if zones[i].IsDefault {
			defZone = &zones[i]
			break
		}
	}
	if defZone == nil && len(zones) > 0 {
		defZone = &zones[0]
	}

	var defNode *store.EdgeNode
	for i := range nodes {
		if nodes[i].IsDefault {
			defNode = &nodes[i]
			break
		}
	}
	if defNode == nil && len(nodes) > 0 {
		defNode = &nodes[0]
	}

	activeRoutes := 0
	for _, r := range routes {
		if r.Enabled && (r.LifecycleState == "active" || r.LifecycleState == "issuing_tls") {
			activeRoutes++
		}
	}

	var tsInfo tailscale.SelfInfo
	if s.tsInspector != nil {
		tsInfo = s.tsInspector.SelfStatus(ctx)
	}

	relayRun := false
	relayAddr := ""
	if s.relay != nil {
		relayRun = s.relay.Running()
		relayAddr = s.relay.BoundAddr()
	}

	rep := &CapabilitiesReport{
		Version:             config.Version,
		Initialized:         initOK,
		DBSizeBytes:         s.store.DBSizeBytes(),
		DockerAvailable:     dockerOK,
		ContainerCount:      len(containers),
		UnexposedContainers: unexposed,
		Tailscale:           tsInfo,
		RelayRunning:        relayRun,
		RelayAddr:           relayAddr,
		DefaultZone:         defZone,
		DefaultNode:         defNode,
		Zones:               zones,
		Nodes:               nodes,
		ActiveRoutes:        activeRoutes,
		TotalRoutes:         len(routes),
		Presets:             []string{"web", "sse", "webdav", "https_insecure"},
	}
	if includeContainers {
		rep.Containers = containers
	}
	return rep, nil
}

// ListContainers discovers running Docker containers and annotates which domains are already published.
func (s *Service) ListContainers(ctx context.Context) ([]discovery.DiscoveredContainer, bool, error) {
	if s.docker == nil {
		return []discovery.DiscoveredContainer{}, false, nil
	}
	routes, _ := s.store.ListRoutes(ctx)
	pubMap := make(map[string][]string)
	for _, r := range routes {
		if r.SourceType == "docker" && r.ContainerName != "" {
			pubMap[r.ContainerName] = append(pubMap[r.ContainerName], r.FullDomain)
		}
	}
	return s.docker.ListContainers(ctx, pubMap)
}

// AddDNSProvider validates credentials against the remote DNS API, encrypts secrets, and syncs root zones.
func (s *Service) AddDNSProvider(ctx context.Context, in AddDNSProviderInput, actor string) (*store.DNSProviderRecord, []store.DNSZone, error) {
	pType := strings.ToLower(strings.TrimSpace(in.ProviderType))
	if pType != "cloudflare" && pType != "dnspod" {
		return nil, nil, newAppErr("VALIDATION_ERROR", "仅支持 cloudflare 或 dnspod 解析厂商", "请检查 provider_type 参数", 400, 2)
	}
	name := strings.TrimSpace(in.Name)
	if name == "" {
		name = strings.ToUpper(pType[:1]) + pType[1:]
	}

	cred := dns.Credentials{
		APIToken:  strings.TrimSpace(in.APIToken),
		SecretID:  strings.TrimSpace(in.SecretID),
		SecretKey: strings.TrimSpace(in.SecretKey),
		BaseURL:   strings.TrimSpace(in.BaseURL),
	}
	credBytes, err := json.Marshal(cred)
	if err != nil {
		return nil, nil, err
	}

	drv, err := dns.Build(pType, string(credBytes))
	if err != nil {
		return nil, nil, newAppErr("VALIDATION_ERROR", err.Error(), "请填写完整的 API Token 或 SecretId/SecretKey", 400, 2)
	}

	remoteZones, err := drv.VerifyAndListZones(ctx)
	if err != nil {
		return nil, nil, newAppErr("DNS_ERROR", fmt.Sprintf("DNS 凭据校验失败：%v", err), "请确认 Cloudflare Token 具备 Zone:Read 与 DNS:Edit 权限", 502, 4)
	}
	if len(remoteZones) == 0 {
		return nil, nil, newAppErr("DNS_ERROR", "该 DNS 账号下未发现任何可用根域名 (Active Zone)", "请先将域名托管至 Cloudflare 或 DNSPod", 422, 4)
	}

	enc, err := s.vault.Encrypt(string(credBytes))
	if err != nil {
		return nil, nil, err
	}

	masked := crypto.MaskSecret(cred.APIToken)
	if pType == "dnspod" {
		masked = crypto.MaskSecret(cred.SecretID)
	}

	rec := store.DNSProviderRecord{
		ID:             ulid.New(),
		Name:           name,
		ProviderType:   pType,
		CredentialsEnc: enc,
		MaskedCred:     masked,
		Status:         "active",
	}
	if err := s.store.CreateDNSProvider(ctx, rec); err != nil {
		return nil, nil, err
	}

	toSave := make([]store.DNSZone, 0, len(remoteZones))
	for _, rz := range remoteZones {
		toSave = append(toSave, store.DNSZone{
			ID:            ulid.New(),
			ProviderID:    rec.ID,
			ZoneName:      strings.ToLower(rz.Name),
			RemoteZoneID:  rz.ID,
			SupportsProxy: rz.SupportsProxy,
		})
	}
	if err := s.store.UpsertDNSZones(ctx, rec.ID, toSave); err != nil {
		return nil, nil, err
	}

	zones, _ := s.store.ListDNSZones(ctx)
	s.store.AppendAudit(ctx, store.AuditEvent{
		Severity:    "info",
		Category:    "dns",
		TargetID:    rec.ID,
		TargetLabel: rec.Name,
		Message:     fmt.Sprintf("已接入 %s 域名解析凭据，同步 %d 个可用根域名", rec.Name, len(remoteZones)),
		Actor:       actor,
	})
	return &rec, zones, nil
}

// SyncDNSZones refreshes the zone list from a configured DNS provider.
func (s *Service) SyncDNSZones(ctx context.Context, providerID, actor string) ([]store.DNSZone, error) {
	rec, err := s.store.GetDNSProvider(ctx, providerID)
	if errors.Is(err, store.ErrNotFound) {
		return nil, newAppErr("NOT_FOUND", "未找到指定的 DNS 凭据", "", 404, 2)
	}
	if err != nil {
		return nil, err
	}
	drv, err := s.buildDNSDriver(rec)
	if err != nil {
		return nil, err
	}
	remoteZones, err := drv.VerifyAndListZones(ctx)
	if err != nil {
		return nil, newAppErr("DNS_ERROR", fmt.Sprintf("同步域名列表失败：%v", err), "请检查网络或 API Token 是否已失效", 502, 4)
	}
	toSave := make([]store.DNSZone, 0, len(remoteZones))
	for _, rz := range remoteZones {
		toSave = append(toSave, store.DNSZone{
			ID:            ulid.New(),
			ProviderID:    rec.ID,
			ZoneName:      strings.ToLower(rz.Name),
			RemoteZoneID:  rz.ID,
			SupportsProxy: rz.SupportsProxy,
		})
	}
	if err := s.store.UpsertDNSZones(ctx, rec.ID, toSave); err != nil {
		return nil, err
	}
	s.store.AppendAudit(ctx, store.AuditEvent{
		Severity:    "info",
		Category:    "dns",
		TargetID:    rec.ID,
		TargetLabel: rec.Name,
		Message:     fmt.Sprintf("手动同步完成，共发现 %d 个根域名", len(remoteZones)),
		Actor:       actor,
	})
	return s.store.ListDNSZones(ctx)
}

func (s *Service) DeleteDNSProvider(ctx context.Context, providerID, actor string) error {
	zones, _ := s.store.ListDNSZones(ctx)
	for _, z := range zones {
		if z.ProviderID == providerID && z.RouteCount > 0 {
			return newAppErr("VALIDATION_ERROR",
				fmt.Sprintf("域名 %s 下仍存在 %d 条在用路由，请先删除对应路由后再移除凭据", z.ZoneName, z.RouteCount),
				"", 409, 2)
		}
	}
	if err := s.store.DeleteDNSProvider(ctx, providerID); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return newAppErr("NOT_FOUND", "DNS 凭据不存在", "", 404, 2)
		}
		return err
	}
	s.store.AppendAudit(ctx, store.AuditEvent{
		Severity: "info",
		Category: "dns",
		TargetID: providerID,
		Message:  "已删除 DNS 厂商凭据",
		Actor:    actor,
	})
	return nil
}

// GenerateEdgeBootstrap builds zero-agent Caddy deployment files for an Edge VPS.
func (s *Service) GenerateEdgeBootstrap(name, publicIP, tailscaleIP, secret string) (caddy.BootstrapBundle, error) {
	if strings.TrimSpace(secret) == "" {
		gen, err := crypto.GenerateRandomSecret("vtu_edge_", 16)
		if err != nil {
			return caddy.BootstrapBundle{}, err
		}
		secret = gen
	}
	if strings.TrimSpace(name) == "" {
		name = "edge-node"
	}
	return caddy.GenerateBootstrapBundle(name, strings.TrimSpace(publicIP), strings.TrimSpace(tailscaleIP), strings.TrimSpace(secret)), nil
}

// AddEdgeNode verifies Edge Caddy Admin API reachability (unless SkipVerify=true) and saves the node.
func (s *Service) AddEdgeNode(ctx context.Context, in AddEdgeNodeInput, actor string) (*store.EdgeNode, error) {
	name := strings.TrimSpace(in.Name)
	pubIP := strings.TrimSpace(in.PublicIPv4)
	tsIP := strings.TrimSpace(in.TailscaleIP)
	secret := strings.TrimSpace(in.AuthSecret)

	if name == "" || pubIP == "" || tsIP == "" || secret == "" {
		return nil, newAppErr("VALIDATION_ERROR", "节点名称、公网 IPv4、Tailscale IP 与 Auth Secret 均不能为空", "", 400, 2)
	}

	adminURL := strings.TrimSpace(in.CaddyAdminURL)
	if adminURL == "" {
		adminURL = fmt.Sprintf("http://%s:2019", tsIP)
	} else if !strings.HasPrefix(adminURL, "http://") && !strings.HasPrefix(adminURL, "https://") {
		adminURL = "http://" + adminURL
	}

	health := "unknown"
	caddyVer := ""
	if !in.SkipVerify {
		cli := caddy.NewClient(adminURL, secret)
		ver, err := cli.Ping(ctx)
		if err != nil {
			return nil, newAppErr("EDGE_UNREACHABLE",
				fmt.Sprintf("无法连接边缘节点 Caddy Admin API (%s)：%v", adminURL, err),
				"请确认边缘 VPS 已运行 vantu-edge-caddy 容器且 Tailscale 互通、Auth Secret 一致", 502, 5)
		}
		if err := cli.EnsureServerBlock(ctx); err != nil {
			return nil, newAppErr("EDGE_UNREACHABLE",
				fmt.Sprintf("初始化边缘 Caddy vantu_srv 服务块失败：%v", err),
				"请检查边缘 Caddy 版本是否为 2.8+", 502, 5)
		}
		health = "online"
		caddyVer = ver
	}

	enc, err := s.vault.Encrypt(secret)
	if err != nil {
		return nil, err
	}

	now := time.Now().UTC()
	node := store.EdgeNode{
		ID:            ulid.New(),
		Name:          name,
		Region:        strings.TrimSpace(in.Region),
		PublicIPv4:    pubIP,
		PublicIPv6:    strings.TrimSpace(in.PublicIPv6),
		TailscaleIP:   tsIP,
		CaddyAdminURL: adminURL,
		AuthSecretEnc: enc,
		MaskedSecret:  crypto.MaskSecret(secret),
		IsDefault:     in.IsDefault,
		Enabled:       true,
		HealthStatus:  health,
		TSConnType:    "direct",
		TSRTTMs:       1,
		CaddyVersion:  caddyVer,
		LastSeenAt:    &now,
	}

	if err := s.store.CreateEdgeNode(ctx, node); err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique") {
			return nil, newAppErr("VALIDATION_ERROR", fmt.Sprintf("节点名称 %q 已存在", name), "请更换一个节点标识名", 409, 2)
		}
		return nil, err
	}

	if s.prober != nil && !in.SkipVerify {
		if probed, pErr := s.prober.ProbeNode(ctx, node.ID); pErr == nil {
			node = *probed
		}
	}

	s.store.AppendAudit(ctx, store.AuditEvent{
		Severity:    "info",
		Category:    "node",
		TargetID:    node.ID,
		TargetLabel: node.Name,
		Message:     fmt.Sprintf("已注册边缘公网节点 %s (公网 %s / Tailscale %s)", node.Name, node.PublicIPv4, node.TailscaleIP),
		Actor:       actor,
	})
	if s.hub != nil {
		s.hub.Publish("nodes_updated", map[string]any{"node_id": node.ID})
	}
	return &node, nil
}

// ReconcileNode heals configuration drift by re-pushing all enabled SQLite routes onto the Edge Node's Caddy instance.
func (s *Service) ReconcileNode(ctx context.Context, nodeIDOrName, actor string) (*store.EdgeNode, int, error) {
	node, err := s.store.GetEdgeNode(ctx, nodeIDOrName)
	if errors.Is(err, store.ErrNotFound) {
		return nil, 0, newAppErr("NOT_FOUND", fmt.Sprintf("边缘节点 %q 不存在", nodeIDOrName), "", 404, 2)
	}
	if err != nil {
		return nil, 0, err
	}

	secret, err := s.vault.Decrypt(node.AuthSecretEnc)
	if err != nil {
		return nil, 0, err
	}
	cli := caddy.NewClient(node.CaddyAdminURL, secret)
	if err := cli.EnsureServerBlock(ctx); err != nil {
		return nil, 0, newAppErr("EDGE_UNREACHABLE", fmt.Sprintf("连接边缘节点失败：%v", err), "", 502, 5)
	}

	routes, err := s.store.ListRoutes(ctx)
	if err != nil {
		return nil, 0, err
	}

	pushed := 0
	for _, rt := range routes {
		if rt.EdgeNodeID != node.ID {
			continue
		}
		if !rt.Enabled {
			_ = cli.DeleteRoute(ctx, rt.ID)
			continue
		}
		spec := s.routeToCaddySpec(rt)
		if err := cli.UpsertRoute(ctx, spec); err != nil {
			return nil, pushed, newAppErr("EDGE_UNREACHABLE", fmt.Sprintf("重推路由 %s 失败：%v", rt.FullDomain, err), "", 502, 5)
		}
		rt.CaddySynced = true
		if rt.LifecycleState == "config_drift" {
			rt.LifecycleState = "active"
			rt.LastError = ""
		}
		_ = s.store.UpsertRoute(ctx, rt)
		pushed++
	}

	if s.prober != nil {
		if updated, pErr := s.prober.ProbeNode(ctx, node.ID); pErr == nil {
			node = updated
		}
	}

	s.store.AppendAudit(ctx, store.AuditEvent{
		Severity:    "info",
		Category:    "node",
		TargetID:    node.ID,
		TargetLabel: node.Name,
		Message:     fmt.Sprintf("节点配置漂移对账修复完成，已同步 %d 条启用路由", pushed),
		Actor:       actor,
	})
	if s.hub != nil {
		s.hub.Publish("routes_updated", map[string]any{"reconciled_node": node.ID})
	}
	return node, pushed, nil
}

func (s *Service) DeleteEdgeNode(ctx context.Context, nodeIDOrName, actor string) error {
	node, err := s.store.GetEdgeNode(ctx, nodeIDOrName)
	if errors.Is(err, store.ErrNotFound) {
		return newAppErr("NOT_FOUND", "边缘节点不存在", "", 404, 2)
	}
	if err != nil {
		return err
	}
	if err := s.store.DeleteEdgeNode(ctx, node.ID); err != nil {
		return newAppErr("VALIDATION_ERROR", err.Error(), "请先移除绑定在该节点上的路由", 409, 2)
	}
	s.store.AppendAudit(ctx, store.AuditEvent{
		Severity:    "info",
		Category:    "node",
		TargetID:    node.ID,
		TargetLabel: node.Name,
		Message:     fmt.Sprintf("已移除边缘节点 %s", node.Name),
		Actor:       actor,
	})
	if s.hub != nil {
		s.hub.Publish("nodes_updated", map[string]any{"deleted": node.ID})
	}
	return nil
}

// Publish orchestrates end-to-end idempotent publishing (or returns an exact zero-side-effect plan when DryRun=true).
func (s *Service) Publish(ctx context.Context, in PublishInput, actor string) (*PublishResult, error) {
	rt, zone, node, err := s.prepareRoutePlan(ctx, in)
	if err != nil {
		return nil, err
	}

	caddySpec := s.routeToCaddySpec(rt)
	caddyPayload := caddy.BuildRoutePayload(caddySpec)
	cliCmd := buildCLICommand(rt)

	if in.DryRun {
		steps := []StepProgress{
			{Step: "local_check", Title: "检查本地服务", Status: "planned", Detail: fmt.Sprintf("%s://%s:%d (%s -> %s)", rt.UpstreamScheme, rt.LocalTargetHost, rt.LocalTargetPort, rt.ForwardMode, rt.EffectiveDial)},
			{Step: "dns_upsert", Title: "配置 DNS 解析", Status: "planned", Detail: fmt.Sprintf("%s A -> %s (Proxied=%v)", rt.FullDomain, node.PublicIPv4, rt.DNSProxied)},
			{Step: "caddy_push", Title: "推送 Caddy 路由", Status: "planned", Detail: fmt.Sprintf("%s (%s) @id=%s%s", node.Name, node.CaddyAdminURL, caddy.RouteIDPrefix, rt.ID)},
			{Step: "probe_verify", Title: "检测连通性与证书", Status: "planned", Detail: fmt.Sprintf("https://%s (%s:443)", rt.FullDomain, node.PublicIPv4)},
		}
		return &PublishResult{
			DryRun:       true,
			Route:        rt,
			Steps:        steps,
			CaddyPayload: caddyPayload,
			CLICommand:   cliCmd,
		}, nil
	}

	steps := make([]StepProgress, 0, 4)
	emitStep := func(step, title, status, detail string) {
		found := false
		for i := range steps {
			if steps[i].Step == step {
				steps[i].Status = status
				steps[i].Detail = detail
				found = true
				break
			}
		}
		if !found {
			steps = append(steps, StepProgress{Step: step, Title: title, Status: status, Detail: detail})
		}
		if s.hub != nil {
			s.hub.Publish("route_step", map[string]any{
				"domain": rt.FullDomain,
				"step":   step,
				"title":  title,
				"status": status,
				"detail": detail,
			})
		}
	}

	// Step 1: Local Target Reachability Check
	emitStep("local_check", "检查本地服务", "running", fmt.Sprintf("连接 %s:%d ...", rt.LocalTargetHost, rt.LocalTargetPort))
	localOK := true
	if !in.SkipLocalCheck {
		d := net.Dialer{Timeout: 2500 * time.Millisecond}
		conn, dErr := d.DialContext(ctx, "tcp", net.JoinHostPort(rt.LocalTargetHost, strconv.Itoa(rt.LocalTargetPort)))
		if dErr != nil {
			emitStep("local_check", "检查本地服务", "error", dErr.Error())
			return nil, newAppErr("LOCAL_TARGET_DOWN",
				fmt.Sprintf("本地目标服务 %s:%d 无法连接：%v", rt.LocalTargetHost, rt.LocalTargetPort, dErr),
				"请确认容器或内网服务已启动且端口号无误（如需跳过检查可传 skip_local_check）", 422, 6)
		}
		_ = conn.Close()
	}
	rt.LocalHealthy = localOK
	emitStep("local_check", "检查本地服务", "done", fmt.Sprintf("%s:%d 正常 (%s)", rt.LocalTargetHost, rt.LocalTargetPort, strings.ToUpper(rt.ForwardMode)))

	// Persist provisioning state in SQLite
	hadExistingDNS := rt.DNSRecordID != ""
	rt.LifecycleState = "provisioning"
	rt.Enabled = true
	if err := s.store.UpsertRoute(ctx, rt); err != nil {
		return nil, err
	}

	// Step 2: Upsert DNS A Record
	emitStep("dns_upsert", "配置 DNS 解析", "running", fmt.Sprintf("%s -> %s ...", rt.FullDomain, node.PublicIPv4))
	dnsRec, err := s.store.GetDNSProvider(ctx, zone.ProviderID)
	if err != nil {
		return nil, err
	}
	dnsDrv, err := s.buildDNSDriver(dnsRec)
	if err != nil {
		return nil, err
	}
	recordID, err := dnsDrv.EnsureRecord(ctx, dns.EnsureRecordInput{
		RemoteZoneID:     zone.RemoteZoneID,
		ZoneName:         zone.ZoneName,
		Subdomain:        rt.Subdomain,
		FullDomain:       rt.FullDomain,
		RecordType:       "A",
		Value:            node.PublicIPv4,
		Proxied:          rt.DNSProxied,
		ExistingRecordID: rt.DNSRecordID,
		AdoptExisting:    in.AdoptDNS,
	})
	if err != nil {
		emitStep("dns_upsert", "配置 DNS 解析", "error", err.Error())
		rt.LifecycleState = "error"
		rt.LastError = err.Error()
		_ = s.store.UpsertRoute(ctx, rt)
		if errors.Is(err, dns.ErrExternalRecordConflict) {
			return nil, newAppErr("DNS_CONFLICT", err.Error(), "勾选覆盖已有同名 DNS 记录或在 CLI 添加 --adopt-dns", 409, 4)
		}
		return nil, newAppErr("DNS_ERROR", fmt.Sprintf("写入 DNS 解析失败：%v", err), "请检查 DNS API Token 权限与网络连通性", 502, 4)
	}
	rt.DNSRecordID = recordID
	_ = s.store.UpsertRoute(ctx, rt)
	emitStep("dns_upsert", "配置 DNS 解析", "done", fmt.Sprintf("%s -> %s", rt.FullDomain, node.PublicIPv4))

	// Step 3: Push to Edge Node Caddy Admin API
	emitStep("caddy_push", "推送 Caddy 路由", "running", fmt.Sprintf("推送至 %s ...", node.Name))
	edgeSecret, err := s.vault.Decrypt(node.AuthSecretEnc)
	if err != nil {
		return nil, err
	}
	caddyCli := caddy.NewClient(node.CaddyAdminURL, edgeSecret)
	if err := caddyCli.UpsertRoute(ctx, caddySpec); err != nil {
		emitStep("caddy_push", "推送 Caddy 路由", "error", err.Error())
		// Automatic rollback of newly created DNS record so broken domain isn't left dangling!
		if !hadExistingDNS && recordID != "" {
			_ = dnsDrv.DeleteRecord(ctx, zone.RemoteZoneID, recordID)
			_ = s.store.DeleteRoute(ctx, rt.ID)
		} else {
			rt.LifecycleState = "error"
			rt.LastError = err.Error()
			_ = s.store.UpsertRoute(ctx, rt)
		}
		return nil, newAppErr("EDGE_UNREACHABLE",
			fmt.Sprintf("推送边缘节点 %s Caddy 配置失败：%v", node.Name, err),
			"已回滚本次新增 DNS 记录；请检查边缘节点 Tailscale 与 Caddy 容器状态", 502, 5)
	}
	rt.CaddySynced = true
	rt.LifecycleState = "issuing_tls"
	rt.LastError = ""
	_ = s.store.UpsertRoute(ctx, rt)
	emitStep("caddy_push", "推送 Caddy 路由", "done", fmt.Sprintf("%s (@id=%s%s)", node.Name, caddy.RouteIDPrefix, rt.ID))

	// Step 4: Probe Verification
	emitStep("probe_verify", "检测连通性与证书", "running", "正在检测源站 TLS 与 HTTP 响应...")
	if s.prober != nil {
		if fresh, pErr := s.prober.ProbeRoute(ctx, rt.ID); pErr == nil && fresh != nil {
			rt = *fresh
		}
		if in.WaitTLS && rt.LifecycleState != "active" {
			waitDur := in.WaitTimeout
			if waitDur <= 0 {
				waitDur = 45 * time.Second
			}
			deadline := time.Now().Add(waitDur)
			for time.Now().Before(deadline) {
				select {
				case <-ctx.Done():
					return nil, ctx.Err()
				case <-time.After(2 * time.Second):
				}
				if fresh, pErr := s.prober.ProbeRoute(ctx, rt.ID); pErr == nil && fresh != nil {
					rt = *fresh
					if rt.OriginTLSOK && rt.LifecycleState == "active" {
						break
					}
				}
			}
			if !rt.OriginTLSOK {
				emitStep("probe_verify", "检测连通性与证书", "error", "等待证书签发超时，后台继续检测")
				return &PublishResult{
					DryRun:       false,
					Route:        rt,
					Steps:        steps,
					CaddyPayload: caddyPayload,
					CLICommand:   cliCmd,
				}, newAppErr("TLS_TIMEOUT",
					fmt.Sprintf("路由已发布至 %s，但等待 TLS 证书签发超时", rt.FullDomain),
					"Caddy 仍在后台申请证书，稍后可通过 vantu route inspect 查看状态", 504, 7)
			}
		}
	}
	emitStep("probe_verify", "检测连通性与证书", "done", fmt.Sprintf("%s (HTTP %d)", rt.LifecycleState, rt.PublicHTTPCode))

	s.store.AppendAudit(ctx, store.AuditEvent{
		Severity:    "info",
		Category:    "route",
		TargetID:    rt.ID,
		TargetLabel: rt.FullDomain,
		Message:     fmt.Sprintf("已发布公网服务 %s -> %s:%d (%s / %s 节点)", rt.FullDomain, rt.LocalTargetHost, rt.LocalTargetPort, strings.ToUpper(rt.ForwardMode), node.Name),
		Actor:       actor,
	})
	if s.hub != nil {
		s.hub.Publish("routes_updated", map[string]any{"route_id": rt.ID})
	}

	return &PublishResult{
		DryRun:       false,
		Route:        rt,
		Steps:        steps,
		CaddyPayload: caddyPayload,
		CLICommand:   cliCmd,
	}, nil
}

// GetRouteDetail returns a route, its probe history bars, and its exact Caddy JSON block.
func (s *Service) GetRouteDetail(ctx context.Context, idOrDomain string) (*RouteDetail, error) {
	rt, err := s.store.GetRoute(ctx, idOrDomain)
	if errors.Is(err, store.ErrNotFound) {
		return nil, newAppErr("NOT_FOUND", fmt.Sprintf("未找到路由 %q", idOrDomain), "可通过 vantu route ls 查看所有已发布路由", 404, 2)
	}
	if err != nil {
		return nil, err
	}
	hist, _ := s.store.ListProbeHistory(ctx, rt.ID, 36)
	spec := s.routeToCaddySpec(*rt)
	return &RouteDetail{
		Route:        *rt,
		History:      hist,
		CaddyPayload: caddy.BuildRoutePayload(spec),
		CLICommand:   buildCLICommand(*rt),
	}, nil
}

// ToggleProxy switches Cloudflare Orange Cloud CDN on or off for an existing route.
func (s *Service) ToggleProxy(ctx context.Context, idOrDomain string, proxied bool, actor string) (*store.Route, error) {
	rt, err := s.store.GetRoute(ctx, idOrDomain)
	if errors.Is(err, store.ErrNotFound) {
		return nil, newAppErr("NOT_FOUND", fmt.Sprintf("未找到路由 %q", idOrDomain), "", 404, 2)
	}
	if err != nil {
		return nil, err
	}
	zone, err := s.store.GetDNSZone(ctx, rt.ZoneID)
	if err != nil {
		return nil, err
	}
	if proxied && !zone.SupportsProxy {
		return nil, newAppErr("VALIDATION_ERROR", fmt.Sprintf("域名 %s 所属解析厂商不支持 Cloudflare CDN 代理", zone.ZoneName), "", 400, 2)
	}
	node, err := s.store.GetEdgeNode(ctx, rt.EdgeNodeID)
	if err != nil {
		return nil, err
	}
	dnsRec, err := s.store.GetDNSProvider(ctx, zone.ProviderID)
	if err != nil {
		return nil, err
	}
	drv, err := s.buildDNSDriver(dnsRec)
	if err != nil {
		return nil, err
	}

	if err := drv.SetProxied(ctx, zone.RemoteZoneID, rt.FullDomain, node.PublicIPv4, rt.DNSRecordID, proxied); err != nil {
		return nil, newAppErr("DNS_ERROR", fmt.Sprintf("切换 CDN 代理状态失败：%v", err), "", 502, 4)
	}

	rt.DNSProxied = proxied
	if err := s.store.UpsertRoute(ctx, *rt); err != nil {
		return nil, err
	}
	if s.prober != nil {
		if fresh, pErr := s.prober.ProbeRoute(ctx, rt.ID); pErr == nil {
			rt = fresh
		}
	}

	modeText := "DNS 直连"
	if proxied {
		modeText = "Cloudflare CDN"
	}
	s.store.AppendAudit(ctx, store.AuditEvent{
		Severity:    "info",
		Category:    "route",
		TargetID:    rt.ID,
		TargetLabel: rt.FullDomain,
		Message:     fmt.Sprintf("已将 %s 切换为 %s", rt.FullDomain, modeText),
		Actor:       actor,
	})
	if s.hub != nil {
		s.hub.Publish("routes_updated", map[string]any{"route_id": rt.ID})
	}
	return rt, nil
}

// PauseRoute temporarily removes the route from Edge Caddy while retaining its DNS record and configuration.
func (s *Service) PauseRoute(ctx context.Context, idOrDomain, actor string) (*store.Route, error) {
	rt, err := s.store.GetRoute(ctx, idOrDomain)
	if errors.Is(err, store.ErrNotFound) {
		return nil, newAppErr("NOT_FOUND", fmt.Sprintf("未找到路由 %q", idOrDomain), "", 404, 2)
	}
	if err != nil {
		return nil, err
	}
	node, err := s.store.GetEdgeNode(ctx, rt.EdgeNodeID)
	if err == nil {
		if secret, sErr := s.vault.Decrypt(node.AuthSecretEnc); sErr == nil {
			_ = caddy.NewClient(node.CaddyAdminURL, secret).DeleteRoute(ctx, rt.ID)
		}
	}
	rt.Enabled = false
	rt.CaddySynced = false
	rt.LifecycleState = "paused"
	rt.LastError = ""
	if err := s.store.UpsertRoute(ctx, *rt); err != nil {
		return nil, err
	}
	s.store.AppendAudit(ctx, store.AuditEvent{
		Severity:    "info",
		Category:    "route",
		TargetID:    rt.ID,
		TargetLabel: rt.FullDomain,
		Message:     fmt.Sprintf("已暂停路由 %s 的公网流量转发", rt.FullDomain),
		Actor:       actor,
	})
	if s.hub != nil {
		s.hub.Publish("routes_updated", map[string]any{"route_id": rt.ID})
	}
	return rt, nil
}

// ResumeRoute re-enables a paused route and pushes its configuration back to Edge Caddy.
func (s *Service) ResumeRoute(ctx context.Context, idOrDomain, actor string) (*store.Route, error) {
	return s.RetryRoute(ctx, idOrDomain, actor)
}

// RetryRoute re-applies DNS and Edge Caddy configuration for an existing route and runs an immediate probe.
func (s *Service) RetryRoute(ctx context.Context, idOrDomain, actor string) (*store.Route, error) {
	rt, err := s.store.GetRoute(ctx, idOrDomain)
	if errors.Is(err, store.ErrNotFound) {
		return nil, newAppErr("NOT_FOUND", fmt.Sprintf("未找到路由 %q", idOrDomain), "", 404, 2)
	}
	if err != nil {
		return nil, err
	}

	res, err := s.Publish(ctx, PublishInput{
		RouteID:        rt.ID,
		SourceType:     rt.SourceType,
		ContainerName:  rt.ContainerName,
		UpstreamScheme: rt.UpstreamScheme,
		LocalHost:      rt.LocalTargetHost,
		LocalPort:      rt.LocalTargetPort,
		Domain:         rt.FullDomain,
		ZoneID:         rt.ZoneID,
		EdgeNodeID:     rt.EdgeNodeID,
		ForwardMode:    rt.ForwardMode,
		Preset:         rt.Preset,
		DNSProxied:     rt.DNSProxied,
		AdoptDNS:       true,
		MaxBodyMB:      rt.MaxBodyMB,
		ReadTimeoutSec: rt.ReadTimeoutSec,
		Note:           rt.Note,
		SkipLocalCheck: true,
	}, actor)
	if err != nil {
		return nil, err
	}
	return &res.Route, nil
}

// DeleteRoute cleanly removes the route from Edge Caddy, deletes its DNS A record (if deleteDNS=true), and drops it from SQLite.
func (s *Service) DeleteRoute(ctx context.Context, idOrDomain string, deleteDNS bool, actor string) error {
	rt, err := s.store.GetRoute(ctx, idOrDomain)
	if errors.Is(err, store.ErrNotFound) {
		return newAppErr("NOT_FOUND", fmt.Sprintf("未找到路由 %q", idOrDomain), "", 404, 2)
	}
	if err != nil {
		return err
	}

	if node, nErr := s.store.GetEdgeNode(ctx, rt.EdgeNodeID); nErr == nil {
		if secret, sErr := s.vault.Decrypt(node.AuthSecretEnc); sErr == nil {
			_ = caddy.NewClient(node.CaddyAdminURL, secret).DeleteRoute(ctx, rt.ID)
		}
	}

	if deleteDNS && rt.DNSRecordID != "" {
		if zone, zErr := s.store.GetDNSZone(ctx, rt.ZoneID); zErr == nil {
			if dnsRec, pErr := s.store.GetDNSProvider(ctx, zone.ProviderID); pErr == nil {
				if drv, dErr := s.buildDNSDriver(dnsRec); dErr == nil {
					_ = drv.DeleteRecord(ctx, zone.RemoteZoneID, rt.DNSRecordID)
				}
			}
		}
	}

	if err := s.store.DeleteRoute(ctx, rt.ID); err != nil {
		return err
	}

	s.store.AppendAudit(ctx, store.AuditEvent{
		Severity:    "info",
		Category:    "route",
		TargetID:    rt.ID,
		TargetLabel: rt.FullDomain,
		Message:     fmt.Sprintf("已下线并移除路由 %s (清理 DNS=%v)", rt.FullDomain, deleteDNS),
		Actor:       actor,
	})
	if s.hub != nil {
		s.hub.Publish("routes_updated", map[string]any{"deleted": rt.ID})
	}
	return nil
}

// Doctor executes a complete system self-check for troubleshooting.
func (s *Service) Doctor(ctx context.Context) (*DoctorReport, error) {
	checks := make([]DoctorCheck, 0, 8)
	allHealthy := true

	// 1. SQLite Storage
	initOK, err := s.store.IsInitialized(ctx)
	if err != nil {
		allHealthy = false
		checks = append(checks, DoctorCheck{Component: "SQLite 存储", Status: "error", Detail: err.Error()})
	} else {
		detail := fmt.Sprintf("正常 (%d KB, 已初始化=%v)", s.store.DBSizeBytes()/1024+1, initOK)
		checks = append(checks, DoctorCheck{Component: "SQLite 存储", Status: "ok", Detail: detail})
	}

	// 2. Docker Socket
	containers, dockerOK, dockerErr := s.ListContainers(ctx)
	switch {
	case dockerErr != nil:
		checks = append(checks, DoctorCheck{
			Component: "Docker",
			Status:    "warn",
			Detail:    dockerErr.Error(),
			Hint:      "挂载 /var/run/docker.sock 可启用容器发现；未挂载时可使用 IP 与端口模式",
		})
	case !dockerOK:
		checks = append(checks, DoctorCheck{
			Component: "Docker",
			Status:    "warn",
			Detail:    "未连接 /var/run/docker.sock",
			Hint:      "挂载 docker.sock 可启用容器发现",
		})
	default:
		checks = append(checks, DoctorCheck{
			Component: "Docker",
			Status:    "ok",
			Detail:    fmt.Sprintf("已连接，运行中容器 %d 个", len(containers)),
		})
	}

	// 3. Tailscale Local Identity
	if s.tsInspector != nil {
		ts := s.tsInspector.SelfStatus(ctx)
		if ts.Available {
			checks = append(checks, DoctorCheck{
				Component: "Tailscale",
				Status:    "ok",
				Detail:    fmt.Sprintf("%s (%s)", ts.IPv4, ts.HostName),
			})
		} else {
			checks = append(checks, DoctorCheck{
				Component: "Tailscale",
				Status:    "warn",
				Detail:    "未检测到本机 Tailscale IPv4",
				Hint:      "请确认 tailscaled 已运行或配置环境变量 VANTU_TAILSCALE_IP",
			})
		}
	}

	// 4. Local Relay Gateway
	if s.relay != nil && s.relay.Running() {
		checks = append(checks, DoctorCheck{
			Component: "Local Relay",
			Status:    "ok",
			Detail:    fmt.Sprintf("监听于 %s", s.relay.BoundAddr()),
		})
	} else {
		allHealthy = false
		checks = append(checks, DoctorCheck{
			Component: "Local Relay",
			Status:    "error",
			Detail:    "未启动",
			Hint:      "请检查 VANTU_RELAY_PORT 端口是否被占用",
		})
	}

	// 5. DNS Providers
	providers, _ := s.store.ListDNSProviders(ctx)
	if len(providers) == 0 {
		checks = append(checks, DoctorCheck{
			Component: "DNS 凭据",
			Status:    "warn",
			Detail:    "尚未添加",
			Hint:      "运行 vantu dns add-cf 或在控制台域名页添加",
		})
	} else {
		for _, p := range providers {
			drv, err := s.buildDNSDriver(&p)
			if err != nil {
				allHealthy = false
				checks = append(checks, DoctorCheck{Component: "DNS: " + p.Name, Status: "error", Detail: err.Error()})
				continue
			}
			zs, err := drv.VerifyAndListZones(ctx)
			if err != nil {
				allHealthy = false
				checks = append(checks, DoctorCheck{
					Component: "DNS: " + p.Name,
					Status:    "error",
					Detail:    err.Error(),
					Hint:      "请检查 API Token 权限或网络连接",
				})
			} else {
				checks = append(checks, DoctorCheck{
					Component: "DNS: " + p.Name,
					Status:    "ok",
					Detail:    fmt.Sprintf("%s，%d 个域名", p.ProviderType, len(zs)),
				})
			}
		}
	}

	// 6. Edge Nodes
	nodes, _ := s.store.ListEdgeNodes(ctx)
	if len(nodes) == 0 {
		checks = append(checks, DoctorCheck{
			Component: "边缘节点",
			Status:    "warn",
			Detail:    "尚未添加",
			Hint:      "运行 vantu node add 或在控制台节点页添加",
		})
	} else {
		for _, n := range nodes {
			if s.prober != nil {
				if updated, pErr := s.prober.ProbeNode(ctx, n.ID); pErr == nil && updated != nil {
					n = *updated
				}
			}
			st := "ok"
			hint := ""
			if n.HealthStatus == "offline" {
				st = "error"
				allHealthy = false
				hint = "无法访问该节点 Caddy Admin API (:2019)"
			} else if n.HealthStatus == "drift" || n.HealthStatus == "degraded" {
				st = "warn"
				hint = "可执行 vantu node reconcile 同步配置"
			}
			checks = append(checks, DoctorCheck{
				Component: "边缘节点: " + n.Name,
				Status:    st,
				Detail:    fmt.Sprintf("%s | 公网 %s | TS %s (%dms)", n.HealthStatus, n.PublicIPv4, n.TailscaleIP, n.TSRTTMs),
				Hint:      hint,
			})
		}
	}

	return &DoctorReport{
		Healthy: allHealthy,
		Checks:  checks,
	}, nil
}

// --- Internal Planning & Helpers ---

func (s *Service) prepareRoutePlan(ctx context.Context, in PublishInput) (store.Route, *store.DNSZone, *store.EdgeNode, error) {
	isSelfTarget := strings.EqualFold(strings.TrimSpace(in.Target), "self") ||
		strings.EqualFold(strings.TrimSpace(in.SourceType), "self")
	if isSelfTarget && strings.TrimSpace(in.Domain) == "" && strings.TrimSpace(in.Subdomain) == "" {
		in.Subdomain = "vantu"
	}

	// 1. Resolve Zone & FullDomain
	zone, sub, fullDomain, err := s.resolveDomainAndZone(ctx, in.Domain, in.Subdomain, in.ZoneID)
	if err != nil {
		return store.Route{}, nil, nil, err
	}

	// Check if route already exists for this FullDomain (idempotent update!) or explicit RouteID.
	var existing *store.Route
	if in.RouteID != "" {
		existing, _ = s.store.GetRoute(ctx, in.RouteID)
	}
	if existing == nil {
		existing, _ = s.store.GetRoute(ctx, fullDomain)
	}

	// 2. Resolve Edge Node
	nodeKey := strings.TrimSpace(in.EdgeNodeID)
	if nodeKey == "" && existing != nil {
		nodeKey = existing.EdgeNodeID
	}
	node, err := s.store.GetEdgeNode(ctx, nodeKey)
	if errors.Is(err, store.ErrNotFound) {
		return store.Route{}, nil, nil, newAppErr("VALIDATION_ERROR",
			"尚未配置可用的边缘公网节点",
			"请先添加至少一台边缘公网 VPS 节点 (vantu node add)", 422, 2)
	}
	if err != nil {
		return store.Route{}, nil, nil, err
	}

	// 3. Resolve Target (Self Console vs Docker container vs Manual IP:Port)
	sourceType, containerName, containerID, scheme, localHost, localPort, autoMode, autoPreset, err := s.resolveTarget(ctx, in)
	if err != nil {
		return store.Route{}, nil, nil, err
	}

	// Security guard: never expose Vantu's own uninitialized setup endpoint to the public internet!
	selfPort := s.selfListenPort()
	exposingSelf := sourceType == "self" ||
		(localPort == selfPort && (localHost == "127.0.0.1" || localHost == "localhost" || localHost == "0.0.0.0" || localHost == "::1"))
	if !in.DryRun && exposingSelf {
		initOK, iErr := s.store.IsInitialized(ctx)
		if iErr != nil {
			return store.Route{}, nil, nil, iErr
		}
		if !initOK {
			return store.Route{}, nil, nil, newAppErr("VALIDATION_ERROR",
				"将自身控制台发布到公网前，必须先设置管理员密码",
				"请先运行 xft admin passwd --generate（或 --password <密码>）完成初始化", 422, 2)
		}
	}

	preset := strings.ToLower(strings.TrimSpace(in.Preset))
	if preset == "" {
		if existing != nil && existing.Preset != "" {
			preset = existing.Preset
		} else if autoPreset != "" {
			preset = autoPreset
		} else {
			preset = "web"
		}
	}
	switch preset {
	case "web", "sse", "webdav", "https_insecure":
	default:
		return store.Route{}, nil, nil, newAppErr("VALIDATION_ERROR",
			fmt.Sprintf("不支持的协议预设 %q（可选: web, sse, webdav, https_insecure）", preset), "", 400, 2)
	}
	if preset == "https_insecure" {
		scheme = "https"
	}

	forwardMode := strings.ToLower(strings.TrimSpace(in.ForwardMode))
	if forwardMode == "" || forwardMode == "auto" {
		forwardMode = autoMode
	}
	if forwardMode != "direct" && forwardMode != "relay" {
		return store.Route{}, nil, nil, newAppErr("VALIDATION_ERROR", "forward_mode 仅支持 auto, direct 或 relay", "", 400, 2)
	}

	// 4. Compute EffectiveDial & Relay HMAC
	routeID := ulid.New()
	dnsRecordID := ""
	createdAt := time.Now().UTC()
	originCertDays := -1
	publicLatencyMs := -1
	if existing != nil {
		routeID = existing.ID
		dnsRecordID = existing.DNSRecordID
		createdAt = existing.CreatedAt
		originCertDays = existing.OriginCertDays
		publicLatencyMs = existing.PublicLatencyMs
	}

	localReachableIP := s.resolveLocalReachableIP(ctx)
	var effectiveDial string
	var relayHMAC string

	if forwardMode == "direct" {
		dialHost := localReachableIP
		if addr, pErr := netip.ParseAddr(localHost); pErr == nil && netip.MustParsePrefix("100.64.0.0/10").Contains(addr) {
			dialHost = localHost
		}
		effectiveDial = net.JoinHostPort(dialHost, strconv.Itoa(localPort))
	} else {
		relayPort := s.cfg.RelayPort
		if s.relay != nil && s.relay.BoundPort() > 0 {
			relayPort = s.relay.BoundPort()
		}
		effectiveDial = net.JoinHostPort(localReachableIP, strconv.Itoa(relayPort))
		relayHMAC = s.vault.ComputeRelayKey(routeID)
	}

	readTimeout := in.ReadTimeoutSec
	if readTimeout <= 0 {
		switch preset {
		case "sse":
			readTimeout = 600
		case "webdav":
			readTimeout = 3600
		default:
			readTimeout = 60
		}
	}

	proxied := in.DNSProxied
	if proxied && !zone.SupportsProxy {
		proxied = false
	}

	rt := store.Route{
		ID:               routeID,
		Subdomain:        sub,
		FullDomain:       fullDomain,
		ZoneID:           zone.ID,
		ZoneName:         zone.ZoneName,
		SupportsProxy:    zone.SupportsProxy,
		EdgeNodeID:       node.ID,
		EdgeNodeName:     node.Name,
		EdgeNodePublicIP: node.PublicIPv4,
		EdgeNodeTSRTTMs:  node.TSRTTMs,
		EdgeNodeTSType:   node.TSConnType,
		SourceType:       sourceType,
		ContainerName:    containerName,
		ContainerID:      containerID,
		ForwardMode:      forwardMode,
		UpstreamScheme:   scheme,
		LocalTargetHost:  localHost,
		LocalTargetPort:  localPort,
		EffectiveDial:    effectiveDial,
		RelayTokenHMAC:   relayHMAC,
		Preset:           preset,
		MaxBodyMB:        in.MaxBodyMB,
		ReadTimeoutSec:   readTimeout,
		DNSRecordID:      dnsRecordID,
		DNSProxied:       proxied,
		Enabled:          true,
		LifecycleState:   "planned",
		OriginCertDays:   originCertDays,
		PublicLatencyMs:  publicLatencyMs,
		Note:             strings.TrimSpace(in.Note),
		CreatedAt:        createdAt,
	}

	return rt, zone, node, nil
}

func (s *Service) resolveDomainAndZone(ctx context.Context, rawDomain, rawSub, zoneID string) (*store.DNSZone, string, string, error) {
	zones, err := s.store.ListDNSZones(ctx)
	if err != nil {
		return nil, "", "", err
	}
	if len(zones) == 0 {
		return nil, "", "", newAppErr("VALIDATION_ERROR",
			"尚未绑定任何可用域名 (DNS Zone)",
			"请先添加 Cloudflare 或 DNSPod 解析凭据 (vantu dns add-cf)", 422, 2)
	}

	rawDomain = strings.ToLower(strings.TrimSpace(rawDomain))
	rawDomain = strings.TrimPrefix(strings.TrimPrefix(rawDomain, "https://"), "http://")
	rawDomain = strings.TrimSuffix(rawDomain, "/")
	rawSub = strings.ToLower(strings.TrimSpace(rawSub))

	var pickedZone *store.DNSZone
	if zoneID != "" {
		for i := range zones {
			if zones[i].ID == zoneID || zones[i].ZoneName == strings.ToLower(zoneID) {
				pickedZone = &zones[i]
				break
			}
		}
		if pickedZone == nil {
			return nil, "", "", newAppErr("NOT_FOUND", fmt.Sprintf("指定的域名 Zone %q 不存在", zoneID), "", 404, 2)
		}
	}

	// Match full domain suffix against known zones if rawDomain contains a dot.
	if pickedZone == nil && strings.Contains(rawDomain, ".") {
		bestLen := 0
		for i := range zones {
			zn := zones[i].ZoneName
			if rawDomain == zn || strings.HasSuffix(rawDomain, "."+zn) {
				if len(zn) > bestLen {
					bestLen = len(zn)
					pickedZone = &zones[i]
				}
			}
		}
		if pickedZone == nil {
			return nil, "", "", newAppErr("VALIDATION_ERROR",
				fmt.Sprintf("域名 %q 不属于当前已接入的任何根域名 Zone", rawDomain),
				"请确认根域名已在『域名与凭据』中同步", 400, 2)
		}
	}

	// Default zone fallback when user passes only a subdomain prefix like "api" or "@".
	if pickedZone == nil {
		for i := range zones {
			if zones[i].IsDefault {
				pickedZone = &zones[i]
				break
			}
		}
		if pickedZone == nil {
			pickedZone = &zones[0]
		}
	}

	sub := rawSub
	if sub == "" {
		switch {
		case rawDomain == "" || rawDomain == "@" || rawDomain == pickedZone.ZoneName:
			sub = "@"
		case strings.HasSuffix(rawDomain, "."+pickedZone.ZoneName):
			sub = strings.TrimSuffix(rawDomain, "."+pickedZone.ZoneName)
		default:
			sub = rawDomain
		}
	}

	if sub == "" {
		return nil, "", "", newAppErr("VALIDATION_ERROR", "子域名前缀不能为空（根域名请填写 @）", "", 400, 2)
	}
	if strings.ContainsAny(sub, " /:\\") {
		return nil, "", "", newAppErr("VALIDATION_ERROR", fmt.Sprintf("非法的子域名前缀 %q", sub), "", 400, 2)
	}

	fullDomain := pickedZone.ZoneName
	if sub != "@" {
		fullDomain = sub + "." + pickedZone.ZoneName
	}

	return pickedZone, sub, fullDomain, nil
}

func (s *Service) resolveTarget(ctx context.Context, in PublishInput) (
	sourceType, containerName, containerID, scheme, localHost string,
	localPort int,
	autoMode, autoPreset string,
	err error,
) {
	scheme = strings.ToLower(strings.TrimSpace(in.UpstreamScheme))
	if scheme == "" {
		scheme = "http"
	}

	target := strings.TrimSpace(in.Target)
	if strings.EqualFold(target, "self") || strings.EqualFold(strings.TrimSpace(in.SourceType), "self") {
		p := s.selfListenPort()
		mode := s.inferManualMode(ctx, "127.0.0.1", p)
		return "self", "", "", "http", "127.0.0.1", p, mode, "sse", nil
	}
	if in.ContainerName != "" && target == "" {
		target = "container:" + strings.TrimSpace(in.ContainerName)
	}

	// Case A: Explicit LocalHost + LocalPort fields from Web UI manual form.
	if target == "" && in.LocalHost != "" && in.LocalPort > 0 {
		h := strings.TrimSpace(in.LocalHost)
		if strings.HasPrefix(h, "https://") {
			scheme = "https"
			h = strings.TrimPrefix(h, "https://")
		} else if strings.HasPrefix(h, "http://") {
			scheme = "http"
			h = strings.TrimPrefix(h, "http://")
		}
		h = strings.TrimSuffix(h, "/")
		mode := s.inferManualMode(ctx, h, in.LocalPort)
		preset := "web"
		if scheme == "https" {
			preset = "https_insecure"
		}
		return "manual", "", "", scheme, h, in.LocalPort, mode, preset, nil
	}

	if target == "" {
		return "", "", "", "", "", 0, "", "", newAppErr("VALIDATION_ERROR",
			"请指定要发布的本地容器或 IP:Port 目标",
			"例如: container:sub2api 或 127.0.0.1:8080", 400, 2)
	}

	// Case B: Docker container target ("container:sub2api" or bare container name without ':').
	isContainerRef := strings.HasPrefix(target, "container:")
	if !isContainerRef && !strings.Contains(target, ":") && !strings.Contains(target, ".") {
		isContainerRef = true
	}

	if isContainerRef {
		cName := strings.TrimPrefix(target, "container:")
		// Support optional port suffix "container:sub2api:8080"
		explicitPort := in.LocalPort
		if idx := strings.LastIndex(cName, ":"); idx > 0 {
			if p, pErr := strconv.Atoi(cName[idx+1:]); pErr == nil && p > 0 {
				explicitPort = p
				cName = cName[:idx]
			}
		}

		containers, ok, _ := s.ListContainers(ctx)
		if !ok {
			return "", "", "", "", "", 0, "", "", newAppErr("VALIDATION_ERROR",
				fmt.Sprintf("当前环境未连接 Docker Socket，无法解析容器 %q", cName),
				"请改用 IP:Port 手动模式，或挂载 /var/run/docker.sock", 422, 2)
		}
		for _, c := range containers {
			if c.Name == cName || c.ID == cName {
				pickedHost := c.RecommendedHost
				pickedPort := c.RecommendedPort
				pickedMode := c.RecommendedMode
				if explicitPort > 0 {
					pickedPort = explicitPort
					for _, dp := range c.Ports {
						if dp.PrivatePort == explicitPort || dp.PublicPort == explicitPort {
							pickedHost = dp.TargetHost
							pickedPort = dp.TargetPort
							pickedMode = dp.RecommendedMode
							break
						}
					}
				}
				return "docker", c.Name, c.ID, scheme, pickedHost, pickedPort, pickedMode, c.SuggestedPreset, nil
			}
		}
		return "", "", "", "", "", 0, "", "", newAppErr("NOT_FOUND",
			fmt.Sprintf("未找到运行中的 Docker 容器 %q", cName),
			"请运行 vantu docker ls 查看当前活跃容器列表", 404, 2)
	}

	// Case C: URL or Host:Port manual string ("https://192.168.1.2:8006", "127.0.0.1:8080", ":8080")
	if strings.HasPrefix(target, "http://") || strings.HasPrefix(target, "https://") {
		u, uErr := url.Parse(target)
		if uErr != nil {
			return "", "", "", "", "", 0, "", "", newAppErr("VALIDATION_ERROR", fmt.Sprintf("无效的目标地址 %q", target), "", 400, 2)
		}
		scheme = u.Scheme
		target = u.Host
	}
	if strings.HasPrefix(target, ":") {
		target = "127.0.0.1" + target
	}
	h, portStr, splitErr := net.SplitHostPort(target)
	if splitErr != nil {
		return "", "", "", "", "", 0, "", "", newAppErr("VALIDATION_ERROR",
			fmt.Sprintf("目标地址 %q 缺少端口号", target),
			"请使用 IP:Port 格式，例如 127.0.0.1:8080", 400, 2)
	}
	p, pErr := strconv.Atoi(portStr)
	if pErr != nil || p <= 0 || p > 65535 {
		return "", "", "", "", "", 0, "", "", newAppErr("VALIDATION_ERROR", fmt.Sprintf("端口号 %q 无效", portStr), "", 400, 2)
	}
	mode := s.inferManualMode(ctx, h, p)
	preset := "web"
	if scheme == "https" {
		preset = "https_insecure"
	}
	return "manual", "", "", scheme, h, p, mode, preset, nil
}

func (s *Service) inferManualMode(ctx context.Context, host string, port int) string {
	// If target is already on Tailscale CGNAT IP, Direct mode can reach it directly.
	if addr, err := netip.ParseAddr(host); err == nil && netip.MustParsePrefix("100.64.0.0/10").Contains(addr) {
		return "direct"
	}
	// If target is loopback (127.0.0.1 / localhost), check whether the service is ALSO bound on our local Tailscale IP (0.0.0.0).
	if host == "127.0.0.1" || host == "localhost" || host == "::1" {
		if s.tsInspector != nil {
			ts := s.tsInspector.SelfStatus(ctx)
			if ts.IPv4 != "" && ts.IPv4 != "127.0.0.1" {
				d := net.Dialer{Timeout: 800 * time.Millisecond}
				if c, err := d.DialContext(ctx, "tcp", net.JoinHostPort(ts.IPv4, strconv.Itoa(port))); err == nil {
					_ = c.Close()
					return "direct"
				}
			}
		}
		return "relay"
	}
	// Private LAN targets (e.g. NAS 192.168.1.10:5000) are seamlessly bridged via Local Relay without needing Tailscale subnet routers!
	return "relay"
}

func (s *Service) resolveLocalReachableIP(ctx context.Context) string {
	if s.tsInspector != nil {
		ts := s.tsInspector.SelfStatus(ctx)
		if ts.IPv4 != "" {
			return ts.IPv4
		}
	}
	if s.relay != nil && s.relay.BoundAddr() != "" {
		if h, _, err := net.SplitHostPort(s.relay.BoundAddr()); err == nil && h != "" && h != "0.0.0.0" && h != "::" {
			return h
		}
	}
	return "127.0.0.1"
}

func (s *Service) routeToCaddySpec(rt store.Route) caddy.RouteSpec {
	hmacKey := rt.RelayTokenHMAC
	if rt.ForwardMode == "relay" && hmacKey == "" {
		hmacKey = s.vault.ComputeRelayKey(rt.ID)
	}
	return caddy.RouteSpec{
		RouteID:        rt.ID,
		FullDomain:     rt.FullDomain,
		EffectiveDial:  rt.EffectiveDial,
		ForwardMode:    rt.ForwardMode,
		UpstreamScheme: rt.UpstreamScheme,
		Preset:         rt.Preset,
		MaxBodyMB:      rt.MaxBodyMB,
		ReadTimeoutSec: rt.ReadTimeoutSec,
		RelayKeyHMAC:   hmacKey,
	}
}

func (s *Service) buildDNSDriver(rec *store.DNSProviderRecord) (dns.Provider, error) {
	dec, err := s.vault.Decrypt(rec.CredentialsEnc)
	if err != nil {
		return nil, fmt.Errorf("decrypt dns credentials: %w", err)
	}
	return dns.Build(rec.ProviderType, dec)
}

func (s *Service) selfListenPort() int {
	_, portStr, err := net.SplitHostPort(strings.TrimSpace(s.cfg.ListenAddr))
	if err == nil {
		if p, pErr := strconv.Atoi(portStr); pErr == nil && p > 0 {
			return p
		}
	}
	return 8089
}

func buildCLICommand(rt store.Route) string {
	var parts []string
	parts = append(parts, "vantu publish")
	switch {
	case rt.SourceType == "self":
		parts = append(parts, "--target self")
	case rt.SourceType == "docker" && rt.ContainerName != "":
		parts = append(parts, fmt.Sprintf("--target container:%s", rt.ContainerName))
	default:
		prefix := ""
		if rt.UpstreamScheme == "https" {
			prefix = "https://"
		}
		parts = append(parts, fmt.Sprintf("--target %s%s:%d", prefix, rt.LocalTargetHost, rt.LocalTargetPort))
	}
	parts = append(parts, fmt.Sprintf("--domain %s", rt.FullDomain))
	if rt.EdgeNodeName != "" {
		parts = append(parts, fmt.Sprintf("--node %s", rt.EdgeNodeName))
	}
	if rt.Preset != "" && rt.Preset != "web" {
		parts = append(parts, fmt.Sprintf("--preset %s", rt.Preset))
	}
	if rt.ForwardMode == "relay" {
		parts = append(parts, "--mode relay")
	}
	if rt.DNSProxied {
		parts = append(parts, "--proxied")
	}
	return strings.Join(parts, " ")
}
