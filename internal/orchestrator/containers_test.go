package orchestrator

import (
	"testing"

	"github.com/llanc/vantu/internal/discovery"
)

func TestContainerServesTarget(t *testing.T) {
	mapped := discovery.DiscoveredContainer{
		BridgeIP: "172.17.0.5",
		Ports: []discovery.DiscoveredPort{
			{PrivatePort: 80, PublicPort: 8080, BindIP: "0.0.0.0", TargetHost: "127.0.0.1", TargetPort: 8080},
		},
	}
	cases := []struct {
		host string
		port int
		want bool
	}{
		{"127.0.0.1", 8080, true},
		{"localhost", 8080, true},
		{"172.17.0.5", 80, true},
		{"127.0.0.1", 80, false},
		{"192.168.1.9", 8080, false},
		{"127.0.0.1", 9000, false},
	}
	for _, c := range cases {
		if got := containerServesTarget(mapped, c.host, c.port); got != c.want {
			t.Errorf("containerServesTarget(%s:%d) = %v, want %v", c.host, c.port, got, c.want)
		}
	}
}
