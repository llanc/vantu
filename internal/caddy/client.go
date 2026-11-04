package caddy

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const (
	ServerBlockName = "vantu_srv"
	RouteIDPrefix   = "vantu-route-"
)

// RouteSpec defines the inputs needed to build a dynamic Caddy v2 reverse_proxy route.
type RouteSpec struct {
	RouteID        string
	FullDomain     string
	EffectiveDial  string // e.g. "100.103.81.50:8080" or "100.103.81.50:18080"
	ForwardMode    string // "direct" | "relay"
	UpstreamScheme string // "http" | "https"
	Preset         string // "web" | "sse" | "webdav" | "https_insecure"
	MaxBodyMB      int
	ReadTimeoutSec int
	RelayKeyHMAC   string
}

// BootstrapBundle contains ready-to-deploy edge node configuration files.
type BootstrapBundle struct {
	NodeName      string `json:"node_name"`
	PublicIP      string `json:"public_ip"`
	TailscaleIP   string `json:"tailscale_ip"`
	AuthSecret    string `json:"auth_secret"`
	DockerCompose string `json:"docker_compose"`
	BootstrapJSON string `json:"bootstrap_json"`
	QuickCommand  string `json:"quick_command"`
}

// Client manages an Edge Node's Caddy Admin API over Tailscale.
type Client struct {
	adminURL   string
	authSecret string
	httpClient *http.Client
}

func NewClient(adminURL, authSecret string) *Client {
	return &Client{
		adminURL:   strings.TrimRight(strings.TrimSpace(adminURL), "/"),
		authSecret: strings.TrimSpace(authSecret),
		httpClient: &http.Client{Timeout: 8 * time.Second},
	}
}

// Ping verifies Caddy Admin API connectivity and authentication.
func (c *Client) Ping(ctx context.Context) (string, error) {
	status, raw, hdr, err := c.do(ctx, http.MethodGet, "/config/", nil)
	if err != nil {
		return "", err
	}
	if status == http.StatusUnauthorized || status == http.StatusForbidden {
		return "", fmt.Errorf("边缘 Caddy 拒绝访问 (HTTP %d)，请核对节点 Auth Secret 密钥", status)
	}
	if status >= 300 {
		return "", fmt.Errorf("caddy admin status %d: %s", status, string(raw))
	}
	ver := hdr.Get("Server")
	if ver == "" {
		ver = "Caddy v2"
	}
	return ver, nil
}

// EnsureServerBlock ensures /config/apps/http/servers/vantu_srv exists without wiping other server blocks.
func (c *Client) EnsureServerBlock(ctx context.Context) error {
	status, raw, _, err := c.do(ctx, http.MethodGet, "/config/apps/http/servers/"+ServerBlockName, nil)
	if err != nil {
		return err
	}
	if status == http.StatusOK && strings.TrimSpace(string(raw)) != "null" && len(bytes.TrimSpace(raw)) > 2 {
		return nil
	}

	srvPayload := map[string]any{
		"listen": []string{":80", ":443"},
		"routes": []any{},
	}

	// Ensure parent hierarchy /config/apps/http/servers exists when starting from an empty "{}" Caddy state.
	stApps, rawApps, _, err := c.do(ctx, http.MethodGet, "/config/apps", nil)
	if err != nil {
		return err
	}
	if stApps != http.StatusOK || strings.TrimSpace(string(rawApps)) == "null" {
		full := map[string]any{
			"http": map[string]any{
				"servers": map[string]any{
					ServerBlockName: srvPayload,
				},
			},
		}
		st, body, _, err := c.do(ctx, http.MethodPut, "/config/apps", full)
		if err != nil {
			return err
		}
		if st >= 300 {
			// Fallback: initialize root /config/ if even /config/ wasn't an object yet.
			root := map[string]any{"apps": full}
			st2, body2, _, err2 := c.do(ctx, http.MethodPost, "/load", root)
			if err2 != nil {
				return err2
			}
			if st2 >= 300 {
				return fmt.Errorf("init caddy config failed (%d / %d): %s %s", st, st2, string(body), string(body2))
			}
		}
		return nil
	}

	stHTTP, rawHTTP, _, _ := c.do(ctx, http.MethodGet, "/config/apps/http", nil)
	if stHTTP != http.StatusOK || strings.TrimSpace(string(rawHTTP)) == "null" {
		httpApp := map[string]any{
			"servers": map[string]any{
				ServerBlockName: srvPayload,
			},
		}
		st, body, _, err := c.do(ctx, http.MethodPut, "/config/apps/http", httpApp)
		if err != nil {
			return err
		}
		if st >= 300 {
			return fmt.Errorf("init caddy http app (%d): %s", st, string(body))
		}
		return nil
	}

	st, body, _, err := c.do(ctx, http.MethodPut, "/config/apps/http/servers/"+ServerBlockName, srvPayload)
	if err != nil {
		return err
	}
	if st >= 300 {
		return fmt.Errorf("create %s block (%d): %s", ServerBlockName, st, string(body))
	}
	return nil
}

// UpsertRoute atomically updates an existing @id route via PATCH, or appends a new route to vantu_srv.
func (c *Client) UpsertRoute(ctx context.Context, spec RouteSpec) error {
	if err := c.EnsureServerBlock(ctx); err != nil {
		return err
	}

	payload := BuildRoutePayload(spec)
	caddyID := RouteIDPrefix + spec.RouteID

	exists, err := c.HasRoute(ctx, spec.RouteID)
	if err != nil {
		return err
	}
	if exists {
		st, body, _, err := c.do(ctx, http.MethodPatch, "/id/"+caddyID, payload)
		if err != nil {
			return err
		}
		if st >= 300 {
			return fmt.Errorf("patch caddy route %s (%d): %s", caddyID, st, string(body))
		}
		return nil
	}

	st, body, _, err := c.do(ctx, http.MethodPost, "/config/apps/http/servers/"+ServerBlockName+"/routes", payload)
	if err != nil {
		return err
	}
	if st >= 300 {
		return fmt.Errorf("post caddy route %s (%d): %s", caddyID, st, string(body))
	}
	return nil
}

// DeleteRoute removes a route by its @id atomically (idempotent if already absent).
func (c *Client) DeleteRoute(ctx context.Context, routeID string) error {
	caddyID := RouteIDPrefix + routeID
	st, body, _, err := c.do(ctx, http.MethodDelete, "/id/"+caddyID, nil)
	if err != nil {
		return err
	}
	if st == http.StatusNotFound || st == http.StatusBadRequest {
		// Caddy returns 404 or 400 "unknown object ID" when @id does not exist.
		if strings.Contains(strings.ToLower(string(body)), "unknown") || st == http.StatusNotFound {
			return nil
		}
	}
	if st >= 300 {
		return fmt.Errorf("delete caddy route %s (%d): %s", caddyID, st, string(body))
	}
	return nil
}

// HasRoute checks whether @id "vantu-route-<routeID>" is currently active in Edge Caddy memory.
func (c *Client) HasRoute(ctx context.Context, routeID string) (bool, error) {
	caddyID := RouteIDPrefix + routeID
	st, raw, _, err := c.do(ctx, http.MethodGet, "/id/"+caddyID, nil)
	if err != nil {
		return false, err
	}
	if st == http.StatusOK && strings.TrimSpace(string(raw)) != "null" {
		return true, nil
	}
	return false, nil
}

// BuildRoutePayload constructs the exact Caddy v2 JSON object for a single reverse_proxy route.
func BuildRoutePayload(spec RouteSpec) map[string]any {
	readTimeout := spec.ReadTimeoutSec
	if readTimeout <= 0 {
		switch spec.Preset {
		case "sse":
			readTimeout = 600
		case "webdav":
			readTimeout = 3600
		default:
			readTimeout = 60
		}
	}

	transport := map[string]any{
		"protocol":      "http",
		"read_timeout":  fmt.Sprintf("%ds", readTimeout),
		"write_timeout": fmt.Sprintf("%ds", readTimeout),
	}
	// When Direct mode hits an HTTPS self-signed upstream directly (not via Local Relay HTTP wrapper), enable TLS skip verify.
	if spec.ForwardMode == "direct" && (spec.Preset == "https_insecure" || spec.UpstreamScheme == "https") {
		transport["tls"] = map[string]any{
			"insecure_skip_verify": true,
		}
	}

	revProxy := map[string]any{
		"handler": "reverse_proxy",
		"upstreams": []map[string]any{
			{"dial": spec.EffectiveDial},
		},
		"transport": transport,
	}

	if spec.Preset == "sse" {
		revProxy["flush_interval"] = -1
	}

	if spec.ForwardMode == "relay" && spec.RelayKeyHMAC != "" {
		revProxy["headers"] = map[string]any{
			"request": map[string]any{
				"set": map[string][]string{
					"X-Vantu-Route":     {spec.RouteID},
					"X-Vantu-Relay-Key": {spec.RelayKeyHMAC},
				},
			},
		}
	}

	handles := make([]any, 0, 2)
	if spec.MaxBodyMB > 0 {
		handles = append(handles, map[string]any{
			"handler":  "request_body",
			"max_size": int64(spec.MaxBodyMB) * 1024 * 1024,
		})
	}
	handles = append(handles, revProxy)

	return map[string]any{
		"@id": RouteIDPrefix + spec.RouteID,
		"match": []map[string]any{
			{"host": []string{spec.FullDomain}},
		},
		"handle":   handles,
		"terminal": true,
	}
}

// GenerateBootstrapBundle creates zero-agent docker-compose.yml and bootstrap.json with Tailscale + Bearer Secret guard.
func GenerateBootstrapBundle(nodeName, publicIP, tailscaleIP, secret string) BootstrapBundle {
	if tailscaleIP == "" {
		tailscaleIP = "100.x.x.x"
	}
	guardObj := map[string]any{
		"admin": map[string]any{
			"listen": "unix//run/caddy-admin.sock",
			"config": map[string]any{
				"persist": true,
			},
		},
		"apps": map[string]any{
			"http": map[string]any{
				"servers": map[string]any{
					"vantu_admin_guard": map[string]any{
						"listen": []string{tailscaleIP + ":2019"},
						"routes": []any{
							map[string]any{
								"match": []map[string]any{
									{
										"remote_ip": map[string]any{"ranges": []string{"100.64.0.0/10", "127.0.0.0/8"}},
										"header":    map[string][]string{"Authorization": {"Bearer " + secret}},
									},
								},
								"handle": []any{
									map[string]any{
										"handler": "reverse_proxy",
										"upstreams": []map[string]any{
											{"dial": "unix//run/caddy-admin.sock"},
										},
										"headers": map[string]any{
											"request": map[string]any{
												"set": map[string][]string{"Host": {"127.0.0.1"}},
											},
										},
									},
								},
								"terminal": true,
							},
							map[string]any{
								"handle": []any{
									map[string]any{
										"handler":     "static_response",
										"status_code": 401,
										"body":        `{"error":"unauthorized vantu edge admin request"}`,
									},
								},
							},
						},
					},
					ServerBlockName: map[string]any{
						"listen": []string{":80", ":443"},
						"routes": []any{},
					},
				},
			},
		},
	}

	bootBytes, _ := json.MarshalIndent(guardObj, "", "  ")
	compose := fmt.Sprintf(`services:
  caddy:
    image: caddy:2.8-alpine
    container_name: vantu-edge-caddy
    restart: always
    network_mode: host
    volumes:
      - ./caddy_data:/data
      - ./caddy_config:/config
      - ./bootstrap.json:/etc/caddy/bootstrap.json:ro
    command: >
      sh -c 'if [ -f /config/caddy/autosave.json ]; then
        caddy run --resume;
      else
        caddy run --config /etc/caddy/bootstrap.json;
      fi'
`)

	quickCmd := fmt.Sprintf("mkdir -p ~/vantu-edge && cd ~/vantu-edge && cat <<'EOF' > bootstrap.json\n%s\nEOF\ncat <<'EOF' > docker-compose.yml\n%sEOF\ndocker compose up -d",
		string(bootBytes), compose)

	return BootstrapBundle{
		NodeName:      nodeName,
		PublicIP:      publicIP,
		TailscaleIP:   tailscaleIP,
		AuthSecret:    secret,
		DockerCompose: compose,
		BootstrapJSON: string(bootBytes),
		QuickCommand:  quickCmd,
	}
}

func (c *Client) do(ctx context.Context, method, path string, payload any) (int, []byte, http.Header, error) {
	var body io.Reader
	if payload != nil {
		b, err := json.Marshal(payload)
		if err != nil {
			return 0, nil, nil, err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.adminURL+path, body)
	if err != nil {
		return 0, nil, nil, err
	}
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.authSecret != "" {
		req.Header.Set("Authorization", "Bearer "+c.authSecret)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return 0, nil, nil, fmt.Errorf("dial edge caddy (%s): %w", c.adminURL, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	return resp.StatusCode, raw, resp.Header, err
}
