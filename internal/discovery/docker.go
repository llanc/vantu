package discovery

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
)

// DiscoveredPort describes a single port candidate on a running Docker container.
type DiscoveredPort struct {
	PrivatePort     int    `json:"private_port"`
	PublicPort      int    `json:"public_port,omitempty"`
	BindIP          string `json:"bind_ip,omitempty"`
	Protocol        string `json:"protocol"`
	TargetHost      string `json:"target_host"`
	TargetPort      int    `json:"target_port"`
	RecommendedMode string `json:"recommended_mode"` // "direct" | "relay"
	Reachability    string `json:"reachability"`     // "host_public" | "loopback_only" | "bridge_internal"
	Reason          string `json:"reason"`
}

// DiscoveredContainer represents a running container parsed from /var/run/docker.sock.
type DiscoveredContainer struct {
	ID               string            `json:"id"`
	Name             string            `json:"name"`
	Image            string            `json:"image"`
	State            string            `json:"state"`
	Status           string            `json:"status"`
	NetworkMode      string            `json:"network_mode"`
	BridgeIP         string            `json:"bridge_ip,omitempty"`
	Ports            []DiscoveredPort  `json:"ports"`
	RecommendedMode  string            `json:"recommended_mode"`
	RecommendedPort  int               `json:"recommended_port"`
	RecommendedHost  string            `json:"recommended_host"`
	SuggestedPreset  string            `json:"suggested_preset"`
	SuggestedSub     string            `json:"suggested_sub"`
	ModeReason       string            `json:"mode_reason"`
	Labels           map[string]string `json:"labels,omitempty"`
	PublishedDomains []string          `json:"published_domains"`
}

// DockerWatcher inspects running containers and watches lifecycle events over Docker's Unix socket.
type DockerWatcher struct {
	sockPath string
	client   *http.Client
}

func NewDockerWatcher(sockPath string) *DockerWatcher {
	tr := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", sockPath)
		},
	}
	return &DockerWatcher{
		sockPath: sockPath,
		client: &http.Client{
			Transport: tr,
			Timeout:   4 * time.Second,
		},
	}
}

func (w *DockerWatcher) Available() bool {
	if w == nil || w.sockPath == "" {
		return false
	}
	_, err := os.Stat(w.sockPath)
	return err == nil
}

type rawDockerContainer struct {
	ID      string            `json:"Id"`
	Names   []string          `json:"Names"`
	Image   string            `json:"Image"`
	State   string            `json:"State"`
	Status  string            `json:"Status"`
	Labels  map[string]string `json:"Labels"`
	Ports   []rawDockerPort   `json:"Ports"`
	HostCfg struct {
		NetworkMode string `json:"NetworkMode"`
	} `json:"HostConfig"`
	NetworkSettings struct {
		Networks map[string]struct {
			IPAddress string `json:"IPAddress"`
		} `json:"Networks"`
	} `json:"NetworkSettings"`
}

type rawDockerPort struct {
	IP          string `json:"IP"`
	PrivatePort int    `json:"PrivatePort"`
	PublicPort  int    `json:"PublicPort"`
	Type        string `json:"Type"`
}

// ListContainers returns running containers classified by port reachability and matched against existing route map.
func (w *DockerWatcher) ListContainers(ctx context.Context, publishedByContainer map[string][]string) ([]DiscoveredContainer, bool, error) {
	if !w.Available() {
		return []DiscoveredContainer{}, false, nil
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://docker/containers/json?all=0", nil)
	if err != nil {
		return nil, true, err
	}
	resp, err := w.client.Do(req)
	if err != nil {
		return []DiscoveredContainer{}, false, fmt.Errorf("query docker.sock: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return nil, true, fmt.Errorf("docker engine status %d", resp.StatusCode)
	}

	var rawList []rawDockerContainer
	if err := json.NewDecoder(resp.Body).Decode(&rawList); err != nil {
		return nil, true, err
	}

	out := make([]DiscoveredContainer, 0, len(rawList))
	for _, rc := range rawList {
		name := "container"
		if len(rc.Names) > 0 {
			name = strings.TrimPrefix(rc.Names[0], "/")
		}
		// Skip Vantu itself and Edge Caddy container to avoid self-proxying loops by accident.
		lowerName := strings.ToLower(name)
		if lowerName == "vantu" || lowerName == "vantu-edge-caddy" {
			continue
		}

		shortID := rc.ID
		if len(shortID) > 12 {
			shortID = shortID[:12]
		}

		var bridgeIP string
		netNames := make([]string, 0, len(rc.NetworkSettings.Networks))
		for k := range rc.NetworkSettings.Networks {
			netNames = append(netNames, k)
		}
		sort.Strings(netNames)
		for _, k := range netNames {
			if ip := rc.NetworkSettings.Networks[k].IPAddress; ip != "" {
				bridgeIP = ip
				break
			}
		}

		ports := classifyPorts(rc.Ports, rc.HostCfg.NetworkMode, bridgeIP)
		recMode := "relay"
		recHost := "127.0.0.1"
		recPort := 80
		reason := "容器未映射宿主机全网卡端口，将通过 Vantu Local Relay 安全桥接转发。"

		if len(ports) > 0 {
			recMode = ports[0].RecommendedMode
			recHost = ports[0].TargetHost
			recPort = ports[0].TargetPort
			reason = ports[0].Reason
		} else if bridgeIP != "" {
			recHost = bridgeIP
		}

		preset := inferPreset(name, rc.Image, recPort, rc.Labels)
		subSuggest := sanitizeSubdomain(name, rc.Labels)
		pubs := publishedByContainer[name]
		if pubs == nil {
			pubs = []string{}
		}

		out = append(out, DiscoveredContainer{
			ID:               shortID,
			Name:             name,
			Image:            rc.Image,
			State:            rc.State,
			Status:           rc.Status,
			NetworkMode:      rc.HostCfg.NetworkMode,
			BridgeIP:         bridgeIP,
			Ports:            ports,
			RecommendedMode:  recMode,
			RecommendedPort:  recPort,
			RecommendedHost:  recHost,
			SuggestedPreset:  preset,
			SuggestedSub:     subSuggest,
			ModeReason:       reason,
			Labels:           rc.Labels,
			PublishedDomains: pubs,
		})
	}

	// Sort unexposed containers first, then alphabetically by name.
	sort.Slice(out, func(i, j int) bool {
		iUnpub := len(out[i].PublishedDomains) == 0
		jUnpub := len(out[j].PublishedDomains) == 0
		if iUnpub != jUnpub {
			return iUnpub
		}
		return out[i].Name < out[j].Name
	})

	return out, true, nil
}

// ResolveContainerTarget dynamically resolves a container's current reachable host:port (immune to Bridge IP changes on restart).
func (w *DockerWatcher) ResolveContainerTarget(ctx context.Context, containerName string, fallbackHost string, fallbackPort int) (string, int) {
	list, ok, err := w.ListContainers(ctx, nil)
	if !ok || err != nil {
		return fallbackHost, fallbackPort
	}
	for _, c := range list {
		if c.Name == containerName {
			for _, p := range c.Ports {
				if p.PrivatePort == fallbackPort || p.TargetPort == fallbackPort {
					return p.TargetHost, p.TargetPort
				}
			}
			if c.BridgeIP != "" && strings.HasPrefix(fallbackHost, "172.") {
				return c.BridgeIP, fallbackPort
			}
			return c.RecommendedHost, c.RecommendedPort
		}
	}
	return fallbackHost, fallbackPort
}

// WatchEvents listens to Docker container start/stop/die/rename events and invokes onChange.
func (w *DockerWatcher) WatchEvents(ctx context.Context, onChange func()) {
	streamClient := &http.Client{
		Transport: w.client.Transport,
	}
	for {
		if ctx.Err() != nil {
			return
		}
		if !w.Available() {
			select {
			case <-ctx.Done():
				return
			case <-time.After(10 * time.Second):
				continue
			}
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodGet,
			`http://docker/events?filters=%7B%22type%22%3A%5B%22container%22%5D%2C%22event%22%3A%5B%22start%22%2C%22die%22%2C%22stop%22%2C%22rename%22%5D%7D`, nil)
		if err != nil {
			return
		}
		resp, err := streamClient.Do(req)
		if err != nil {
			select {
			case <-ctx.Done():
				return
			case <-time.After(5 * time.Second):
				continue
			}
		}

		scanner := bufio.NewScanner(resp.Body)
		for scanner.Scan() {
			if onChange != nil {
				onChange()
			}
		}
		_ = resp.Body.Close()
		select {
		case <-ctx.Done():
			return
		case <-time.After(2 * time.Second):
		}
	}
}

func classifyPorts(raw []rawDockerPort, networkMode, bridgeIP string) []DiscoveredPort {
	seen := make(map[string]bool)
	out := make([]DiscoveredPort, 0, len(raw))

	for _, p := range raw {
		if p.Type != "" && p.Type != "tcp" {
			continue
		}
		key := strconv.Itoa(p.PrivatePort) + ":" + strconv.Itoa(p.PublicPort)
		if seen[key] {
			continue
		}
		seen[key] = true

		dp := DiscoveredPort{
			PrivatePort: p.PrivatePort,
			PublicPort:  p.PublicPort,
			BindIP:      p.IP,
			Protocol:    "tcp",
		}

		switch {
		case networkMode == "host":
			dp.TargetHost = "127.0.0.1"
			dp.TargetPort = p.PrivatePort
			dp.RecommendedMode = "direct"
			dp.Reachability = "host_public"
			dp.Reason = fmt.Sprintf("容器采用 host 网络模式，边缘节点可直接经本机 Tailscale IP 访问端口 %d。", p.PrivatePort)
		case p.PublicPort > 0 && (p.IP == "0.0.0.0" || p.IP == "::" || strings.HasPrefix(p.IP, "100.")):
			dp.TargetHost = "127.0.0.1"
			dp.TargetPort = p.PublicPort
			dp.RecommendedMode = "direct"
			dp.Reachability = "host_public"
			dp.Reason = fmt.Sprintf("端口已映射至宿主机全网卡 (0.0.0.0:%d)，推荐 Direct 高速直连模式。", p.PublicPort)
		case p.PublicPort > 0 && (p.IP == "127.0.0.1" || p.IP == "::1"):
			dp.TargetHost = "127.0.0.1"
			dp.TargetPort = p.PublicPort
			dp.RecommendedMode = "relay"
			dp.Reachability = "loopback_only"
			dp.Reason = fmt.Sprintf("端口仅绑定本机回环 (127.0.0.1:%d)，自动启用 Vantu Local Relay 安全桥接，无需暴露宿主机网卡。", p.PublicPort)
		default:
			host := bridgeIP
			if host == "" {
				host = "127.0.0.1"
			}
			dp.TargetHost = host
			dp.TargetPort = p.PrivatePort
			dp.RecommendedMode = "relay"
			dp.Reachability = "bridge_internal"
			dp.Reason = fmt.Sprintf("容器未映射 -p 宿主机端口（仅内网 %s:%d），自动启用 Local Relay 安全桥接穿透。", host, p.PrivatePort)
		}
		out = append(out, dp)
	}

	// Prefer host_public > loopback_only > bridge_internal when picking default port.
	sort.SliceStable(out, func(i, j int) bool {
		rank := func(r string) int {
			switch r {
			case "host_public":
				return 0
			case "loopback_only":
				return 1
			default:
				return 2
			}
		}
		return rank(out[i].Reachability) < rank(out[j].Reachability)
	})
	return out
}

func inferPreset(name, image string, port int, labels map[string]string) string {
	if v := strings.TrimSpace(labels["vantu.preset"]); v != "" {
		return v
	}
	s := strings.ToLower(name + " " + image)
	switch {
	case strings.Contains(s, "sub2api"), strings.Contains(s, "one-api"), strings.Contains(s, "new-api"),
		strings.Contains(s, "ollama"), strings.Contains(s, "vllm"), strings.Contains(s, "open-webui"),
		strings.Contains(s, "dify"), strings.Contains(s, "litellm"):
		return "sse"
	case strings.Contains(s, "webdav"), strings.Contains(s, "alist"), strings.Contains(s, "openlist"),
		strings.Contains(s, "immich"), strings.Contains(s, "nextcloud"), strings.Contains(s, "seafile"),
		strings.Contains(s, "minio"):
		return "webdav"
	case strings.Contains(s, "portainer") && port == 9443, port == 8006, strings.Contains(s, "proxmox"):
		return "https_insecure"
	default:
		return "web"
	}
}

func sanitizeSubdomain(name string, labels map[string]string) string {
	if v := strings.TrimSpace(labels["vantu.subdomain"]); v != "" {
		return strings.ToLower(v)
	}
	clean := strings.ToLower(name)
	for _, trim := range []string{"-server", "-web", "-app", "_1", "-1"} {
		clean = strings.TrimSuffix(clean, trim)
	}
	var b strings.Builder
	for _, ch := range clean {
		if (ch >= 'a' && ch <= 'z') || (ch >= '0' && ch <= '9') || ch == '-' {
			b.WriteRune(ch)
		}
	}
	res := strings.Trim(b.String(), "-")
	if res == "" {
		return "app"
	}
	return res
}
