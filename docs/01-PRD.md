# 小凡兔 · Vantu 产品需求文档（PRD）

* **文档版本**：v1.0
* **产品名称**：小凡兔（英文主名：**Vantu**，CLI 统一主命令：`vantu`，附可选终端软链别名：`xft`）
* **目标形态**：Go 单静态二进制（内嵌 Vue 3 Web 控制台 + AI 友好 CLI）+ 边缘纯净版 Caddy
* **关联文档**：[项目总览](./README.md) · [系统架构设计](./02-ARCHITECTURE.md) · [UI/UX 与原型规范](./03-UI-UX-SPEC.md) · [原始设想](../设想.md)

---

## 1. 产品背景与核心痛点

### 1.1 目标用户画像

| 用户类型 | 典型基础设施现状 | 核心痛点 | Vantu 提供的价值 |
| :--- | :--- | :--- | :--- |
| **自建党 / Homelab 玩家** | 家里或内网服务器跑满数十个 Docker 容器（`sub2api`、`webdav`、Immich、Memos、Portainer），拥有 1~2 台海外/香港轻量云（如 `101.32.x.x`）作为公网入口，通过 Tailscale 组网 | 每新开一个容器都要：查本机 Tailscale IP ➡️ 去 Cloudflare 配解析 ➡️ SSH 登香港机改反代配置 ➡️ `reload` 祈祷别写错语法 ➡️ 手动测试证书是否签出，耗时 5~10 分钟且极易遗忘端口 | **15 秒闭环**：勾选运行中容器 ➡️ 输入子域名 ➡️ 点击发布，全链路自动生效并持续监控 |
| **多云 / 跨境未备案开发者** | 业务算力与存储放在国内高配主机或本地工作站，公网流量由香港/新加坡等多台免备案边缘节点承接 | 多边缘节点缺少统一视图；开启 Cloudflare 小黄云后看不清源站真实证书何时过期；Tailscale 偶发退化为 DERP 中继导致跨境延迟暴增却浑然不知 | 多边缘节点统一纳管、源站+公网双轨巡检、Tailscale 直连/DERP 实时预警 |
| **AI Agent 重度使用者** | 日常使用 Claude Code、Codex、Antigravity 等 Agent 部署调试新服务（如 LLM 网关、Webhook、预览站） | 传统反代面板（如 Nginx Proxy Manager）只有图形界面且依赖臃肿数据库，AI Agent 无法在终端一行命令完成“启动容器 + 绑定公网 HTTPS 域名 + 自检连通性” | **原生 AI 友好 CLI**：`vantu publish` 支持 `--json`、`--dry-run`、上下文自描述与一键导出 `SKILL.md` |

### 1.2 产品边界（Scope & Non-Goals）

* **我们要做的（In Scope）**：
  * 云边 HTTP/HTTPS/WebSocket/SSE 反代路由的**自动化编排、安全穿透与全链路可观测性**；
  * 控制面极度轻量（单容器、SQLite 零外部依赖），边缘面保持官方纯净 Caddy（无自定义边缘 Agent 进程负担，控制面挂掉不影响已发布直连业务流量）。
* **我们不做/暂不做的（Non-Goals in MVP）**：
  * 不做重型企业级多租户 RBAC 权限体系（MVP 聚焦单管理员 + 多 API Token）；
  * 不替代防火墙或重型 WAF 引擎（安全防护交由边缘 Cloudflare 小黄云及 Caddy 基础安全头）；
  * MVP 聚焦七层（HTTP/HTTPS/WS/SSE/gRPC-Web）反代发布，四层纯 TCP/UDP 端口转发列入后续演进（v1.2）。

---

## 2. 核心业务概念定义（Domain Glossary）

1. **控制端（Vantu Core / Origin Node）**：运行在业务容器所在宿主机（如北京本地机）的 Vantu 主程序，负责读取本地 Docker/Tailscale 状态、驱动云端 DNS API 与远端边缘 Caddy Admin API、运行本地端口桥接器与健康巡检器。
2. **边缘节点（Edge Node）**：具备公网 IP（如香港机 `101.32.190.233`）、与控制端处于同一 Tailscale 网络并运行官方纯净版 Caddy 的公网入口服务器。
3. **托管根域名（DNS Zone）**：接入 DNS 服务商（如 Cloudflare）的根域名（如 `xiaofantu.com`），用户发布路由时只需填写子域名前缀（如 `sub`）即可合成完整域名 `sub.xiaofantu.com`。
4. **转发模式（Forwarding Mode）**：
   * **直连模式（`Direct`）**：目标业务端口已在宿主机全网卡（`0.0.0.0`）或 Tailscale 网卡监听，边缘 Caddy 直接经 Tailscale 转发至 `http://<本机Tailscale_IP>:<业务端口>`，性能最高且与 Vantu 进程生命周期解耦。
   * **本地安全桥接模式（`Local Relay`）**：目标容器未映射宿主机端口（仅在 Docker Bridge 内网暴露）或仅绑定 `127.0.0.1` 防内网扫描时，边缘 Caddy 转发至 Vantu 绑定在本机 Tailscale 网卡上的**单一安全中继端口**（如 `<本机Tailscale_IP>:18080`），再由 Vantu 本机转发至 `127.0.0.1:Port` 或容器内网 IP，实现**宿主机零额外端口暴露**。
5. **双轨巡检（Dual-Track Probing）**：同时从“公网真实域名入口”和“边缘节点公网 IP + SNI 直连入口”发起探测，兼顾真实用户体验校验与源站 ACME 证书有效期精确提取。

---

## 3. 功能需求详述（Functional Requirements）

> 优先级说明：**P0** = MVP 必须交付；**P1** = v1.1 紧随迭代；**P2** = v1.2 开源增强演进。

### 3.1 系统初始化与控制台安全鉴权（FR-01 · P0）

* **FR-01.1 首次启动引导向导（Setup Wizard）**
  * 系统首次启动且未检测到管理员账户时，访问 Web 控制台自动拦截进入 3 步极简向导：① 设置管理员密码 ➡️ ②（可选）接入首个 Cloudflare Token 并拉取可用域名 ➡️ ③（可选）添加首台边缘 Caddy 节点并执行连通性测试。
  * 支持通过环境变量（如 `VANTU_ADMIN_PASSWORD`）在无头/自动化环境下静默完成初始化。
* **FR-01.2 Web 登录与会话保护**
  * 登录接口内置防暴力破解限流（如单 IP 连续失败 5 次冷却 5 分钟）；
  * Web 端采用 `HttpOnly + SameSite=Lax` Cookie / JWT 会话机制，支持手动注销与修改密码。
* **FR-01.3 API Token 管理（面向 CLI 与 AI Agent）**
  * 支持创建、查看列表、吊销命名 API Token（如 `claude-code-local`、`ci-deploy`），支持设置过期时间与权限范围（`read-only` 只读诊断 / `admin` 读写编排）；
  * 明文 Token 仅在创建时展示一次，数据库仅存储 SHA-256 摘要与前缀标识（如 `vtu_live_8f92...`）。
* **FR-01.4 敏感凭据落盘加密**
  * 所有第三方密钥（Cloudflare API Token、DNSPod SecretKey、边缘节点 Auth Secret、Tailscale AuthKey）在写入 SQLite 前必须经 **`AES-256-GCM`** 加密；API 响应与日志输出一律脱敏展示（如 `cf_****9a2b`）。

---

### 3.2 边缘代理节点管理（FR-02 · P0）

* **FR-02.1 多节点数据抽象与 CRUD**
  * 支持添加、编辑、启停、删除多台边缘代理节点（如 `香港轻量-01`、`新加坡BGP-02`、`广州备案中转-01`），可指定 1 台为“默认发布节点”。
  * 节点核心属性：节点名称、地域标签、公网 IPv4（选填 IPv6）、Tailscale 内网 IP、Caddy Admin API 地址（默认 `http://<Tailscale_IP>:2019`）、节点鉴权密钥 `Auth Secret`、备注。
* **FR-02.2 零侵入边缘节点一键部署生成**
  * 在“添加边缘节点”界面，根据用户填入的节点 Tailscale IP 自动生成一段**即拷即用的边缘端 `docker-compose.yml` 与引导命令**（内置 `Auth Secret` 校验与持久化 `--resume` 参数），降低边缘机配置门槛且杜绝误将 `:2019` 裸露到公网。
* **FR-02.3 节点连通性与配置漂移对账（Drift Reconcile）**
  * 添加节点时实时校验：① Tailscale 隧道可达性 ➡️ ② Caddy Admin API 鉴权通过 ➡️ ③ 自动初始化 `vantu_srv` HTTP 服务块；
  * 当边缘节点意外丢失规则（例如人为误改或未挂载持久化目录重启）时，系统自动标记节点为 **“配置漂移（Drift Detected）”**，支持一键（或配置自动）将 SQLite 中该节点名下的所有启用路由无损重推回边缘 Caddy。
  * 删除节点保护：若节点下仍挂载活跃路由，禁止直接删除，提示先迁移出口节点或下线路由。

---

### 3.3 多平台 DNS 服务商与域名管理（FR-03 · P0 / P1）

* **FR-03.1 统一 `DNSProvider` 驱动体系**
  * 支持配置多个 DNS 凭据账号（如“个人 Cloudflare 账号”、“团队 Cloudflare 账号”）。
  * 保存凭据时自动调用厂商 API 验证有效性，并**自动同步拉取该凭据有权限管理的根域名列表（DNS Zones，如 `xiaofantu.com`）**，发布路由时无需手敲完整后缀，直接“输入子域名前缀 + 下拉选根域名”。
* **FR-03.2 Cloudflare 完整支持（P0）**
  * 自动创建、更新、删除 `A`（及可选 `AAAA`）解析记录，指向所选边缘节点的 `Public IP`；
  * 支持在发布时或看板卡片上**一键切换小黄云（`Proxied` 代理状态）**，实时调用 Cloudflare PATCH 接口生效；
  * 冲突检测：若目标子域名在 Cloudflare 已存在同名非 Vantu 托管记录，提示用户选择“接管更新”或“更换前缀”，严禁静默覆盖已有外部解析。
* **FR-03.3 DNSPod / 腾讯云解析支持（P1）**
  * 通过腾讯云 `SecretId` / `SecretKey` 实现国内已备案域名的自动 A 记录增删改查；界面自动隐藏该厂商不支持的“小黄云”开关（置为仅 DNS 解析态）。

---

### 3.4 Tailscale 隧道感知与网络诊断（FR-04 · P0 / P2）

* **FR-04.1 本机 Tailscale 状态零配置自动感知（P0）**
  * 启动及运行期通过挂载的 `/var/run/tailscale/tailscaled.sock`（或环境变量显式覆写）自动读取本机 Tailscale IPv4（如 `100.103.81.50`）、主机名与在线状态；
  * 用户全程无需记忆或手填本机 `100.x.x.x` 地址。
* **FR-04.2 Peer 间直连 vs DERP 中继深度监测（P0）**
  * 定期采集本机到各边缘节点的 Tailscale 连接模式与 RTT 延迟：
    * **UDP 直连（`Direct`）**：绿标展示真实 RTT（如 `香港01 · 49ms Direct`）；
    * **DERP 中继（`DERP Relay`）**：黄标提示当前通过哪个中继节点转发（如 `DERP Tokyo · 186ms`），提醒用户检查 UDP 打洞或防火墙策略。
* **FR-04.3 内嵌 `tsnet` 免宿主机客户端模式（P2）**
  * 针对群晖、特殊受限容器平台等未安装宿主机 `tailscaled` 的环境，支持在设置页填入 `Tailscale AuthKey`，由 Vantu 进程内嵌 `tsnet` 直接接入虚拟内网。

---

### 3.5 服务发现与本地安全桥接（FR-05 · P0）

* **FR-05.1 Docker Socket 实时容器探测**
  * 以只读模式挂载 `/var/run/docker.sock:ro`，实时监听容器生命周期事件（`start` / `die` / `stop` / `rename`）；
  * 自动解析每个运行中容器的：容器名、Compose 项目名、镜像名、网络模式（`host` / `bridge` / 自定义网络）、端口映射详情及是否已被 Vantu 发布（已发布容器标记对应域名，未发布容器置顶便于导入）。
* **FR-05.2 端口可达性智能分级与自动桥接（核心体验）**
  * 系统自动分析容器端口状态并智能推荐最优转发模式：
    1. **宿主机全网卡端口（如 `0.0.0.0:8080->8080/tcp` 或 `network_mode: host`）**：默认选用 **`Direct` 直连模式**（边缘 Caddy 直连 `<本机TS_IP>:8080`）；
    2. **仅回环绑定端口（如 `127.0.0.1:8080->8080/tcp`）或未 `-p` 映射的容器内网端口（如 `172.18.0.5:8080`）**：自动选用 **`Local Relay` 本地安全桥接模式**，边缘 Caddy 转发至 `<本机TS_IP>:18080`（携带路由标识与内部签名密钥），由本机 Vantu 内置反向代理安全桥接至目标容器，**用户无需重启容器改 `-p` 参数，更无需把业务端口暴露到宿主机公网/局域网**！
* **FR-05.3 手工静态目标添加**
  * 支持手动输入任意宿主机端口（如 `127.0.0.1:3000`）或局域网其他设备地址（如群晖 NAS `192.168.1.20:5000`、路由器 `192.168.1.1:80`），由本机 Vantu 自动通过 `Local Relay` 桥接出海。
* **FR-05.4 Docker Labels 声明式提示（可选增强 · P1）**
  * 识别容器上的 `vantu.domain`、`vantu.preset` 等 Label，在控制台中高亮提示“发现待一键确认发布的服务”或开启全自动零点击发布。

---

### 3.6 一键发布编排与上游协议增强（FR-06 · P0）

* **FR-06.1 极简一键发布表单**
  * 必填项仅 3 项：**选择目标服务（容器下拉或手填 IP:Port）** + **输入二级域名前缀 & 选择根域名** + **选择边缘出口节点（默认已预选）**。
  * 高级选项默认折叠并提供 **4 个场景化一键预设胶囊（Presets）**：
    1. **`常规 Web 服务`（默认）**：自动支持 HTTP/1.1、HTTP/2、HTTP/3 及 **WebSocket 无缝升级**，自动注入标准 `X-Forwarded-For` / `X-Forwarded-Proto` / `X-Real-IP`；
    2. **`AI / SSE 流式接口`（专为 `sub2api`、OneAPI、Ollama、Dify 设计）**：配置 Caddy `flush_interval -1` 禁用响应缓冲、延长流式读取超时，确保大模型 Token 逐字丝滑输出不卡顿；
    3. **`WebDAV / 大文件传输`（专为 WebDAV、Alist、Immich、Nextcloud 设计）**：放开请求体大小限制（或按需设上限）、延长上传读写超时、完整透传 WebDAV 扩展 HTTP 方法（`PROPFIND`、`MKCOL`、`MOVE`、`COPY`、`LOCK` 等）；
    4. **`HTTPS 自签名后端`（专为 PVE、群晖 DSM、Portainer、OpenWrt 设计）**：上游切换为 `https://` 并自动开启 Caddy `tls_insecure_skip_verify`，解决内网自签名 HTTPS 管理后台反代 502 报错。
* **FR-06.2 编排原子性、进度可视化与异常补偿**
  * 点击发布后实时反馈 4 步进度：① 写入本地记录 ➡️ ② 注入 DNS 解析 ➡️ ③ 下发边缘 Caddy 路由 ➡️ ④ 触发首轮双轨拨测与证书签发检查。
  * 若步骤 ② 成功但步骤 ③ 失败（如边缘节点瞬断），路由状态标记为 `SYNC_FAILED` 并保留已创建的 `DNS Record ID`：用户可点击 **“一键重试下发”**（幂等补齐步骤 ③）或 **“撤销并清理 DNS”**（自动回滚删除步骤 ② 的解析记录），绝不残留孤儿配置。
* **FR-06.3 路由全生命周期操作**
  * 支持对已发布路由执行：**暂停/恢复**（临时摘除边缘 Caddy 路由但保留 DNS 与配置）、**热切换边缘出口节点**（自动更新 DNS 记录指向新节点 IP + 在新节点注入路由 + 从旧节点移除路由）、**修改后端端口/预设档位**、**彻底删除**（同步清理边缘 Caddy 路由与云端 DNS 记录）。

---

### 3.7 双轨健康巡检与自动化 SSL 证书观测（FR-07 · P0）

* **FR-07.1 证书自动签发与续期解耦**
  * 证书申请与到期前 30 天静默续签全部由边缘纯净 Caddy 的内置 ACME 引擎（Let's Encrypt / ZeroSSL）自动完成，Vantu 无需搬运私钥文件，降低证书泄露面。
* **FR-07.2 五维双轨定期巡检（默认每 60s，支持单路由立即手动拨测）**
  1. **本机源站探针（Local Check）**：TCP/HTTP 探测本机目标容器或局域网端口是否存活；
  2. **隧道探针（Tunnel Check）**：检测本机至所属边缘节点的 Tailscale RTT 与 Direct/DERP 状态；
  3. **边缘规则探针（Caddy Config Check）**：通过边缘 Caddy Admin API 校验 `@id` 路由规则在内存中是否存在且未漂移；
  4. **边缘源站直连探针（Origin TLS & HTTP Check）**：绕过公网 DNS/CDN，直拨 `边缘节点公网IP:443`（携带目标域名 SNI），**精确提取香港 Caddy 源站真实 SSL 证书的颁发机构、生效状态与真实剩余有效天数**（彻底解决开启 Cloudflare 小黄云后只能读到 CF 边缘证书的问题！）；
  5. **公网端到端探针（Public E2E Check）**：通过标准公网 DNS 访问 `https://sub.xiaofantu.com`，验证 DNS 解析生效情况、Cloudflare 边缘状态、最终 HTTP 状态码与端到端响应耗时。
* **FR-07.3 精准故障定界与证书到期预警**
  * 当路由异常时，不只显示笼统的“异常红灯”，而是**直接标出故障发生的具体跳点**（例如：`本地容器已停止 [sub2api:8080 Connection Refused]`、`源站 ACME 证书签发中`、`Cloudflare 522 回源超时`）；
  * 源站证书剩余有效期 `≤ 14 天` 标黄预警，`≤ 3 天` 或签发失败标红告警。

---

### 3.8 全景看板 Dashboard 与事件审计（FR-08 · P0）

* **FR-08.1 顶部全局态势指标栏**
  * 聚合展示：活跃路由总数 / 异常路由数、在线边缘节点数（含平均跨境 RTT 与直连率）、本机已发现未发布容器数、证书预警数。
* **FR-08.2 路由视图（卡片网格视图 + 高密度表格视图一键切换）**
  * 每条路由直观呈现：二级域名（一键新窗口打开 / 复制链接）、后端目标（如 `[Docker] sub2api:8080 · Bridge`）、出口节点与延迟（如 `HK-01 · 49ms`）、**五段式链路微缩健康灯**、源站 SSL 剩余天数、**Cloudflare 小黄云快捷拨杆**、预设标签（`SSE` / `WebDAV`）。
  * 支持按域名/容器名关键词实时过滤、按出口节点或健康状态筛选，也支持随时唤起 **`⌘K / Ctrl+K` 全局命令面板**秒搜容器并发布。
* **FR-08.3 网络波动与操作事件时间线**
  * 记录关键生命周期事件（如路由发布/删除、小黄云切换、容器重启导致上游恢复、Tailscale 切换 DERP、证书自动续签完成），便于回溯排障。

---

### 3.9 AI 友好的 CLI 工具链（FR-09 · P0）

为满足开发者终端效率与 AI Coding Agent（Claude Code / Antigravity / Codex 等）自动化调用需求，`vantu` 二进制原生提供一流的 Agent-DX 命令行能力：

* **FR-09.1 单二进制双角色与免配置本机直连**
  * 在服务端宿主机直接执行 `vantu <command>` 时，优先通过本地受权限保护的 Unix Domain Socket（`/app/data/vantu.sock`）通信，免去繁琐 Token 配置；跨机器远程调用支持 `VANTU_URL` + `VANTU_TOKEN` 环境变量或 `~/.config/vantu/config.json`。
* **FR-09.2 100% 结构化输出与幂等非交互执行**
  * 所有子命令统一支持 `--json`（及管道非 TTY 环境下可通过 `VANTU_OUTPUT=json` 全局启用），返回包含 `ok`、`code`、`data`、`error`、`remediation`（修复建议）的稳定 JSON 结构；
  * 写操作命令全部支持 `--dry-run`（仅计算并展示即将执行的 DNS 记录与 Caddy JSON 变更，不实际落库下发）与幂等更新，绝不在无 `--interactive` 时弹出阻塞式问答卡死 AI Agent。
* **FR-09.3 上下文自省与一键 Agent Skill 导出**
  * `vantu capabilities --json`：单次调用返回当前系统所有可用根域名（Zones）、在线边缘节点、可导入的运行中容器及支持的上游预设档位，让 AI Agent 仅用 **1 次查询 + 1 次执行**即可准确完成发布；
  * `vantu doctor [domain] --json`：针对指定域名或全局基础设施执行五段式逐跳深度诊断，输出结构化故障原因与建议修复命令；
  * `vantu skill export`：一键生成适配主流 Agent 的标准 `SKILL.md` 说明文件。

---

## 4. 核心验收标准（Acceptance Criteria）

1. **端到端一键发布耗时**：在域名 NS 已托管于 Cloudflare、边缘 Caddy 在线的前提下，从选中本地 `sub2api` 容器点击“发布”到公网 HTTPS `200 OK`（或预期业务状态码）全自动跑通，全程零 SSH 登录边缘机、零手动编辑配置文件。
2. **边缘无损热更新**：新增、修改或删除任意路由时，同一边缘节点上正在进行的其他长连接（如 SSE 流式输出或大文件下载）**不断流、不抖动**。
3. **小黄云下真实源站证书可见**：开启 Cloudflare 小黄云（公网解析返回 Cloudflare Anycast IP）后，看板与 `vantu route inspect --json` 依然能准确显示香港边缘 Caddy 上 Let's Encrypt 证书的真实到期时间，误差为 0。
4. **未暴露容器零改造穿透**：启动一个仅 `EXPOSE 80` 但未加 `-p` 参数（或仅加 `-p 127.0.0.1:18088:80`）的 Nginx 测试容器，在 Vantu 选择发布后，公网域名可正常访问该容器，且宿主机公网/局域网网卡未额外暴露 `18088` 端口。
5. **AI Agent 盲调通过率**：将 `vantu skill export` 生成的说明交给 AI Agent，下达自然语言指令“把本地刚跑起来的 webdav 容器发布到 dav.xiaofantu.com 并使用大文件预设”，Agent 可自主通过 `--json` 完成探测、预览、发布与拨测确认。

---

## 5. 非功能需求（Non-Functional Requirements）

| 维度 | 指标要求 |
| :--- | :--- |
| **资源占用** | Vantu 控制端容器空闲内存占用 `≤ 40MB`（Go + SQLite 静态编译），镜像压缩体积 `≤ 35MB`；边缘端纯官方 Caddy 内存占用 `~20MB`。 |
| **故障隔离（高可用）** | 控制面与直连数据面解耦：若北京本地 Vantu 容器停止或升级重启，所有处于 **`Direct` 直连模式**的已发布业务路由不受任何影响，公网依然正常经香港 Caddy 访问本地容器。 |
| **安全性** | 默认只读挂载 `docker.sock` 与 `tailscaled.sock`；边缘 Caddy Admin API 严禁裸露公网 `0.0.0.0:2019`；SQLite 密钥字段全部 `AES-256-GCM` 加密；前后端输入严格校验域名与端口格式，杜绝 SSRF 与配置注入。 |
| **跨平台与部署** | 原生支持 `linux/amd64` 与 `linux/arm64`（树莓派 / ARM 软路由 / Mac Mini M 系列 Homelab 均可运行），提供单文件 `docker-compose.yml` 3 分钟开箱即用。 |

---

## 6. 版本演进路线图（Release Roadmap）

* **MVP（v0.1.0 — 核心闭环版）**
  * 单管理员初始化与凭据加密、多边缘 Caddy 节点管理与漂移对账、Cloudflare DNS 驱动（含小黄云切换）、宿主机 Tailscale 自动感知（含 Direct/DERP 识别）、Docker 容器自动发现 + 手工添加、`Direct` + `Local Relay` 双模转发、四大上游协议预设（Web / SSE / WebDAV / 自签名 HTTPS）、双轨健康与源站证书巡检、Vue 3 全景拓扑看板、`vantu` AI 友好 CLI 全套核心命令。
* **v1.1（国内生态与声明式自动化增强）**
  * 新增 **DNSPod / 腾讯云解析** 与 **阿里云 DNS** 驱动；
  * 支持 Docker Labels（`vantu.*`）容器启动即自动注册上线、容器销毁自动归档路由；
  * 新增 Webhook 告警通知（Telegram Bot / Bark / 飞书 / 企业微信 / 自定义 Webhook：节点离线、源站宕机、证书到期预警自动推送）。
* **v1.2（免宿主机隧道与四层/访问控制扩展）**
  * 引入 Go 原生 **`tsnet`** 嵌入式入网模式（无需宿主机安装 `tailscaled`）；
  * 边缘层可选附加轻量访问保护（Caddy Basic Auth 密码锁 / IP 白名单一键开关，保护无自带登录的内网小工具）；
  * 探索基于 Caddy Layer4 或隧道转发的 TCP 端口映射扩展。
