package cli

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/llanc/vantu/internal/api"
	"github.com/llanc/vantu/internal/config"
	"github.com/llanc/vantu/internal/crypto"
	"github.com/llanc/vantu/internal/discovery"
	"github.com/llanc/vantu/internal/events"
	"github.com/llanc/vantu/internal/orchestrator"
	"github.com/llanc/vantu/internal/prober"
	"github.com/llanc/vantu/internal/relay"
	"github.com/llanc/vantu/internal/store"
	"github.com/llanc/vantu/internal/tailscale"
)

func newServeCmd() *cobra.Command {
	cfg := config.FromEnv()
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "启动小凡兔 (Vantu) 核心控制面守护进程、Web 控制台与 Local Relay 网关",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runDaemon(cmd.Context(), cfg)
		},
	}
	cmd.Flags().StringVar(&cfg.ListenAddr, "listen", cfg.ListenAddr, "Web 控制台与 REST API 监听地址")
	cmd.Flags().StringVar(&cfg.DataDir, "data-dir", cfg.DataDir, "SQLite 与主密钥数据目录")
	cmd.Flags().IntVar(&cfg.RelayPort, "relay-port", cfg.RelayPort, "Local Relay 安全桥接监听端口")
	return cmd
}

func runDaemon(parentCtx context.Context, cfg config.Config) error {
	// Recompute derived paths if --data-dir was overridden on CLI and explicit env wasn't set.
	if os.Getenv("VANTU_DB_PATH") == "" {
		cfg.DBPath = filepath.Join(cfg.DataDir, "vantu.db")
	}
	if os.Getenv("VANTU_SOCK_PATH") == "" {
		cfg.SocketPath = filepath.Join(cfg.DataDir, "vantu.sock")
	}

	if err := os.MkdirAll(cfg.DataDir, 0o755); err != nil {
		return fmt.Errorf("create data directory %s: %w", cfg.DataDir, err)
	}

	vault, err := crypto.NewVaultFromKeyOrFile(cfg.MasterKeyRaw, filepath.Join(cfg.DataDir, "master.key"))
	if err != nil {
		return fmt.Errorf("initialize master vault: %w", err)
	}

	st, err := store.Open(cfg.DBPath)
	if err != nil {
		return fmt.Errorf("open sqlite store: %w", err)
	}
	defer st.Close()

	ctx, stop := signal.NotifyContext(parentCtx, syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// Optional headless admin bootstrap when VANTU_ADMIN_PASSWORD is supplied.
	if cfg.AdminPassword != "" {
		initOK, _ := st.IsInitialized(ctx)
		if !initOK {
			hash, hErr := crypto.HashPassword(cfg.AdminPassword)
			if hErr == nil {
				_ = st.CreateOrUpdateAdmin(ctx, "admin", hash)
				log.Printf("[vantu] Bootstrapped initial admin account from VANTU_ADMIN_PASSWORD")
			}
		}
	}

	hub := events.NewHub()
	dockerWatcher := discovery.NewDockerWatcher(cfg.DockerSock)
	tsInspector := tailscale.NewInspector(cfg.TailscaleSock, cfg.TailscaleIP)

	relaySrv := relay.NewServer(st, vault, dockerWatcher, tsInspector, cfg.RelayBindIP, cfg.RelayPort)
	go func() {
		if rErr := relaySrv.Start(ctx); rErr != nil {
			log.Printf("[vantu-relay] warning: %v", rErr)
		}
	}()

	pr := prober.New(st, vault, dockerWatcher, tsInspector, hub, prober.Options{
		Interval: cfg.ProbeInterval,
	})
	go pr.Start(ctx)

	go dockerWatcher.WatchEvents(ctx, func() {
		hub.Publish("containers_updated", map[string]any{"source": "docker_events"})
	})

	svc := orchestrator.NewService(cfg, st, vault, dockerWatcher, tsInspector, relaySrv, pr, hub)
	apiSrv := api.NewServer(cfg, svc)

	log.Printf("[vantu] 小凡兔 · Vantu %s 已启动 -> Web UI & API: http://%s | Unix Socket: %s",
		config.Version, cfg.ListenAddr, cfg.SocketPath)
	return apiSrv.Start(ctx)
}
