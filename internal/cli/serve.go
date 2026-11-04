package cli

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"sync/atomic"
	"syscall"
	"time"

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
	"github.com/llanc/vantu/internal/update"
)

func newServeCmd() *cobra.Command {
	cfg := config.FromEnv()
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "启动小凡兔 (Vantu) 核心控制面守护进程、Web 控制台与 Local Relay 网关",
		RunE: func(cmd *cobra.Command, _ []string) error {
			// Resolve before a self-update swaps the file; afterwards /proc/self/exe points at the deleted inode.
			exe, exeErr := os.Executable()
			err := runDaemon(cmd.Context(), cfg)
			if !errors.Is(err, errRestart) {
				return err
			}
			if exeErr != nil {
				return fmt.Errorf("更新已完成但无法定位可执行文件，请手动重启: %w", exeErr)
			}
			log.Printf("[vantu] 正在以新版本重启: %s", exe)
			return syscall.Exec(exe, os.Args, os.Environ())
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
				_ = st.SetAdminPassword(ctx, hash)
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

	var restartRequested atomic.Bool
	updater := update.NewManager(config.Version, cfg.UpdateCheck)
	apiSrv.EnableUpdates(updater, func() {
		restartRequested.Store(true)
		stop()
	})
	if cfg.UpdateCheck {
		go watchUpdates(ctx, updater, hub)
	}

	log.Printf("[vantu] 小凡兔 · Vantu %s 已启动 -> Web UI & API: http://%s | Unix Socket: %s",
		config.Version, cfg.ListenAddr, cfg.SocketPath)
	err = apiSrv.Start(ctx)
	if restartRequested.Load() {
		return errRestart
	}
	return err
}

var errRestart = errors.New("restart requested after self-update")

func watchUpdates(ctx context.Context, m *update.Manager, hub *events.Hub) {
	timer := time.NewTimer(30 * time.Second)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		if info := m.Info(ctx, true); info.HasUpdate {
			log.Printf("[vantu] 发现新版本 %s（当前 %s）：%s", info.LatestVersion, info.CurrentVersion, info.ReleaseURL)
			hub.Publish("update_available", info)
		}
		timer.Reset(12 * time.Hour)
	}
}
