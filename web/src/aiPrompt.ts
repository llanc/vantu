import type { DashboardData, DiscoveredContainer } from './api'
import type { Locale } from './i18n'

export function buildAIPrompt(
  lang: Locale,
  origin: string,
  dash: DashboardData | null,
  unexposed: DiscoveredContainer[]
): string {
  const zh = lang === 'zh'
  const zones =
    dash?.zones
      .map((z) => `${z.zone_name}${z.is_default ? (zh ? '(默认)' : ' (default)') : ''}${z.supports_proxy ? (zh ? '[支持CDN]' : ' [CDN]') : ''}`)
      .join(', ') || (zh ? '暂未配置' : 'not configured')
  const nodes =
    dash?.nodes
      .map((n) => `${n.name}(${n.public_ipv4}${n.is_default ? (zh ? ',默认' : ', default') : ''})`)
      .join(', ') || (zh ? '暂未配置' : 'not configured')
  const routes =
    dash?.routes.map((r) => `${r.full_domain} -> ${r.local_target_host}:${r.local_target_port}`).join(', ') ||
    (zh ? '暂无' : 'none')
  const unpub = unexposed.map((c) => `${c.name}(:${c.recommended_port})`).join(', ') || (zh ? '无' : 'none')

  if (zh) {
    return `请帮我操作当前「小凡兔 Vantu」网关控制台（将本地 Docker 容器或内网端口发布为公网 HTTPS 服务）。

【当前控制台实时快照】
- 控制台地址: ${origin}
- 可用根域名: ${zones}
- 边缘节点: ${nodes}
- 已发布路由: ${routes}
- 待发布容器: ${unpub}

【第一步：自动检测连接通道（严禁向用户索要明文 API Token）】
请按以下顺序自动探测执行环境：

1. 场景 A（同机 / SSH 环境，免 Token 优先）：
   先检查本机是否存在 \`xft\` 命令或 \`vantu\` 容器：
   \`xft capabilities --json 2>/dev/null || docker exec vantu xft capabilities --json 2>/dev/null\`
   若返回 \`"ok": true\`，后续所有操作直接调用本机 \`xft\`（通过本地 Unix Socket 通信，无需任何 Token）。

2. 场景 B（跨机器远程环境，Token 走本地凭据或环境变量，不进对话上下文）：
   若不在小凡兔宿主机上，请检查当前环境是否已配置远程凭据：
   - 若本机装有 \`xft\` CLI：检查 \`~/.config/vantu/credentials.json\` 或环境变量 \`$VANTU_TOKEN\`；若未配置，请引导我自己在终端执行一次：
     \`xft login --endpoint ${origin} --token <从控制台设置页创建的API_Token>\`
     （凭据保存在我本机 \`0600\` 权限文件中，你直接调用 \`xft ... --json\` 即可，全程不要让我把 Token 发到聊天框）。
   - 若本机未装 \`xft\` CLI（纯 HTTP API 调用）：先检查 \`[ -n "$VANTU_TOKEN" ]\`。若为空，请引导我自己在本地终端或环境配置中设置：
     \`export VANTU_ENDPOINT="${origin}" && export VANTU_TOKEN="<从控制台设置页创建的API_Token>"\`
     设置完成后你直接引用 \`$VANTU_ENDPOINT\` 与 \`$VANTU_TOKEN\` 环境变量发起请求，切勿让我将明文 Token 粘贴到对话中。

【第二步：常用操作速查】
- CLI 方式（同机或已执行 \`xft login\`）：
  - 查看能力与容器：\`xft capabilities --json\`
  - 预览/发布服务：\`xft publish --target <container:容器名|IP:端口|self> --domain <完整域名> --preset <web|sse|webdav|https_insecure> [--proxied] [--dry-run] --json\`
  - 切换 CDN 代理：\`xft route proxy <完整域名> --on|--off --json\`
  - 拨测/暂停/恢复/删除：\`xft route inspect <完整域名> --probe --json\` | \`xft route pause|resume <完整域名> --json\` | \`xft route rm <完整域名> --yes --json\`
- HTTP API 方式（引用本地 \`$VANTU_TOKEN\` 环境变量）：
  - 查看能力：\`curl -sS -H "Authorization: Bearer $VANTU_TOKEN" "${origin}/api/v1/capabilities"\`
  - 发布路由：\`curl -sS -X POST -H "Authorization: Bearer $VANTU_TOKEN" -H "Content-Type: application/json" "${origin}/api/v1/routes" -d '{"target":"container:容器名","domain":"子域名.根域名","preset":"web","dns_proxied":false}'\`
  - 切换 CDN：\`curl -sS -X PATCH -H "Authorization: Bearer $VANTU_TOKEN" -H "Content-Type: application/json" "${origin}/api/v1/routes/<完整域名>/proxy" -d '{"proxied":true}'\`
  - 删除路由：\`curl -sS -X DELETE -H "Authorization: Bearer $VANTU_TOKEN" "${origin}/api/v1/routes/<完整域名>?delete_dns=true"\`

请先自动检测你当前可用的连接方式，然后协助我完成以下需求：`
  }

  return `Please help me operate my "Vantu" gateway console (it publishes local Docker containers or LAN ports as public HTTPS services).

[Live console snapshot]
- Console URL: ${origin}
- Available zones: ${zones}
- Edge nodes: ${nodes}
- Published routes: ${routes}
- Unpublished containers: ${unpub}

[Step 1: Detect the connection channel automatically (never ask me for a plaintext API token)]
Probe the environment in this order:

1. Scenario A (same host / SSH session, token-free first):
   Check whether the \`xft\` command or a \`vantu\` container exists on this machine:
   \`xft capabilities --json 2>/dev/null || docker exec vantu xft capabilities --json 2>/dev/null\`
   If it returns \`"ok": true\`, run every later operation through the local \`xft\` (it talks over the local Unix socket, no token needed).

2. Scenario B (remote machine; the token lives in local credentials or environment variables and never enters this conversation):
   If you are not on the Vantu host, check whether remote credentials are already configured:
   - If the \`xft\` CLI is installed: check \`~/.config/vantu/credentials.json\` or \`$VANTU_TOKEN\`. If neither is set, guide me to run this once in my own terminal:
     \`xft login --endpoint ${origin} --token <API token created in console Settings>\`
     (The credentials are stored in a \`0600\` file on my machine; just call \`xft ... --json\`. Never ask me to paste the token into the chat.)
   - If \`xft\` is not installed (plain HTTP API): first check \`[ -n "$VANTU_TOKEN" ]\`. If it is empty, guide me to set it myself in my terminal or environment:
     \`export VANTU_ENDPOINT="${origin}" && export VANTU_TOKEN="<API token created in console Settings>"\`
     Then reference \`$VANTU_ENDPOINT\` and \`$VANTU_TOKEN\` in your requests. Never ask me to paste the plaintext token into the conversation.

[Step 2: Quick reference]
- CLI (same host, or after \`xft login\`; add \`--lang en\` or \`export VANTU_LANG=en\` for English output):
  - Capabilities & containers: \`xft capabilities --json\`
  - Preview / publish: \`xft publish --target <container:NAME|IP:PORT|self> --domain <FQDN> --preset <web|sse|webdav|https_insecure> [--proxied] [--dry-run] --json\`
  - Toggle CDN proxy: \`xft route proxy <FQDN> --on|--off --json\`
  - Probe / pause / resume / delete: \`xft route inspect <FQDN> --probe --json\` | \`xft route pause|resume <FQDN> --json\` | \`xft route rm <FQDN> --yes --json\`
- HTTP API (using the local \`$VANTU_TOKEN\` variable):
  - Capabilities: \`curl -sS -H "Authorization: Bearer $VANTU_TOKEN" "${origin}/api/v1/capabilities"\`
  - Publish a route: \`curl -sS -X POST -H "Authorization: Bearer $VANTU_TOKEN" -H "Content-Type: application/json" "${origin}/api/v1/routes" -d '{"target":"container:NAME","domain":"sub.example.com","preset":"web","dns_proxied":false}'\`
  - Toggle CDN: \`curl -sS -X PATCH -H "Authorization: Bearer $VANTU_TOKEN" -H "Content-Type: application/json" "${origin}/api/v1/routes/<FQDN>/proxy" -d '{"proxied":true}'\`
  - Delete a route: \`curl -sS -X DELETE -H "Authorization: Bearer $VANTU_TOKEN" "${origin}/api/v1/routes/<FQDN>?delete_dns=true"\`

Detect which connection method is available to you first, then help me with the following request:`
}
