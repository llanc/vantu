package config

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Version is injected at build time via -ldflags; defaults to v0.1.0.
var Version = "v0.1.0"

// Config holds server runtime parameters loaded from environment variables or CLI flags.
type Config struct {
	ListenAddr    string
	DataDir       string
	DBPath        string
	SocketPath    string
	RelayBindIP   string
	RelayPort     int
	DockerSock    string
	TailscaleSock string
	TailscaleIP   string
	ProbeInterval time.Duration
	AdminPassword string
	MasterKeyRaw  string
	UpdateCheck   bool
}

// FromEnv constructs Config with sensible defaults for Homelab / Docker deployments.
func FromEnv() Config {
	dataDir := envOr("VANTU_DATA_DIR", "./data")
	listenAddr := envOr("VANTU_LISTEN_ADDR", "")
	if listenAddr == "" {
		port := envOr("PORT", "8089")
		listenAddr = "0.0.0.0:" + strings.TrimPrefix(port, ":")
	}

	relayPort, err := strconv.Atoi(envOr("VANTU_RELAY_PORT", "18080"))
	if err != nil || relayPort <= 0 {
		relayPort = 18080
	}

	probeSec, err := strconv.Atoi(envOr("VANTU_PROBE_INTERVAL_SEC", "60"))
	if err != nil || probeSec < 10 {
		probeSec = 60
	}

	return Config{
		ListenAddr:    listenAddr,
		DataDir:       dataDir,
		DBPath:        envOr("VANTU_DB_PATH", filepath.Join(dataDir, "vantu.db")),
		SocketPath:    envOr("VANTU_SOCK_PATH", filepath.Join(dataDir, "vantu.sock")),
		RelayBindIP:   os.Getenv("VANTU_RELAY_BIND_IP"),
		RelayPort:     relayPort,
		DockerSock:    envOr("VANTU_DOCKER_SOCK", "/var/run/docker.sock"),
		TailscaleSock: envOr("VANTU_TAILSCALE_SOCK", "/var/run/tailscale/tailscaled.sock"),
		TailscaleIP:   os.Getenv("VANTU_TAILSCALE_IP"),
		ProbeInterval: time.Duration(probeSec) * time.Second,
		AdminPassword: os.Getenv("VANTU_ADMIN_PASSWORD"),
		MasterKeyRaw:  os.Getenv("VANTU_MASTER_KEY"),
		UpdateCheck:   !isOff(os.Getenv("VANTU_UPDATE_CHECK")),
	}
}

func isOff(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "0", "false", "off", "no":
		return true
	}
	return false
}

func envOr(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}
