package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/llanc/vantu/internal/caddy"
	"github.com/llanc/vantu/internal/config"
	"github.com/llanc/vantu/internal/discovery"
	"github.com/llanc/vantu/internal/orchestrator"
	"github.com/llanc/vantu/internal/skilldoc"
	"github.com/llanc/vantu/internal/store"
	"github.com/llanc/vantu/internal/update"
)

type globalFlags struct {
	jsonOut  bool
	endpoint string
	token    string
	sockPath string
}

// NewRootCmd builds the complete `vantu` / `xft` CLI command tree.
func NewRootCmd() *cobra.Command {
	binName := filepath.Base(os.Args[0])
	if binName != "xft" {
		binName = "vantu"
	}

	gf := &globalFlags{
		jsonOut: strings.EqualFold(os.Getenv("VANTU_OUTPUT"), "json"),
	}

	root := &cobra.Command{
		Use:           binName,
		Short:         "小凡兔 Vantu — 跨网反代与服务发布工具",
		SilenceUsage:  true,
		SilenceErrors: true,
		Version:       config.Version,
	}

	root.PersistentFlags().BoolVar(&gf.jsonOut, "json", gf.jsonOut, "输出结构化 JSON 信封（AI Agent 推荐开启，等价于 VANTU_OUTPUT=json）")
	root.PersistentFlags().StringVar(&gf.endpoint, "endpoint", "", "远程小凡兔 API 地址 (默认优先走本地 Unix Socket，其次 VANTU_ENDPOINT)")
	root.PersistentFlags().StringVar(&gf.token, "token", "", "API Bearer Token (等价于 VANTU_TOKEN)")
	root.PersistentFlags().StringVar(&gf.sockPath, "socket", "", "本地 Unix Socket 路径覆盖")

	root.AddCommand(
		newServeCmd(),
		newAdminCmd(gf),
		newLoginCmd(gf),
		newCapabilitiesCmd(gf),
		newDockerCmd(gf),
		newPublishCmd(gf),
		newRouteCmd(gf),
		newNodeCmd(gf),
		newDNSCmd(gf),
		newTokenCmd(gf),
		newDoctorCmd(gf),
		newSkillCmd(gf),
		newUpdateCmd(gf),
	)

	return root
}

func (gf *globalFlags) client() *apiClient {
	return newAPIClient(gf.endpoint, gf.token, gf.sockPath)
}

func (gf *globalFlags) execAPI(ctx context.Context, method, path string, reqBody any, dst any) ([]byte, error) {
	env, raw, err := gf.client().call(ctx, method, path, reqBody)
	if err != nil {
		if gf.jsonOut {
			out, _ := json.Marshal(map[string]any{
				"ok": false,
				"error": map[string]any{
					"code":    "DAEMON_UNREACHABLE",
					"message": err.Error(),
				},
			})
			fmt.Println(string(out))
		} else {
			fmt.Fprintf(os.Stderr, "错误: %s\n", err.Error())
		}
		return nil, &CLIExitError{ExitCode: 1, Err: err}
	}

	if !env.OK {
		code := "INTERNAL_ERROR"
		msg := "请求失败"
		sug := ""
		if env.Error != nil {
			code = env.Error.Code
			msg = env.Error.Message
			sug = env.Error.Suggestion
		}
		exitCode := mapCodeToExit(code)
		if gf.jsonOut {
			fmt.Println(prettyJSON(raw))
		} else {
			fmt.Fprintf(os.Stderr, "[%s] %s\n", code, msg)
			if sug != "" {
				fmt.Fprintf(os.Stderr, "建议: %s\n", sug)
			}
		}
		return raw, &CLIExitError{ExitCode: exitCode, Err: fmt.Errorf("%s: %s", code, msg)}
	}

	if gf.jsonOut {
		fmt.Println(prettyJSON(raw))
	}
	if dst != nil && len(env.Data) > 0 {
		if err := json.Unmarshal(env.Data, dst); err != nil {
			return raw, err
		}
	}
	return raw, nil
}

func prettyJSON(raw []byte) string {
	var obj any
	if err := json.Unmarshal(raw, &obj); err != nil {
		return string(raw)
	}
	b, err := json.MarshalIndent(obj, "", "  ")
	if err != nil {
		return string(raw)
	}
	return string(b)
}

// --- Subcommands ---

func newAdminCmd(gf *globalFlags) *cobra.Command {
	adminCmd := &cobra.Command{
		Use:   "admin",
		Short: "初始化或重置本地管理员账号",
	}

	var password string
	var generate bool
	passwdCmd := &cobra.Command{
		Use:   "passwd",
		Short: "初始化或重置管理员密码（本机 Unix Socket 直连免密执行）",
		Example: `  vantu admin passwd --generate
  vantu admin passwd --password 'YourStrongPassword'`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			var res struct {
				Updated           bool   `json:"updated"`
				GeneratedPassword string `json:"generated_password"`
			}
			_, err := gf.execAPI(cmd.Context(), http.MethodPost, "/api/v1/auth/admin", map[string]any{
				"password": password,
				"generate": generate,
			}, &res)
			if err != nil {
				return err
			}
			if gf.jsonOut {
				return nil
			}
			if res.GeneratedPassword != "" {
				fmt.Printf("管理员密码已生成（请立即妥善保存）：\n\n  %s\n\n", res.GeneratedPassword)
			} else {
				fmt.Println("管理员密码已设置完成。")
			}
			return nil
		},
	}
	passwdCmd.Flags().StringVar(&password, "password", "", "指定管理员密码（至少 6 位）")
	passwdCmd.Flags().BoolVar(&generate, "generate", false, "自动生成高强度随机密码并打印")

	adminCmd.AddCommand(passwdCmd)
	return adminCmd
}

func newLoginCmd(gf *globalFlags) *cobra.Command {
	var password string
	cmd := &cobra.Command{
		Use:   "login",
		Short: "登录远程小凡兔实例并保存凭据至 ~/.config/vantu/credentials.json",
		RunE: func(cmd *cobra.Command, _ []string) error {
			ep := gf.endpoint
			if ep == "" {
				ep = os.Getenv("VANTU_ENDPOINT")
			}
			if ep == "" {
				ep = "http://127.0.0.1:8089"
			}
			tok := gf.token
			if tok == "" && password != "" {
				cli := newAPIClient(ep, "", "")
				env, raw, err := cli.call(cmd.Context(), http.MethodPost, "/api/v1/auth/login", map[string]string{
					"password": password,
				})
				if err != nil {
					return err
				}
				if !env.OK {
					if gf.jsonOut {
						fmt.Println(prettyJSON(raw))
					}
					return &CLIExitError{ExitCode: 3, Err: fmt.Errorf("登录失败")}
				}
				var data struct {
					SessionToken string `json:"session_token"`
				}
				_ = json.Unmarshal(env.Data, &data)
				tok = data.SessionToken
			}
			if tok == "" {
				return &CLIExitError{ExitCode: 2, Err: fmt.Errorf("请提供 --token vtu_xxx 或 --password 进行登录")}
			}
			// Verify token works against capabilities
			cli := newAPIClient(ep, tok, "")
			env, raw, err := cli.call(cmd.Context(), http.MethodGet, "/api/v1/capabilities", nil)
			if err != nil {
				return err
			}
			if !env.OK {
				if gf.jsonOut {
					fmt.Println(prettyJSON(raw))
				}
				return &CLIExitError{ExitCode: 3, Err: fmt.Errorf("Token 校验未通过")}
			}
			if err := saveCredentials(ep, tok); err != nil {
				return err
			}
			if gf.jsonOut {
				b, _ := json.MarshalIndent(map[string]any{"ok": true, "data": map[string]any{"endpoint": ep, "saved_to": credentialsPath()}}, "", "  ")
				fmt.Println(string(b))
			} else {
				fmt.Printf("登录成功！凭据已保存至 %s\n", credentialsPath())
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&password, "password", "", "管理员密码（若未使用 --token）")
	return cmd
}

func newCapabilitiesCmd(gf *globalFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "capabilities",
		Short: "一次性输出可用域名、边缘节点、本机容器与组网状态（AI Agent 首选入口）",
		RunE: func(cmd *cobra.Command, _ []string) error {
			var rep orchestrator.CapabilitiesReport
			if _, err := gf.execAPI(cmd.Context(), http.MethodGet, "/api/v1/capabilities", nil, &rep); err != nil {
				return err
			}
			if gf.jsonOut {
				return nil
			}
			fmt.Printf("小凡兔 · Vantu %s\n", rep.Version)
			fmt.Printf("  管理员已初始化 : %v\n", rep.Initialized)
			fmt.Printf("  本机 Tailscale : %v (%s)\n", rep.Tailscale.Available, rep.Tailscale.IPv4)
			fmt.Printf("  Local Relay    : %v (%s)\n", rep.RelayRunning, rep.RelayAddr)
			fmt.Printf("  Docker 自动发现: %v (总容器 %d / 待发布 %d)\n", rep.DockerAvailable, rep.ContainerCount, rep.UnexposedContainers)
			fmt.Printf("  可用根域名     : %d 个\n", len(rep.Zones))
			for _, z := range rep.Zones {
				def := ""
				if z.IsDefault {
					def = " [默认]"
				}
				fmt.Printf("    - %s (%s)%s\n", z.ZoneName, z.ProviderName, def)
			}
			fmt.Printf("  边缘公网节点   : %d 台\n", len(rep.Nodes))
			for _, n := range rep.Nodes {
				def := ""
				if n.IsDefault {
					def = " [默认]"
				}
				fmt.Printf("    - %s: 公网 %s | TS %s (%s %dms) | 状态 %s%s\n",
					n.Name, n.PublicIPv4, n.TailscaleIP, n.TSConnType, n.TSRTTMs, n.HealthStatus, def)
			}
			fmt.Printf("  已发布路由     : %d 在线 / 共 %d 条\n", rep.ActiveRoutes, rep.TotalRoutes)
			return nil
		},
	}
}

func newDockerCmd(gf *globalFlags) *cobra.Command {
	dockerCmd := &cobra.Command{
		Use:   "docker",
		Short: "探测本机运行中的 Docker 容器与端口可达性模式",
	}
	lsCmd := &cobra.Command{
		Use:   "ls",
		Short: "列出运行中容器及 Direct / Relay 推荐转发方式",
		RunE: func(cmd *cobra.Command, _ []string) error {
			var res struct {
				DockerAvailable bool                            `json:"docker_available"`
				Containers      []discovery.DiscoveredContainer `json:"containers"`
			}
			if _, err := gf.execAPI(cmd.Context(), http.MethodGet, "/api/v1/containers", nil, &res); err != nil {
				return err
			}
			if gf.jsonOut {
				return nil
			}
			if !res.DockerAvailable {
				fmt.Println("当前未检测到 /var/run/docker.sock，请挂载 Docker Socket 或使用手动 IP:Port 发布。")
				return nil
			}
			tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
			fmt.Fprintln(tw, "CONTAINER\tTARGET\tMODE\tPRESET\tPUBLISHED")
			for _, c := range res.Containers {
				pubs := "-"
				if len(c.PublishedDomains) > 0 {
					pubs = strings.Join(c.PublishedDomains, ",")
				}
				fmt.Fprintf(tw, "%s\t%s:%d\t%s\t%s\t%s\n",
					c.Name, c.RecommendedHost, c.RecommendedPort, strings.ToUpper(c.RecommendedMode), c.SuggestedPreset, pubs)
			}
			return tw.Flush()
		},
	}
	dockerCmd.AddCommand(lsCmd)
	return dockerCmd
}

func newPublishCmd(gf *globalFlags) *cobra.Command {
	var in orchestrator.PublishInput
	cmd := &cobra.Command{
		Use:   "publish",
		Short: "一键将本地容器或内网端口发布到公网 HTTPS 域名（幂等创建或更新）",
		Example: `  vantu publish --target self --domain vantu.xiaofantu.com --proxied
  vantu publish --target container:sub2api --domain api.xiaofantu.com --preset sse --dry-run
  vantu publish --target 127.0.0.1:8080 --domain demo.xiaofantu.com --proxied --wait`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			var res orchestrator.PublishResult
			if _, err := gf.execAPI(cmd.Context(), http.MethodPost, "/api/v1/routes", in, &res); err != nil {
				return err
			}
			if gf.jsonOut {
				return nil
			}
			if res.DryRun {
				fmt.Printf("[DRY-RUN 预演计划] %s -> %s:%d (%s 模式)\n",
					res.Route.FullDomain, res.Route.LocalTargetHost, res.Route.LocalTargetPort, strings.ToUpper(res.Route.ForwardMode))
				for i, st := range res.Steps {
					fmt.Printf("  %d. %s: %s\n", i+1, st.Title, st.Detail)
				}
				return nil
			}
			fmt.Printf("发布完成: https://%s\n", res.Route.FullDomain)
			fmt.Printf("  转发链路 : %s:%d -> (%s %s) -> 节点 %s (%s)\n",
				res.Route.LocalTargetHost, res.Route.LocalTargetPort,
				strings.ToUpper(res.Route.ForwardMode), res.Route.EffectiveDial,
				res.Route.EdgeNodeName, res.Route.EdgeNodePublicIP)
			fmt.Printf("  当前状态 : %s (源站证书: %s, 剩余 %d 天)\n",
				res.Route.LifecycleState, res.Route.OriginCertIssuer, res.Route.OriginCertDays)
			return nil
		},
	}
	cmd.Flags().StringVar(&in.Target, "target", "", "发布目标 (self=自身控制台, container:sub2api, 127.0.0.1:8080, https://192.168.1.2:8006)")
	cmd.Flags().StringVar(&in.Domain, "domain", "", "完整域名 (如 api.xiaofantu.com) 或子域名前缀 (如 api)")
	cmd.Flags().StringVar(&in.ZoneID, "zone", "", "指定根域名 ID 或名称 (默认自动匹配后缀或使用默认域名)")
	cmd.Flags().StringVar(&in.EdgeNodeID, "node", "", "边缘公网节点名称或 ID (默认使用默认节点)")
	cmd.Flags().StringVar(&in.Preset, "preset", "", "协议预设: web | sse | webdav | https_insecure")
	cmd.Flags().StringVar(&in.ForwardMode, "mode", "auto", "转发通道: auto | direct | relay")
	cmd.Flags().BoolVar(&in.DNSProxied, "proxied", false, "开启 Cloudflare CDN 代理")
	cmd.Flags().BoolVar(&in.AdoptDNS, "adopt-dns", false, "允许接管 DNS 厂商中已存在的非小凡兔同名解析")
	cmd.Flags().BoolVar(&in.DryRun, "dry-run", false, "仅生成执行计划与 Caddy JSON 预览，不执行真实写入")
	cmd.Flags().BoolVar(&in.WaitTLS, "wait", false, "阻塞等待边缘源站 TLS 证书签发就绪")
	cmd.Flags().BoolVar(&in.SkipLocalCheck, "skip-local-check", false, "跳过发布前本地端口连通性探测")
	cmd.Flags().IntVar(&in.MaxBodyMB, "max-body-mb", 0, "限制最大上传请求体大小 (MB, 0=不限制)")
	cmd.Flags().StringVar(&in.Note, "note", "", "路由备注说明")
	return cmd
}

func newRouteCmd(gf *globalFlags) *cobra.Command {
	routeCmd := &cobra.Command{
		Use:   "route",
		Short: "查看、巡检、开闭 CDN、启停或下线已发布的公网路由",
	}

	lsCmd := &cobra.Command{
		Use:   "ls",
		Short: "列出所有已发布路由",
		RunE: func(cmd *cobra.Command, _ []string) error {
			var list []store.Route
			if _, err := gf.execAPI(cmd.Context(), http.MethodGet, "/api/v1/routes", nil, &list); err != nil {
				return err
			}
			if gf.jsonOut {
				return nil
			}
			if len(list) == 0 {
				fmt.Println("当前尚无已发布的路由，可使用 vantu publish 创建。")
				return nil
			}
			tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
			fmt.Fprintln(tw, "DOMAIN\tTARGET\tMODE\tNODE\tCDN\tCERT DAYS\tSTATE")
			for _, r := range list {
				cdn := "DNS-Only"
				if r.DNSProxied {
					cdn = "CF-Proxy"
				}
				certStr := "-"
				if r.OriginCertDays >= 0 {
					certStr = fmt.Sprintf("%dd", r.OriginCertDays)
				}
				fmt.Fprintf(tw, "%s\t%s:%d\t%s\t%s\t%s\t%s\t%s\n",
					r.FullDomain, r.LocalTargetHost, r.LocalTargetPort, strings.ToUpper(r.ForwardMode),
					r.EdgeNodeName, cdn, certStr, r.LifecycleState)
			}
			return tw.Flush()
		},
	}

	var doProbe bool
	inspectCmd := &cobra.Command{
		Use:   "inspect <domain-or-id>",
		Short: "查看单条路由详情、TLS 证书与 Caddy 配置",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var detail orchestrator.RouteDetail
			method := http.MethodGet
			apiPath := "/api/v1/routes/" + args[0]
			if doProbe {
				method = http.MethodPost
				apiPath += "/probe"
			}
			if _, err := gf.execAPI(cmd.Context(), method, apiPath, nil, &detail); err != nil {
				return err
			}
			if gf.jsonOut {
				return nil
			}
			r := detail.Route
			fmt.Printf("域名       : https://%s (%s)\n", r.FullDomain, r.LifecycleState)
			fmt.Printf("本地目标   : %s://%s:%d (容器=%s, 健康=%v)\n", r.UpstreamScheme, r.LocalTargetHost, r.LocalTargetPort, r.ContainerName, r.LocalHealthy)
			fmt.Printf("转发方式   : %s -> %s (预设=%s)\n", strings.ToUpper(r.ForwardMode), r.EffectiveDial, r.Preset)
			fmt.Printf("边缘节点   : %s (公网 %s, TS %dms, Caddy已同步=%v)\n", r.EdgeNodeName, r.EdgeNodePublicIP, r.EdgeNodeTSRTTMs, r.CaddySynced)
			fmt.Printf("源站证书   : %s (剩余 %d 天, OK=%v)\n", r.OriginCertIssuer, r.OriginCertDays, r.OriginTLSOK)
			fmt.Printf("公网响应   : HTTP %d (延迟 %dms, CDN=%v, 证书=%s)\n", r.PublicHTTPCode, r.PublicLatencyMs, r.DNSProxied, r.PublicCertIssuer)
			if r.LastError != "" {
				fmt.Printf("最近告警   : %s\n", r.LastError)
			}
			return nil
		},
	}
	inspectCmd.Flags().BoolVar(&doProbe, "probe", false, "立即触发一次实时检测再返回结果")

	var proxyOn, proxyOff bool
	proxyCmd := &cobra.Command{
		Use:   "proxy <domain-or-id>",
		Short: "开启或关闭路由的 Cloudflare CDN 代理",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if proxyOn == proxyOff {
				return &CLIExitError{ExitCode: 2, Err: fmt.Errorf("请明确指定 --on 或 --off")}
			}
			var rt store.Route
			_, err := gf.execAPI(cmd.Context(), http.MethodPatch, "/api/v1/routes/"+args[0]+"/proxy", map[string]bool{"proxied": proxyOn}, &rt)
			if err == nil && !gf.jsonOut {
				fmt.Printf("已更新 %s 的 CDN 代理状态 -> proxied=%v\n", rt.FullDomain, rt.DNSProxied)
			}
			return err
		},
	}
	proxyCmd.Flags().BoolVar(&proxyOn, "on", false, "开启 Cloudflare CDN 代理")
	proxyCmd.Flags().BoolVar(&proxyOff, "off", false, "关闭代理，恢复 DNS 直连")

	pauseCmd := &cobra.Command{
		Use:   "pause <domain-or-id>",
		Short: "临时暂停路由转发（保留 DNS 记录与本地配置）",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var rt store.Route
			_, err := gf.execAPI(cmd.Context(), http.MethodPost, "/api/v1/routes/"+args[0]+"/pause", nil, &rt)
			if err == nil && !gf.jsonOut {
				fmt.Printf("已暂停路由 %s\n", rt.FullDomain)
			}
			return err
		},
	}

	resumeCmd := &cobra.Command{
		Use:   "resume <domain-or-id>",
		Short: "恢复已暂停的路由并重新挂载到边缘节点",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var rt store.Route
			_, err := gf.execAPI(cmd.Context(), http.MethodPost, "/api/v1/routes/"+args[0]+"/resume", nil, &rt)
			if err == nil && !gf.jsonOut {
				fmt.Printf("已恢复路由 %s (%s)\n", rt.FullDomain, rt.LifecycleState)
			}
			return err
		},
	}

	retryCmd := &cobra.Command{
		Use:   "retry <domain-or-id>",
		Short: "重新同步单条路由的 DNS 与边缘 Caddy 配置并立即巡检",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var rt store.Route
			_, err := gf.execAPI(cmd.Context(), http.MethodPost, "/api/v1/routes/"+args[0]+"/retry", nil, &rt)
			if err == nil && !gf.jsonOut {
				fmt.Printf("已重推并验证路由 %s -> %s\n", rt.FullDomain, rt.LifecycleState)
			}
			return err
		},
	}

	var rmYes, keepDNS bool
	rmCmd := &cobra.Command{
		Use:   "rm <domain-or-id>",
		Short: "彻底下线路由并自动清理边缘 Caddy 与 DNS 解析记录",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			_ = rmYes // Accepted for non-interactive script ergonomics.
			q := "?delete_dns=true"
			if keepDNS {
				q = "?delete_dns=false"
			}
			_, err := gf.execAPI(cmd.Context(), http.MethodDelete, "/api/v1/routes/"+args[0]+q, nil, nil)
			if err == nil && !gf.jsonOut {
				fmt.Printf("已成功下线并移除路由 %s\n", args[0])
			}
			return err
		},
	}
	rmCmd.Flags().BoolVarP(&rmYes, "yes", "y", false, "跳过交互确认直接删除")
	rmCmd.Flags().BoolVar(&keepDNS, "keep-dns", false, "仅移除边缘反代规则，保留远端 DNS 记录")

	routeCmd.AddCommand(lsCmd, inspectCmd, proxyCmd, pauseCmd, resumeCmd, retryCmd, rmCmd)
	return routeCmd
}

func newNodeCmd(gf *globalFlags) *cobra.Command {
	nodeCmd := &cobra.Command{
		Use:   "node",
		Short: "管理边缘公网节点与 Caddy 部署配置",
	}

	lsCmd := &cobra.Command{
		Use:   "ls",
		Short: "列出所有已注册边缘节点",
		RunE: func(cmd *cobra.Command, _ []string) error {
			var nodes []store.EdgeNode
			if _, err := gf.execAPI(cmd.Context(), http.MethodGet, "/api/v1/nodes", nil, &nodes); err != nil {
				return err
			}
			if gf.jsonOut {
				return nil
			}
			tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
			fmt.Fprintln(tw, "NAME\tPUBLIC IP\tTAILSCALE IP\tTUNNEL\tROUTES\tSTATUS")
			for _, n := range nodes {
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s (%dms)\t%d\t%s\n",
					n.Name, n.PublicIPv4, n.TailscaleIP, n.TSConnType, n.TSRTTMs, n.RouteCount, n.HealthStatus)
			}
			return tw.Flush()
		},
	}

	var bootName, bootPubIP, bootTSIP, bootSecret string
	bootCmd := &cobra.Command{
		Use:   "bootstrap",
		Short: "生成边缘节点 Caddy 容器启动命令与鉴权配置",
		RunE: func(cmd *cobra.Command, _ []string) error {
			var bundle caddy.BootstrapBundle
			_, err := gf.execAPI(cmd.Context(), http.MethodPost, "/api/v1/nodes/bootstrap", map[string]string{
				"name":         bootName,
				"public_ipv4":  bootPubIP,
				"tailscale_ip": bootTSIP,
				"auth_secret":  bootSecret,
			}, &bundle)
			if err != nil {
				return err
			}
			if gf.jsonOut {
				return nil
			}
			fmt.Printf("# 边缘节点 %s 一键部署命令 (请在边缘 VPS 上运行):\n\n%s\n\n# 节点 Auth Secret: %s\n",
				bundle.NodeName, bundle.QuickCommand, bundle.AuthSecret)
			return nil
		},
	}
	bootCmd.Flags().StringVar(&bootName, "name", "edge-vps", "节点名称")
	bootCmd.Flags().StringVar(&bootPubIP, "public-ip", "", "边缘 VPS 公网 IPv4")
	bootCmd.Flags().StringVar(&bootTSIP, "ts-ip", "", "边缘 VPS Tailscale IP (100.x.x.x)")
	bootCmd.Flags().StringVar(&bootSecret, "secret", "", "自定义共享密钥 (留空自动生成)")

	var addIn orchestrator.AddEdgeNodeInput
	addCmd := &cobra.Command{
		Use:   "add",
		Short: "注册并验证一台边缘公网 VPS 节点",
		RunE: func(cmd *cobra.Command, _ []string) error {
			var node store.EdgeNode
			_, err := gf.execAPI(cmd.Context(), http.MethodPost, "/api/v1/nodes", addIn, &node)
			if err == nil && !gf.jsonOut {
				fmt.Printf("节点 %s 已接入！(状态: %s, RTT: %dms)\n", node.Name, node.HealthStatus, node.TSRTTMs)
			}
			return err
		},
	}
	addCmd.Flags().StringVar(&addIn.Name, "name", "", "节点标识名称 (如 tokyo-vps)")
	addCmd.Flags().StringVar(&addIn.Region, "region", "", "区域备注 (如 Tokyo)")
	addCmd.Flags().StringVar(&addIn.PublicIPv4, "public-ip", "", "节点公网 IPv4")
	addCmd.Flags().StringVar(&addIn.TailscaleIP, "ts-ip", "", "节点 Tailscale IP")
	addCmd.Flags().StringVar(&addIn.CaddyAdminURL, "admin-url", "", "Caddy Admin 地址 (默认 http://<ts-ip>:2019)")
	addCmd.Flags().StringVar(&addIn.AuthSecret, "secret", "", "边缘 Caddy 守卫 Bearer 密钥")
	addCmd.Flags().BoolVar(&addIn.IsDefault, "default", false, "设为默认边缘节点")

	pingCmd := &cobra.Command{
		Use:   "ping <name-or-id>",
		Short: "立即拨测边缘节点 Tailscale 延迟与 Caddy Admin 连通性",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var node store.EdgeNode
			_, err := gf.execAPI(cmd.Context(), http.MethodPost, "/api/v1/nodes/"+args[0]+"/ping", nil, &node)
			if err == nil && !gf.jsonOut {
				fmt.Printf("节点 %s: 状态=%s, 隧道=%s (%dms), Caddy=%s\n",
					node.Name, node.HealthStatus, node.TSConnType, node.TSRTTMs, node.CaddyVersion)
			}
			return err
		},
	}

	reconcileCmd := &cobra.Command{
		Use:   "reconcile <name-or-id>",
		Short: "一键重推该节点所有启用路由，修复边缘 Caddy 配置漂移",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var res struct {
				Node         store.EdgeNode `json:"node"`
				PushedRoutes int            `json:"pushed_routes"`
			}
			_, err := gf.execAPI(cmd.Context(), http.MethodPost, "/api/v1/nodes/"+args[0]+"/reconcile", nil, &res)
			if err == nil && !gf.jsonOut {
				fmt.Printf("节点 %s 对账修复完成，已重推 %d 条路由。\n", res.Node.Name, res.PushedRoutes)
			}
			return err
		},
	}

	rmCmd := &cobra.Command{
		Use:   "rm <name-or-id>",
		Short: "移除空闲的边缘节点",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			_, err := gf.execAPI(cmd.Context(), http.MethodDelete, "/api/v1/nodes/"+args[0], nil, nil)
			if err == nil && !gf.jsonOut {
				fmt.Printf("已删除边缘节点 %s\n", args[0])
			}
			return err
		},
	}

	nodeCmd.AddCommand(lsCmd, bootCmd, addCmd, pingCmd, reconcileCmd, rmCmd)
	return nodeCmd
}

func newDNSCmd(gf *globalFlags) *cobra.Command {
	dnsCmd := &cobra.Command{
		Use:   "dns",
		Short: "管理 Cloudflare / 腾讯云 DNSPod 凭据与可用根域名",
	}

	lsCmd := &cobra.Command{
		Use:   "ls",
		Short: "列出所有已同步根域名与所属 DNS 凭据",
		RunE: func(cmd *cobra.Command, _ []string) error {
			var zones []store.DNSZone
			if _, err := gf.execAPI(cmd.Context(), http.MethodGet, "/api/v1/dns/zones", nil, &zones); err != nil {
				return err
			}
			if gf.jsonOut {
				return nil
			}
			tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
			fmt.Fprintln(tw, "ZONE\tPROVIDER\tCDN PROXY\tROUTES\tDEFAULT")
			for _, z := range zones {
				fmt.Fprintf(tw, "%s\t%s (%s)\t%v\t%d\t%v\n",
					z.ZoneName, z.ProviderName, z.ProviderType, z.SupportsProxy, z.RouteCount, z.IsDefault)
			}
			return tw.Flush()
		},
	}

	var cfName, cfToken, cfBaseURL string
	addCFCmd := &cobra.Command{
		Use:   "add-cf",
		Short: "添加 Cloudflare API Token 并自动同步根域名列表",
		RunE: func(cmd *cobra.Command, _ []string) error {
			var res struct {
				Provider store.DNSProviderRecord `json:"provider"`
				Zones    []store.DNSZone         `json:"zones"`
			}
			_, err := gf.execAPI(cmd.Context(), http.MethodPost, "/api/v1/dns/providers", orchestrator.AddDNSProviderInput{
				Name:         cfName,
				ProviderType: "cloudflare",
				APIToken:     cfToken,
				BaseURL:      cfBaseURL,
			}, &res)
			if err == nil && !gf.jsonOut {
				fmt.Printf("Cloudflare 凭据 %s 已接入，共发现 %d 个可用域名。\n", res.Provider.Name, len(res.Zones))
			}
			return err
		},
	}
	addCFCmd.Flags().StringVar(&cfName, "name", "Cloudflare", "凭据备注名称")
	addCFCmd.Flags().StringVar(&cfToken, "api-token", "", "Cloudflare API Token (需 Zone:Read + DNS:Edit)")
	addCFCmd.Flags().StringVar(&cfBaseURL, "base-url", "", "可选自定义 API 地址")

	var dpName, dpID, dpKey, dpBaseURL string
	addDPCmd := &cobra.Command{
		Use:   "add-dnspod",
		Short: "添加腾讯云 DNSPod API 3.0 凭据并同步根域名列表",
		RunE: func(cmd *cobra.Command, _ []string) error {
			var res struct {
				Provider store.DNSProviderRecord `json:"provider"`
				Zones    []store.DNSZone         `json:"zones"`
			}
			_, err := gf.execAPI(cmd.Context(), http.MethodPost, "/api/v1/dns/providers", orchestrator.AddDNSProviderInput{
				Name:         dpName,
				ProviderType: "dnspod",
				SecretID:     dpID,
				SecretKey:    dpKey,
				BaseURL:      dpBaseURL,
			}, &res)
			if err == nil && !gf.jsonOut {
				fmt.Printf("DNSPod 凭据 %s 已接入，共发现 %d 个可用域名。\n", res.Provider.Name, len(res.Zones))
			}
			return err
		},
	}
	addDPCmd.Flags().StringVar(&dpName, "name", "DNSPod", "凭据备注名称")
	addDPCmd.Flags().StringVar(&dpID, "secret-id", "", "腾讯云 SecretId")
	addDPCmd.Flags().StringVar(&dpKey, "secret-key", "", "腾讯云 SecretKey")
	addDPCmd.Flags().StringVar(&dpBaseURL, "base-url", "", "可选自定义 API 地址")

	syncCmd := &cobra.Command{
		Use:   "sync <provider-id>",
		Short: "重新同步指定 DNS 凭据下的根域名列表",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			_, err := gf.execAPI(cmd.Context(), http.MethodPost, "/api/v1/dns/providers/"+args[0]+"/sync", nil, nil)
			if err == nil && !gf.jsonOut {
				fmt.Println("域名列表同步完成。")
			}
			return err
		},
	}

	rmCmd := &cobra.Command{
		Use:   "rm <provider-id>",
		Short: "删除指定的 DNS 厂商凭据",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			_, err := gf.execAPI(cmd.Context(), http.MethodDelete, "/api/v1/dns/providers/"+args[0], nil, nil)
			if err == nil && !gf.jsonOut {
				fmt.Printf("已删除 DNS 凭据 %s\n", args[0])
			}
			return err
		},
	}

	dnsCmd.AddCommand(lsCmd, addCFCmd, addDPCmd, syncCmd, rmCmd)
	return dnsCmd
}

func newTokenCmd(gf *globalFlags) *cobra.Command {
	tokCmd := &cobra.Command{
		Use:   "token",
		Short: "管理 AI Agent 与自动化脚本专用的 API Token",
	}

	lsCmd := &cobra.Command{
		Use:   "ls",
		Short: "列出所有已签发 API Token",
		RunE: func(cmd *cobra.Command, _ []string) error {
			var list []store.APIToken
			if _, err := gf.execAPI(cmd.Context(), http.MethodGet, "/api/v1/tokens", nil, &list); err != nil {
				return err
			}
			if gf.jsonOut {
				return nil
			}
			tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
			fmt.Fprintln(tw, "ID\tNAME\tPREFIX\tSCOPE\tCREATED")
			for _, t := range list {
				fmt.Fprintf(tw, "%s\t%s\t%s...\t%s\t%s\n",
					t.ID, t.Name, t.TokenPrefix, t.Scope, t.CreatedAt.Format("2006-01-02 15:04"))
			}
			return tw.Flush()
		},
	}

	var name, scope string
	createCmd := &cobra.Command{
		Use:   "create",
		Short: "生成新的 API Token（明文仅展示一次）",
		RunE: func(cmd *cobra.Command, _ []string) error {
			var res struct {
				Token     store.APIToken `json:"token"`
				PlainText string         `json:"plain_text"`
			}
			if _, err := gf.execAPI(cmd.Context(), http.MethodPost, "/api/v1/tokens", map[string]string{
				"name":  name,
				"scope": scope,
			}, &res); err != nil {
				return err
			}
			if gf.jsonOut {
				return nil
			}
			fmt.Printf("API Token 已签发（请立即妥善保存，仅显示一次）：\n\n  %s\n\n", res.PlainText)
			return nil
		},
	}
	createCmd.Flags().StringVar(&name, "name", "ai-agent", "Token 备注名称")
	createCmd.Flags().StringVar(&scope, "scope", "admin", "权限范围: admin | readonly")

	rmCmd := &cobra.Command{
		Use:   "rm <token-id>",
		Short: "吊销指定的 API Token",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			_, err := gf.execAPI(cmd.Context(), http.MethodDelete, "/api/v1/tokens/"+args[0], nil, nil)
			if err == nil && !gf.jsonOut {
				fmt.Printf("已吊销 Token %s\n", args[0])
			}
			return err
		},
	}

	tokCmd.AddCommand(lsCmd, createCmd, rmCmd)
	return tokCmd
}

func newDoctorCmd(gf *globalFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "doctor",
		Short: "执行全栈环境自检（SQLite、Docker、Tailscale、Local Relay、DNS 与边缘节点）",
		RunE: func(cmd *cobra.Command, _ []string) error {
			var rep orchestrator.DoctorReport
			if _, err := gf.execAPI(cmd.Context(), http.MethodGet, "/api/v1/doctor", nil, &rep); err != nil {
				return err
			}
			if gf.jsonOut {
				return nil
			}
			for _, c := range rep.Checks {
				tag := "[OK]  "
				if c.Status == "warn" {
					tag = "[WARN]"
				} else if c.Status == "error" {
					tag = "[ERR] "
				}
				fmt.Printf("%s %-26s %s\n", tag, c.Component, c.Detail)
				if c.Hint != "" {
					fmt.Printf("       -> 提示: %s\n", c.Hint)
				}
			}
			if !rep.Healthy {
				return &CLIExitError{ExitCode: 1, Err: fmt.Errorf("doctor detected unhealthy components")}
			}
			return nil
		},
	}
}

func newUpdateCmd(gf *globalFlags) *cobra.Command {
	var apply bool
	cmd := &cobra.Command{
		Use:   "update",
		Short: "检查小凡兔新版本，二进制部署可加 --apply 一键更新并重启",
		Example: `  vantu update
  vantu update --apply`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			var info update.Info
			if _, err := gf.execAPI(cmd.Context(), http.MethodGet, "/api/v1/update?refresh=1", nil, &info); err != nil {
				return err
			}
			if !apply {
				if gf.jsonOut {
					return nil
				}
				fmt.Printf("当前版本: %s\n", info.CurrentVersion)
				switch {
				case info.CheckError != "":
					fmt.Printf("检查失败: %s\n", info.CheckError)
				case info.HasUpdate:
					fmt.Printf("发现新版本: %s  %s\n", info.LatestVersion, info.ReleaseURL)
					if info.CanSelfUpdate {
						fmt.Println("执行 vantu update --apply 一键更新")
					} else if info.UpgradeCommand != "" {
						fmt.Printf("升级命令: %s\n", info.UpgradeCommand)
					} else {
						fmt.Println(info.SelfUpdateNote)
					}
				default:
					fmt.Printf("已是最新版本 (最新发布 %s)\n", info.LatestVersion)
				}
				return nil
			}
			var res struct {
				From string `json:"from"`
				To   string `json:"to"`
			}
			if _, err := gf.execAPI(cmd.Context(), http.MethodPost, "/api/v1/update/apply", nil, &res); err != nil {
				return err
			}
			if !gf.jsonOut {
				fmt.Printf("已从 %s 更新至 %s，服务正在重启。\n", res.From, res.To)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&apply, "apply", false, "下载、校验并替换二进制，随后自动重启服务")
	return cmd
}

func newSkillCmd(gf *globalFlags) *cobra.Command {
	skillCmd := &cobra.Command{
		Use:   "skill",
		Short: "导出供 Claude Code / Codex / Cursor / Antigravity 使用的 SKILL.md 规范文档",
	}
	var outPath string
	exportCmd := &cobra.Command{
		Use:   "export",
		Short: "输出或保存 AI Agent 专用 SKILL.md",
		RunE: func(_ *cobra.Command, _ []string) error {
			ep := gf.endpoint
			if ep == "" {
				ep = os.Getenv("VANTU_ENDPOINT")
			}
			md := skilldoc.Render(ep)
			if outPath != "" {
				if err := os.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
					return err
				}
				if err := os.WriteFile(outPath, []byte(md), 0o644); err != nil {
					return err
				}
				if gf.jsonOut {
					b, _ := json.MarshalIndent(map[string]any{"ok": true, "data": map[string]any{"written_to": outPath}}, "", "  ")
					fmt.Println(string(b))
				} else {
					fmt.Printf("SKILL.md 已导出至 %s\n", outPath)
				}
				return nil
			}
			if gf.jsonOut {
				b, _ := json.MarshalIndent(map[string]any{"ok": true, "data": map[string]any{"skill_md": md}}, "", "  ")
				fmt.Println(string(b))
				return nil
			}
			fmt.Print(md)
			return nil
		},
	}
	exportCmd.Flags().StringVarP(&outPath, "out", "o", "", "写入目标文件路径 (例如 ~/.claude/skills/vantu-publish/SKILL.md)")
	skillCmd.AddCommand(exportCmd)
	return skillCmd
}
