# 小凡兔 · Vantu

[![CI](https://github.com/llanc/vantu/actions/workflows/ci.yml/badge.svg)](https://github.com/llanc/vantu/actions/workflows/ci.yml)
[![Go](https://img.shields.io/badge/Go-1.27+-00ADD8?logo=go&logoColor=white)](./go.mod)
[![Vue](https://img.shields.io/badge/Vue-3.5-4FC08D?logo=vuedotjs&logoColor=white)](./web/package.json)
[![Docker](https://img.shields.io/badge/Docker-ghcr.io%2Fllanc%2Fvantu-2496ED?logo=docker&logoColor=white)](./Dockerfile)
[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](./LICENSE)

**简体中文** | [English](./README.en.md)

轻量级云边隧道与反向代理编排工具。将本地 Docker 容器或内网端口发布为公网 HTTPS 服务，同时提供 Web 控制台与命令行 (`vantu` / `xft`)。

---

## 核心特性

1. **端到端路由编排**：通过单次操作完成 **DNS 记录同步（Cloudflare / 腾讯云 DNSPod）** → **Tailscale 内网直连或本地安全桥接** → **边缘 VPS Caddy `@id` 原子路由下发与 ACME 证书签发**。
2. **边缘节点免自定义 Agent**：边缘公网 VPS 仅需运行官方 `caddy:2.8-alpine` 容器，Vantu 可直接生成带 Tailscale 网段白名单与 Bearer 密钥守卫的引导配置。
3. **Local Relay 本地桥接 (`:18080`)**：对未映射宿主机端口（无 `-p`）或仅监听 `127.0.0.1` 的本地容器，通过单端口多路复用与 `X-Vantu-Relay-Key` HMAC 签名转发，无需重启容器或暴露宿主机端口。
4. **动态跳数链路与源站证书巡检**：根据每条路由的实际配置动态展示 **3 / 4 / 5 跳**拓扑（按需包含 `Local Relay` 与 `Cloudflare CDN`）；开启 CDN 代理时支持绕过 CDN 直连边缘节点 `:443` 校验源站证书剩余有效期。
5. **CLI 与 AI Agent 友好**：提供 `vantu`（及短别名 `xft`），全命令支持 `--json` 结构化输出、`--dry-run` 变更预演、确定性退出码（`0~7`）与 `SKILL.md` 导出。
6. **单文件交付**：`CGO_ENABLED=0` 静态编译，内嵌 Vue 3 + TypeScript 控制台与纯 Go SQLite（WAL 模式）。

---

## 快速开始

### 方式一：Docker Compose 部署（推荐）

```bash
docker compose up -d
```

默认监听 `http://<本机IP>:8089`，首次打开浏览器可完成管理员密码初始化。容器内内置 `vantu` 与 `xft` 命令：

```bash
docker exec -it vantu xft capabilities --json
docker exec -it vantu xft doctor
```

### 方式二：从源码构建单文件二进制

环境要求：Go `1.27+`、Node.js `20+`、pnpm `12+`。

```bash
make build
./vantu serve --listen 0.0.0.0:8089 --data-dir ./data
```

### 部署后将控制台自身发布到公网（自代理）

适用于无图形界面或无法直接访问 `:8089` 的远程主机。以下命令在部署 Vantu 的本机执行，CLI 默认通过本地 Unix Socket 免密通信（Docker 部署时在命令前加 `docker exec -it vantu`）：

```bash
# 1. 初始化管理员密码（--generate 自动生成随机密码并打印，或用 --password 指定）
xft admin passwd --generate

# 2. 添加 DNS 凭据
xft dns add-cf --api-token <CLOUDFLARE_API_TOKEN>
# 或：xft dns add-dnspod --secret-id <ID> --secret-key <KEY>

# 3. 生成边缘节点 Caddy 部署命令，复制到边缘 VPS 上执行
xft node bootstrap --name edge --public-ip <边缘公网IP> --ts-ip <边缘TailscaleIP>

# 4. 注册边缘节点（--secret 填入上一步输出的 Auth Secret）
xft node add --name edge --public-ip <边缘公网IP> --ts-ip <边缘TailscaleIP> --secret <AUTH_SECRET> --default

# 5. 将控制台自身发布到公网 HTTPS
xft publish --target self --domain vantu.example.com --proxied --wait
```

说明：
- `--target self` 自动解析当前实例的监听端口并启用 `sse` 预设；省略 `--domain` 时默认使用 `vantu.<默认根域名>`。
- 未初始化管理员密码前，`--target self` 会拒绝执行，防止初始化页面暴露到公网。
- 忘记密码时，可在本机执行 `xft admin passwd --password <新密码>` 重置。
- Web 控制台「发布服务」弹窗中也可选择「本机控制台」完成相同配置。

---

## CLI (`vantu` / `xft`) 常用示例

```bash
# 1. 查看当前可用根域名、边缘节点与本机容器列表
xft capabilities --json

# 2. 预览发布计划与生成的 Caddy 路由 JSON（零副作用）
xft publish --target container:sub2api --domain api.example.com --preset sse --dry-run

# 3. 发布本地服务并开启 Cloudflare CDN 代理
xft publish --target 127.0.0.1:8080 --domain demo.example.com --proxied --wait

# 4. 查看单条路由详情并触发实时链路拨测
xft route inspect demo.example.com --probe

# 5. 导出供 Claude Code / Cursor / Codex 使用的 SKILL.md
xft skill export -o ~/.claude/skills/vantu-publish/SKILL.md

# 6. 检查新版本；二进制部署可一键下载、校验并重启
xft update
xft update --apply
```

### 版本更新

控制台每 12 小时检查一次 GitHub Releases，发现新版本时在顶栏提示，「设置 → 版本与更新」可查看更新说明。

- **二进制部署**：点击「更新至 vX.Y.Z」或执行 `xft update --apply`，会下载当前平台的发布包并用 `checksums.txt` 校验 SHA-256，再原子替换可执行文件并原地重启（PID 不变，systemd 无感知）。运行用户需要对二进制所在目录有写权限。
- **Docker 部署**：容器内不做自替换，控制台会给出升级命令：`docker compose pull vantu && docker compose up -d vantu`。
- 设置 `VANTU_UPDATE_CHECK=off` 可完全关闭版本检查（离线环境适用）。

### 界面语言

默认中文，支持英文。Web 控制台点击顶栏「EN / 中」切换，后端提示、操作记录与 AI 提示词同步切换。CLI 使用 `--lang en` 或环境变量 `VANTU_LANG=en` 输出英文（帮助文本、服务端报错、`skill export` 均生效）；对 `vantu serve` 设置 `VANTU_LANG=en` 时守护进程日志也为英文。

### 本地 Socket 与远程连接解析顺序

当未显式指定 `--endpoint` 或 `VANTU_ENDPOINT` 时，CLI 按以下顺序查找本地 Unix Socket 免密通道：

1. `--socket <路径>` 命令行参数
2. `VANTU_SOCK_PATH` 环境变量
3. `$VANTU_DATA_DIR/vantu.sock`
4. `./data/vantu.sock`
5. `/data/vantu.sock`

若以上 Socket 均不存在，则回退至 `~/.config/vantu/credentials.json`（由 `vantu login` 生成）或 `http://127.0.0.1:8089`。

---

## 环境变量说明

| 变量名 | 默认值 | 说明 |
| :--- | :--- | :--- |
| `VANTU_LISTEN_ADDR` | `0.0.0.0:8089` | Web 控制台与 HTTP API 监听地址 |
| `VANTU_DATA_DIR` | `./data`（容器内为 `/data`） | 数据目录（存放 SQLite 数据库、主密钥与 Unix Socket） |
| `VANTU_DB_PATH` | `<VANTU_DATA_DIR>/vantu.db` | SQLite 数据库文件路径 |
| `VANTU_SOCK_PATH` | `<VANTU_DATA_DIR>/vantu.sock` | 本地管理 Unix Domain Socket 路径 |
| `VANTU_RELAY_PORT` | `18080` | Local Relay 安全桥接监听端口 |
| `VANTU_RELAY_BIND_IP` | 自动取本机 Tailscale IP | Local Relay 绑定网卡地址 |
| `VANTU_DOCKER_SOCK` | `/var/run/docker.sock` | Docker Engine Socket 路径 |
| `VANTU_TAILSCALE_SOCK` | `/var/run/tailscale/tailscaled.sock` | Tailscale LocalAPI Socket 路径 |
| `VANTU_PROBE_INTERVAL_SEC` | `60` | 后台链路与证书巡检周期（秒，最小 `10`） |
| `VANTU_UPDATE_CHECK` | `on` | 设为 `off` 关闭 GitHub Releases 版本检查 |
| `VANTU_MASTER_KEY` | 自动生成 `<VANTU_DATA_DIR>/.master.key` | 凭据加密主密钥（可选通过环境变量外部注入） |
| `VANTU_ADMIN_PASSWORD` | 空 | 首次启动时自动初始化的管理员密码（可选） |

---

## 开发与安全文档

- [CONTRIBUTING.md](./CONTRIBUTING.md)：本地开发、测试与贡献指南
- [SECURITY.md](./SECURITY.md)：安全机制说明与漏洞上报方式

---

## 开源许可

本项目基于 [MIT License](./LICENSE) 开源。
