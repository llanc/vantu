import type { ProxyOptions, RouteRecord } from './api'

// AdvancedForm is the editable, text-friendly shape of the advanced proxy options.
export interface AdvancedForm {
  maxBodyMB: number | ''
  readTimeout: number | ''
  upstreamHost: string
  tlsSkipVerify: boolean
  compress: boolean
  ipAllow: string
  reqHeaders: string
  respHeaders: string
}

export function emptyAdvanced(): AdvancedForm {
  return {
    maxBodyMB: '',
    readTimeout: '',
    upstreamHost: '',
    tlsSkipVerify: false,
    compress: false,
    ipAllow: '',
    reqHeaders: '',
    respHeaders: '',
  }
}

export function defaultReadTimeout(preset: string): number {
  if (preset === 'sse') return 600
  if (preset === 'webdav') return 3600
  return 60
}

function headersToText(h?: Record<string, string>): string {
  return Object.entries(h ?? {})
    .sort(([a], [b]) => a.localeCompare(b))
    .map(([k, v]) => `${k}: ${v}`)
    .join('\n')
}

export function advancedFromRoute(r: RouteRecord): AdvancedForm {
  const p = r.proxy ?? {}
  return {
    maxBodyMB: r.max_body_mb > 0 ? r.max_body_mb : '',
    readTimeout: r.read_timeout_sec > 0 && r.read_timeout_sec !== defaultReadTimeout(r.preset) ? r.read_timeout_sec : '',
    upstreamHost: p.upstream_host ?? '',
    tlsSkipVerify: !!p.tls_skip_verify,
    compress: !!p.compress,
    ipAllow: (p.ip_allowlist ?? []).join('\n'),
    reqHeaders: headersToText(p.request_headers),
    respHeaders: headersToText(p.response_headers),
  }
}

function lines(text: string): string[] {
  return text
    .split(/[\n,]/)
    .map((s) => s.trim())
    .filter(Boolean)
}

// parseHeaders returns the header map, or the first malformed line.
function parseHeaders(text: string): { headers: Record<string, string>; bad?: string } {
  const headers: Record<string, string> = {}
  for (const raw of text.split('\n')) {
    const line = raw.trim()
    if (!line) continue
    const idx = line.indexOf(':')
    if (idx <= 0) return { headers, bad: line }
    headers[line.slice(0, idx).trim()] = line.slice(idx + 1).trim()
  }
  return { headers }
}

export interface AdvancedPayload {
  proxy: ProxyOptions
  max_body_mb: number
  read_timeout_sec: number
}

// toAdvancedPayload converts the form for the API; badLine is set when a header line is malformed.
export function toAdvancedPayload(f: AdvancedForm): { payload: AdvancedPayload; badLine?: string } {
  const req = parseHeaders(f.reqHeaders)
  const resp = parseHeaders(f.respHeaders)
  const payload: AdvancedPayload = {
    max_body_mb: Number(f.maxBodyMB) || 0,
    read_timeout_sec: Number(f.readTimeout) || 0,
    proxy: {
      upstream_host: f.upstreamHost.trim() || undefined,
      tls_skip_verify: f.tlsSkipVerify || undefined,
      compress: f.compress || undefined,
      ip_allowlist: lines(f.ipAllow),
      request_headers: req.headers,
      response_headers: resp.headers,
    },
  }
  return { payload, badLine: req.bad ?? resp.bad }
}

export function advancedCount(f: AdvancedForm): number {
  return [
    Number(f.maxBodyMB) > 0,
    Number(f.readTimeout) > 0,
    !!f.upstreamHost.trim(),
    f.tlsSkipVerify,
    f.compress,
    lines(f.ipAllow).length > 0,
    !!f.reqHeaders.trim(),
    !!f.respHeaders.trim(),
  ].filter(Boolean).length
}

function shellQuote(s: string): string {
  return /^[A-Za-z0-9._:/@%+=,-]+$/.test(s) ? s : `'${s.replace(/'/g, `'\\''`)}'`
}

export function advancedCLIFlags(f: AdvancedForm): string[] {
  const { payload } = toAdvancedPayload(f)
  const p = payload.proxy
  const out: string[] = []
  if (payload.max_body_mb > 0) out.push(`--max-body-mb ${payload.max_body_mb}`)
  if (payload.read_timeout_sec > 0) out.push(`--read-timeout ${payload.read_timeout_sec}`)
  if (p.upstream_host) out.push(`--upstream-host ${shellQuote(p.upstream_host)}`)
  if (p.tls_skip_verify) out.push('--insecure-skip-verify')
  if (p.compress) out.push('--compress')
  for (const ip of p.ip_allowlist ?? []) out.push(`--allow-ip ${ip}`)
  for (const [k, v] of Object.entries(p.request_headers ?? {})) out.push(`--req-header ${shellQuote(`${k}: ${v}`)}`)
  for (const [k, v] of Object.entries(p.response_headers ?? {})) out.push(`--resp-header ${shellQuote(`${k}: ${v}`)}`)
  return out
}
