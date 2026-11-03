package skilldoc

import "fmt"

// Render returns the complete SKILL.md specification for AI Coding Agents (Claude Code, Codex, Cursor, Antigravity).
func Render(serverEndpoint string) string {
	if serverEndpoint == "" {
		serverEndpoint = "http://127.0.0.1:8089"
	}
	return fmt.Sprintf(`---
name: vantu-publish
description: 使用小凡兔 (Vantu / xft) 将本地 Docker 容器或内网端口安全发布到公网 HTTPS 域名，自动编排 DNS 解析、Tailscale 内网穿透与边缘节点 Caddy 反代证书。当用户要求把某个本地服务/容器暴露到公网、绑定域名、开启/关闭 Cloudflare CDN、检查证书有效期或下线公网服务时使用。
---

# 小凡兔 Vantu AI CLI 操作指南

小凡兔提供声明式 CLI 工具 `+"`vantu`"+`（短别名 `+"`xft`"+`）。在本机或容器内执行时，CLI 优先通过本地 Unix Socket 通信；跨机器远程调用可配置环境变量 `+"`VANTU_ENDPOINT=%s`"+` 与 `+"`VANTU_TOKEN=vtu_xxx`"+`。

## 黄金安全准则（Agent 必读）

1. **先探测环境能力**：操作前先运行 `+"`vantu capabilities --json`"+`，一次性获取可用根域名 (`+"`zones`"+`)、边缘节点 (`+"`nodes`"+`)、未暴露容器 (`+"`containers`"+`) 与默认配置。
2. **变更前先 Dry-Run**：执行 `+"`publish`"+` 前先带 `+"`--dry-run --json`"+` 预览将要执行的 DNS 记录、转发模式 (`+"`direct`"+` vs `+"`relay`"+`) 与边缘 Caddy JSON 片段。
3. **始终启用结构化输出**：所有命令支持 `+"`--json`"+`（或环境变量 `+"`VANTU_OUTPUT=json`"+`），返回统一的 `+"`{\"ok\": bool, \"data\": ..., \"error\": ...}`"+` JSON 信封。
4. **切勿擅自覆盖外部 DNS**：若返回退出码 `+"`4`"+` (`+"`DNS_CONFLICT`"+`)，必须先向用户确认是否允许接管已有同名记录，获得同意后再加 `+"`--adopt-dns`"+`。

## 核心命令速查

### 1. 环境感知与自检
`+"```bash"+`
# 一次性读取可用域名、边缘节点、Tailscale 状态与本地容器
vantu capabilities --json

# 仅列出本机运行中的 Docker 容器及可达性分类
vantu docker ls --json

# 全链路系统自检（SQLite、Docker、Tailscale、Relay、DNS、边缘节点）
vantu doctor --json
`+"```"+`

### 2. 一键发布服务到公网
`+"```bash"+`
# 首次无头安装时初始化管理员密码，并将小凡兔自身控制台发布到公网 HTTPS
vantu admin passwd --generate --json
vantu publish --target self --domain vantu.example.com --proxied --json

# 预览发布计划（零副作用）
vantu publish --target container:sub2api --domain api.example.com --preset sse --dry-run --json

# 正式发布 Docker 容器（未映射 -p 端口会自动走 Local Relay 安全桥接，无需重启容器）
vantu publish --target container:sub2api --domain api.example.com --preset sse --wait --json

# 发布本地端口或局域网 NAS / PVE 自签名后台
vantu publish --target 127.0.0.1:8080 --domain demo.example.com --proxied --json
vantu publish --target https://192.168.1.2:8006 --domain pve.example.com --preset https_insecure --json
`+"```"+`

**协议预设 (`+"`--preset`"+`) 选择说明：**
- `+"`web`"+`（默认）：常规前后端应用，原生支持 WebSocket 双向升级。
- `+"`sse`"+`：AI 大模型网关 (`+"`sub2api`"+` / `+"`one-api`"+` / `+"`ollama`"+` / `+"`dify`"+`)，自动开启 `+"`flush_interval: -1`"+` 零缓冲流式输出与 600s 长超时。
- `+"`webdav`"+`：网盘/相册 (`+"`alist`"+` / `+"`immich`"+` / `+"`nextcloud`"+`)，放宽请求体限制并设置 3600s 读写超时。
- `+"`https_insecure`"+`：内网自签名 HTTPS 服务 (`+"`Proxmox`"+` / `+"`Portainer:9443`"+` / `+"`DSM`"+`)，自动忽略内网自签证书校验并在公网重签正规证书。

### 3. 路由巡检与生命周期管理
`+"```bash"+`
# 查看所有已发布路由与运行状态
vantu route ls --json

# 查看单条路由详情并触发实时检测（含源站证书剩余天数）
vantu route inspect api.example.com --probe --json

# 开启或关闭 Cloudflare CDN 代理
vantu route proxy api.example.com --on --json
vantu route proxy api.example.com --off --json

# 暂停 / 恢复 / 漂移修复重推 / 彻底下线（含自动清理 DNS 记录）
vantu route pause api.example.com --json
vantu route resume api.example.com --json
vantu route retry api.example.com --json
vantu route rm api.example.com --yes --json
`+"```"+`

### 4. 边缘节点对账修复
`+"```bash"+`
vantu node ls --json
vantu node ping tokyo-vps --json
vantu node reconcile tokyo-vps --json
`+"```"+`

## 确定性退出码规范

| Exit Code | 错误码 (`+"`error.code`"+`) | 含义与处理建议 |
| :--- | :--- | :--- |
| `+"`0`"+` | - | 执行成功 |
| `+"`2`"+` | `+"`VALIDATION_ERROR`"+` / `+"`NOT_FOUND`"+` | 参数缺失或资源不存在，先查 `+"`vantu capabilities --json`"+` |
| `+"`3`"+` | `+"`AUTH_REQUIRED`"+` | Token 缺失或权限不足，检查 `+"`VANTU_TOKEN`"+` |
| `+"`4`"+` | `+"`DNS_ERROR`"+` / `+"`DNS_CONFLICT`"+` | DNS API 报错或存在外部同名记录冲突 |
| `+"`5`"+` | `+"`EDGE_UNREACHABLE`"+` | 边缘 VPS Caddy 不可达（已自动回滚新增 DNS），检查节点状态 |
| `+"`6`"+` | `+"`LOCAL_TARGET_DOWN`"+` | 本地容器或端口未监听，先启动本地服务再重试 |
| `+"`7`"+` | `+"`TLS_TIMEOUT`"+` | 路由已下发但等待 ACME 证书超时，稍后用 `+"`route inspect`"+` 复查 |
`, serverEndpoint)
}
