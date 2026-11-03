package tailscale

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"os"
	"os/exec"
	"strings"
	"time"
)

// SelfInfo represents the local host's Tailscale identity.
type SelfInfo struct {
	Available    bool   `json:"available"`
	BackendState string `json:"backend_state"`
	IPv4         string `json:"ipv4"`
	HostName     string `json:"hostname"`
	Source       string `json:"source"` // "env" | "socket" | "cli" | "unavailable"
}

// PeerPingResult captures tunnel health between this machine and an Edge Node's Tailscale IP.
type PeerPingResult struct {
	Reachable  bool   `json:"reachable"`
	ConnType   string `json:"conn_type"`   // "direct" | "derp" | "offline"
	DERPRegion string `json:"derp_region"` // non-empty when relayed via DERP
	RTTMs      int    `json:"rtt_ms"`
}

// Inspector reads local Tailscale status via tailscaled.sock (or CLI / env override).
type Inspector struct {
	sockPath   string
	overrideIP string
	sockClient *http.Client
}

func NewInspector(sockPath, overrideIP string) *Inspector {
	tr := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", sockPath)
		},
	}
	return &Inspector{
		sockPath:   sockPath,
		overrideIP: strings.TrimSpace(overrideIP),
		sockClient: &http.Client{
			Transport: tr,
			Timeout:   4 * time.Second,
		},
	}
}

type tsStatusJSON struct {
	BackendState string `json:"BackendState"`
	Self         struct {
		HostName     string   `json:"HostName"`
		TailscaleIPs []string `json:"TailscaleIPs"`
		Online       bool     `json:"Online"`
	} `json:"Self"`
	Peer map[string]struct {
		HostName     string   `json:"HostName"`
		TailscaleIPs []string `json:"TailscaleIPs"`
		Online       bool     `json:"Online"`
		CurAddr      string   `json:"CurAddr"`
		Relay        string   `json:"Relay"`
	} `json:"Peer"`
}

type tsPingJSON struct {
	Err            string  `json:"Err"`
	LatencySeconds float64 `json:"LatencySeconds"`
	Endpoint       string  `json:"Endpoint"`
	DERPRegionCode string  `json:"DERPRegionCode"`
}

// SelfStatus returns the detected local Tailscale IPv4 and node status.
func (ins *Inspector) SelfStatus(ctx context.Context) SelfInfo {
	if ins.overrideIP != "" {
		host, _ := os.Hostname()
		return SelfInfo{
			Available:    true,
			BackendState: "Running",
			IPv4:         ins.overrideIP,
			HostName:     host,
			Source:       "env",
		}
	}

	st, source, err := ins.fetchStatus(ctx)
	if err == nil && st != nil {
		ipv4 := pickIPv4(st.Self.TailscaleIPs)
		if ipv4 != "" {
			return SelfInfo{
				Available:    st.BackendState == "Running",
				BackendState: st.BackendState,
				IPv4:         ipv4,
				HostName:     st.Self.HostName,
				Source:       source,
			}
		}
	}

	// Fallback: inspect local network interfaces for Tailscale CGNAT range 100.64.0.0/10.
	if ip := detectInterfaceCGNAT(); ip != "" {
		host, _ := os.Hostname()
		return SelfInfo{
			Available:    true,
			BackendState: "Running",
			IPv4:         ip,
			HostName:     host,
			Source:       "netif",
		}
	}

	return SelfInfo{
		Available:    false,
		BackendState: "Unavailable",
		Source:       "unavailable",
	}
}

// ProbePeer measures RTT and checks whether communication to peerTSIP is UDP Direct or DERP Relayed.
func (ins *Inspector) ProbePeer(ctx context.Context, peerTSIP, fallbackTCPAddr string) PeerPingResult {
	peerTSIP = strings.TrimSpace(peerTSIP)

	// 1. Try LocalAPI disco ping over tailscaled.sock for exact WireGuard Direct vs DERP info.
	if ins.sockAvailable() && peerTSIP != "" {
		u := fmt.Sprintf("http://local-tailscaled.sock/localapi/v0/ping?ip=%s&type=disco", peerTSIP)
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, nil)
		if err == nil {
			resp, err := ins.sockClient.Do(req)
			if err == nil {
				var pr tsPingJSON
				decErr := json.NewDecoder(resp.Body).Decode(&pr)
				_ = resp.Body.Close()
				if decErr == nil && pr.Err == "" && pr.LatencySeconds > 0 {
					rtt := int(pr.LatencySeconds * 1000)
					if rtt < 1 {
						rtt = 1
					}
					connType := "direct"
					if pr.Endpoint == "" && pr.DERPRegionCode != "" {
						connType = "derp"
					}
					return PeerPingResult{
						Reachable:  true,
						ConnType:   connType,
						DERPRegion: pr.DERPRegionCode,
						RTTMs:      rtt,
					}
				}
			}
		}
	}

	// 2. Check status peer table if available, combined with TCP RTT measurement to Caddy Admin port.
	connType := "direct"
	derpRegion := ""
	if st, _, err := ins.fetchStatus(ctx); err == nil && st != nil {
		for _, peer := range st.Peer {
			for _, pip := range peer.TailscaleIPs {
				if pip == peerTSIP {
					if !peer.Online {
						return PeerPingResult{Reachable: false, ConnType: "offline", RTTMs: -1}
					}
					if peer.CurAddr == "" && peer.Relay != "" {
						connType = "derp"
						derpRegion = peer.Relay
					}
				}
			}
		}
	}

	// 3. Measure TCP handshake RTT to fallbackTCPAddr (e.g. 100.116.63.43:2019).
	if fallbackTCPAddr != "" {
		start := time.Now()
		d := net.Dialer{Timeout: 3 * time.Second}
		conn, err := d.DialContext(ctx, "tcp", fallbackTCPAddr)
		if err == nil {
			_ = conn.Close()
			ms := int(time.Since(start).Milliseconds())
			if ms < 1 {
				ms = 1
			}
			return PeerPingResult{
				Reachable:  true,
				ConnType:   connType,
				DERPRegion: derpRegion,
				RTTMs:      ms,
			}
		}
	}

	return PeerPingResult{Reachable: false, ConnType: "offline", RTTMs: -1}
}

func (ins *Inspector) sockAvailable() bool {
	if ins.sockPath == "" {
		return false
	}
	_, err := os.Stat(ins.sockPath)
	return err == nil
}

func (ins *Inspector) fetchStatus(ctx context.Context) (*tsStatusJSON, string, error) {
	if ins.sockAvailable() {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://local-tailscaled.sock/localapi/v0/status", nil)
		if err == nil {
			resp, err := ins.sockClient.Do(req)
			if err == nil {
				defer resp.Body.Close()
				if resp.StatusCode == http.StatusOK {
					var st tsStatusJSON
					if err := json.NewDecoder(resp.Body).Decode(&st); err == nil {
						return &st, "socket", nil
					}
				}
			}
		}
	}

	if path, err := exec.LookPath("tailscale"); err == nil {
		cctx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		out, err := exec.CommandContext(cctx, path, "status", "--json").Output()
		if err == nil {
			var st tsStatusJSON
			if err := json.Unmarshal(out, &st); err == nil {
				return &st, "cli", nil
			}
		}
	}
	return nil, "", fmt.Errorf("tailscale status unavailable")
}

func pickIPv4(ips []string) string {
	for _, raw := range ips {
		if addr, err := netip.ParseAddr(raw); err == nil && addr.Is4() {
			return addr.String()
		}
	}
	return ""
}

func detectInterfaceCGNAT() string {
	prefix := netip.MustParsePrefix("100.64.0.0/10")
	ifaces, err := net.Interfaces()
	if err != nil {
		return ""
	}
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 {
			continue
		}
		if !strings.Contains(strings.ToLower(iface.Name), "tailscale") && !strings.HasPrefix(iface.Name, "ts") {
			continue
		}
		addrs, _ := iface.Addrs()
		for _, a := range addrs {
			ipStr := strings.Split(a.String(), "/")[0]
			if addr, err := netip.ParseAddr(ipStr); err == nil && addr.Is4() && prefix.Contains(addr) {
				return addr.String()
			}
		}
	}
	return ""
}
