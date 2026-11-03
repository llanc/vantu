# 小凡兔 · Vantu

> **轻量级云边隧道与反向代理自动化编排工具** — 5 秒将本地未暴露的 Docker 容器或内网端口发布为公网 HTTPS 服务，原生支持 Web 控制台与 AI Coding Agent CLI (`vantu` / `xft`)。

---

## 核心特性

1. **三合一端到端自动编排**：一次点击或一条命令自动完成 **DNS 解析写入 (Cloudflare / DNSPod)** → **Tailscale 内网直连或安全桥接** → **边缘公网 VPS Caddy `@id` 原子路由与 ACME 证书签发**。
2. **零自定义边缘 Agent**：公网 VPS 只需运行官方纯净 `caddy:2.8-alpine` 容器，小凡兔内置自动生成带 Tailscale 网段白名单与 Bearer 密钥守卫的 `docker-compose.yml` 与 `bootstrap.json`。
3. **内置 Local Relay 安全桥接 (`:18080`)**：本地容器哪怕未映射 `-p` 端口或仅监听 `127.0.0.1`，也无需重启容器或暴露全网卡，直接由小凡兔通过单端口多路复用 + HMAC 签名安全穿透。
4. **动态实景链路与源站证书巡检**：按每条路由真实的转发路径动态呈现 **3 / 4 / 5 跳**（是否经过 `Local Relay` 与 `Cloudflare CDN`）；开启 CDN 时还会绕过代理直连边缘节点 `:443` 核验源站证书剩余天数。
5. **AI Agent First 双命令体验**：官方命令 `vantu`（内置短别名软链 `xft`），全命令支持 `--json` 信封、`--dry-run` 预演、确定性退出码与一键导出 `SKILL.md`。
6. **单静态二进制 + 单实例 WAL SQLite**：`CGO_ENABLED=0` 纯 Go 编译内嵌 Vue 3 + TypeScript 控制台，空闲内存 `< 30MB`。

---

## 快速启动

### 方式一：Docker Compose 一键部署（推荐）

```bash
docker compose up -d
```

打开浏览器访问 `http://<本机IP>:8089`，完成首次管理员口令初始化即可使用。容器内可直接执行：

```bash
docker exec -it vantu vantu capabilities --json
# 或使用等价短别名：
docker exec -it vantu xft doctor
```

### 方式二：从源码构建单文件二进制

```bash
make build
./vantu serve --listen 0.0.0.0:8089 --data-dir ./data
```

---

## AI CLI (`vantu` / `xft`) 常用示例

```bash
# 1. 查看系统全局就绪能力（可用根域名、边缘节点、待发布容器）
vantu capabilities --json

# 2. 零副作用预览发布计划与生成的 Caddy JSON 块
vantu publish --target container:sub2api --domain api.xiaofantu.com --preset sse --dry-run

# 3. 正式发布服务并开启 Cloudflare 小黄云代理
vantu publish --target 127.0.0.1:8080 --domain demo.xiaofantu.com --proxied --wait

# 4. 查看单条路由详情并触发实时检测
vantu route inspect demo.xiaofantu.com --probe

# 5. 导出供 Claude Code / Codex / Cursor / Antigravity 使用的 SKILL.md
vantu skill export -o ~/.claude/skills/vantu-publish/SKILL.md
```

---

## 设计与架构文档索引

- [docs/README.md](./docs/README.md)：项目产品总览与设计决策
- [docs/01-PRD.md](./docs/01-PRD.md)：产品需求规格说明书 (`FR-01` ~ `FR-09`)
- [docs/02-ARCHITECTURE.md](./docs/02-ARCHITECTURE.md)：系统架构、SQLite 数据模型、REST/SSE API 与 CLI 规范
- [docs/03-UI-UX-SPEC.md](./docs/03-UI-UX-SPEC.md)：高密度瑞士网格与动态链路拓扑交互设计规范
