import { locale } from './i18n'

export interface AuthStatus {
  initialized: boolean
  authenticated: boolean
  actor: string
  scope: string
  version: string
}

export interface UpdateInfo {
  current_version: string
  latest_version: string
  has_update: boolean
  release_url: string
  release_notes: string
  published_at?: string
  checked_at?: string
  check_error?: string
  install_mode: 'binary' | 'docker'
  can_self_update: boolean
  self_update_note?: string
  upgrade_command?: string
}

export interface DiscoveredPort {
  private_port: number
  public_port?: number
  bind_ip?: string
  protocol: string
  target_host: string
  target_port: number
  recommended_mode: 'direct' | 'relay'
  reachability: string
  reason: string
}

export interface DiscoveredContainer {
  id: string
  name: string
  image: string
  state: string
  status: string
  network_mode: string
  bridge_ip?: string
  ports: DiscoveredPort[]
  recommended_mode: 'direct' | 'relay'
  recommended_port: number
  recommended_host: string
  suggested_preset: 'web' | 'sse' | 'webdav' | 'https_insecure'
  suggested_sub: string
  mode_reason: string
  published_domains: string[]
}

export interface DNSProviderRecord {
  id: string
  name: string
  provider_type: 'cloudflare' | 'dnspod'
  masked_cred: string
  status: string
  created_at: string
}

export interface DNSZone {
  id: string
  provider_id: string
  provider_name: string
  provider_type: 'cloudflare' | 'dnspod'
  zone_name: string
  remote_zone_id: string
  supports_proxy: boolean
  is_default: boolean
  route_count: number
  synced_at: string
}

export interface EdgeNode {
  id: string
  name: string
  region: string
  public_ipv4: string
  public_ipv6: string
  tailscale_ip: string
  caddy_admin_url: string
  masked_secret: string
  is_default: boolean
  enabled: boolean
  health_status: 'online' | 'degraded' | 'offline' | 'drift' | 'unknown'
  ts_conn_type: 'direct' | 'derp' | 'offline' | 'unknown'
  ts_derp_region: string
  ts_rtt_ms: number
  caddy_version: string
  route_count: number
  last_seen_at?: string
}

export interface RouteRecord {
  id: string
  subdomain: string
  full_domain: string
  zone_id: string
  zone_name: string
  supports_proxy: boolean
  edge_node_id: string
  edge_node_name: string
  edge_node_public_ip: string
  edge_node_ts_rtt_ms: number
  edge_node_ts_type: string
  source_type: 'docker' | 'manual' | 'self'
  container_name: string
  container_id: string
  forward_mode: 'direct' | 'relay'
  upstream_scheme: 'http' | 'https'
  local_target_host: string
  local_target_port: number
  effective_dial: string
  preset: 'web' | 'sse' | 'webdav' | 'https_insecure'
  max_body_mb: number
  read_timeout_sec: number
  dns_record_id: string
  dns_proxied: boolean
  enabled: boolean
  lifecycle_state: string
  last_error: string
  local_healthy: boolean
  caddy_synced: boolean
  origin_tls_ok: boolean
  origin_cert_issuer: string
  origin_cert_expires?: string
  origin_cert_days: number
  public_cert_issuer: string
  public_http_code: number
  public_latency_ms: number
  last_probed_at?: string
  note: string
  basic_auth_user: string
}

export interface ProbeSample {
  id: number
  route_id: string
  is_ok: boolean
  ts_rtt_ms: number
  public_latency_ms: number
  public_http_code: number
  fail_stage: string
  checked_at: string
}

export interface RouteDetail {
  route: RouteRecord
  history: ProbeSample[]
  caddy_payload: Record<string, unknown>
  cli_command: string
}

export interface APITokenRecord {
  id: string
  name: string
  token_prefix: string
  scope: 'admin' | 'readonly'
  last_used_at?: string
  created_at: string
}

export interface AuditEvent {
  id: number
  severity: 'info' | 'warn' | 'error'
  category: string
  target_id: string
  target_label: string
  message: string
  actor: string
  created_at: string
}

export interface DashboardData {
  capabilities: {
    version: string
    initialized: boolean
    db_size_bytes: number
    docker_available: boolean
    container_count: number
    unexposed_containers: number
    tailscale: {
      available: boolean
      backend_state: string
      ipv4: string
      hostname: string
      source: string
    }
    relay_running: boolean
    relay_addr: string
  }
  routes: RouteRecord[]
  containers: DiscoveredContainer[]
  nodes: EdgeNode[]
  zones: DNSZone[]
  providers: DNSProviderRecord[]
  tokens: APITokenRecord[]
  events: AuditEvent[]
  summary: {
    active_routes: number
    total_routes: number
    unexposed_containers: number
    container_count: number
    nodes_online: number
    nodes_total: number
    min_cert_days: number
    min_cert_domain: string
    degraded_count: number
    drift_count: number
  }
}

export interface PublishPayload {
  route_id?: string
  target?: string
  source_type?: string
  container_name?: string
  upstream_scheme?: string
  local_host?: string
  local_port?: number
  domain?: string
  subdomain?: string
  zone_id?: string
  edge_node_id?: string
  forward_mode?: string
  preset?: string
  dns_proxied?: boolean
  adopt_dns?: boolean
  note?: string
  basic_auth_user?: string
  basic_auth_password?: string
  skip_local_check?: boolean
  dry_run?: boolean
}

export interface StepProgress {
  step: string
  title: string
  status: 'planned' | 'running' | 'done' | 'error'
  detail: string
  domain?: string
}

export interface PublishResponse {
  dry_run: boolean
  route: RouteRecord
  steps: StepProgress[]
  caddy_payload: Record<string, unknown>
  cli_command: string
}

export interface BootstrapBundle {
  node_name: string
  public_ip: string
  tailscale_ip: string
  auth_secret: string
  docker_compose: string
  bootstrap_json: string
  quick_command: string
}

export interface DoctorCheck {
  component: string
  status: 'ok' | 'warn' | 'error'
  detail: string
  hint?: string
}

export class APIRequestError extends Error {
  code: string
  suggestion?: string

  constructor(code: string, message: string, suggestion?: string) {
    super(message)
    this.code = code
    this.suggestion = suggestion
  }
}

async function request<T>(path: string, init?: RequestInit): Promise<T> {
  const headers: Record<string, string> = {
    'X-Vantu-Lang': locale.value,
    ...(init?.headers as Record<string, string> | undefined),
  }
  if (init?.body && !headers['Content-Type']) {
    headers['Content-Type'] = 'application/json'
  }
  const resp = await fetch(`/api/v1${path}`, {
    ...init,
    credentials: 'same-origin',
    headers,
  })
  const json = await resp.json()
  if (!json.ok) {
    const code = json.error?.code || 'INTERNAL_ERROR'
    const msg = json.error?.message || `HTTP ${resp.status}`
    const sug = json.error?.suggestion
    throw new APIRequestError(code, msg, sug)
  }
  return json.data as T
}

export const api = {
  getAuthStatus: () => request<AuthStatus>('/auth/status'),
  setupAdmin: (password: string) =>
    request<{ session_token: string }>('/auth/setup', {
      method: 'POST',
      body: JSON.stringify({ password }),
    }),
  login: (password: string) =>
    request<{ session_token: string }>('/auth/login', {
      method: 'POST',
      body: JSON.stringify({ password }),
    }),
  logout: () => request<{ logged_out: boolean }>('/auth/logout', { method: 'POST' }),
  changePassword: (current_password: string, new_password: string) =>
    request<{ updated: boolean }>('/auth/password', {
      method: 'POST',
      body: JSON.stringify({ current_password, new_password }),
    }),

  getDashboard: () => request<DashboardData>('/dashboard'),
  getDoctor: () => request<{ healthy: boolean; checks: DoctorCheck[] }>('/doctor'),
  getSkill: () => request<{ endpoint: string; skill_md: string }>('/skill'),
  getHealth: () => request<{ status: string; version: string }>('/healthz'),
  getUpdateInfo: (refresh = false) => request<UpdateInfo>(`/update${refresh ? '?refresh=1' : ''}`),
  applyUpdate: () =>
    request<{ updated: boolean; from: string; to: string; restarting: boolean }>('/update/apply', {
      method: 'POST',
    }),

  publishRoute: (payload: PublishPayload) =>
    request<PublishResponse>('/routes', {
      method: 'POST',
      body: JSON.stringify(payload),
    }),
  getRouteDetail: (id: string) => request<RouteDetail>(`/routes/${encodeURIComponent(id)}`),
  probeRoute: (id: string) =>
    request<RouteDetail>(`/routes/${encodeURIComponent(id)}/probe`, { method: 'POST' }),
  toggleProxy: (id: string, proxied: boolean) =>
    request<RouteRecord>(`/routes/${encodeURIComponent(id)}/proxy`, {
      method: 'PATCH',
      body: JSON.stringify({ proxied }),
    }),
  pauseRoute: (id: string) =>
    request<RouteRecord>(`/routes/${encodeURIComponent(id)}/pause`, { method: 'POST' }),
  resumeRoute: (id: string) =>
    request<RouteRecord>(`/routes/${encodeURIComponent(id)}/resume`, { method: 'POST' }),
  retryRoute: (id: string) =>
    request<RouteRecord>(`/routes/${encodeURIComponent(id)}/retry`, { method: 'POST' }),
  setRouteAuth: (id: string, username: string, password: string) =>
    request<RouteRecord>(`/routes/${encodeURIComponent(id)}/auth`, {
      method: 'PUT',
      body: JSON.stringify({ username, password }),
    }),
  deleteRoute: (id: string, deleteDNS = true) =>
    request<{ deleted: string }>(`/routes/${encodeURIComponent(id)}?delete_dns=${deleteDNS}`, {
      method: 'DELETE',
    }),

  previewBootstrap: (payload: {
    name: string
    public_ipv4: string
    tailscale_ip: string
    auth_secret?: string
  }) =>
    request<BootstrapBundle>('/nodes/bootstrap', {
      method: 'POST',
      body: JSON.stringify(payload),
    }),
  createNode: (payload: {
    name: string
    region: string
    public_ipv4: string
    tailscale_ip: string
    caddy_admin_url?: string
    auth_secret: string
    is_default?: boolean
    skip_verify?: boolean
  }) =>
    request<EdgeNode>('/nodes', {
      method: 'POST',
      body: JSON.stringify(payload),
    }),
  setDefaultNode: (id: string) =>
    request<EdgeNode[]>(`/nodes/${encodeURIComponent(id)}/default`, { method: 'PUT' }),
  pingNode: (id: string) =>
    request<EdgeNode>(`/nodes/${encodeURIComponent(id)}/ping`, { method: 'POST' }),
  reconcileNode: (id: string) =>
    request<{ node: EdgeNode; pushed_routes: number }>(
      `/nodes/${encodeURIComponent(id)}/reconcile`,
      { method: 'POST' }
    ),
  deleteNode: (id: string) =>
    request<{ deleted: string }>(`/nodes/${encodeURIComponent(id)}`, { method: 'DELETE' }),

  createDNSProvider: (payload: {
    name: string
    provider_type: 'cloudflare' | 'dnspod'
    api_token?: string
    secret_id?: string
    secret_key?: string
    base_url?: string
  }) =>
    request<{ provider: DNSProviderRecord; zones: DNSZone[] }>('/dns/providers', {
      method: 'POST',
      body: JSON.stringify(payload),
    }),
  syncDNSProvider: (id: string) =>
    request<{ zones: DNSZone[] }>(`/dns/providers/${encodeURIComponent(id)}/sync`, {
      method: 'POST',
    }),
  deleteDNSProvider: (id: string) =>
    request<{ deleted: string }>(`/dns/providers/${encodeURIComponent(id)}`, {
      method: 'DELETE',
    }),
  setDefaultZone: (id: string) =>
    request<DNSZone[]>(`/dns/zones/${encodeURIComponent(id)}/default`, { method: 'PUT' }),

  createToken: (name: string, scope: 'admin' | 'readonly') =>
    request<{ token: APITokenRecord; plain_text: string }>('/tokens', {
      method: 'POST',
      body: JSON.stringify({ name, scope }),
    }),
  deleteToken: (id: string) =>
    request<{ deleted: string }>(`/tokens/${encodeURIComponent(id)}`, { method: 'DELETE' }),
}
