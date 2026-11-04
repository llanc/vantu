# 安全策略 (Security Policy)

## 安全机制概览

- **凭据落盘加密**：DNS API Token、SecretKey 与边缘节点 Auth Secret 均经 `AES-256-GCM`（基于主密钥 `.master.key` 或环境变量 `VANTU_MASTER_KEY`）加密后存入 SQLite。
- **口令与会话**：管理员密码采用 `Argon2id` 哈希存储；API Token 与 Web Session 仅在数据库中保存 `SHA-256` 摘要。
- **本机特权隔离**：本地 Unix Domain Socket（默认 `0600` 权限）仅允许同机宿主用户免密执行管理命令；远程 HTTP 接口强制校验 Session Cookie 或 Bearer Token。
- **自代理防护**：在管理员密码未完成初始化前，系统拒绝执行 `--target self` 或将自身监听端口发布至公网，防止初始化接口暴露。
- **Local Relay 鉴权**：本地多路复用桥接端口（默认 `:18080`）仅接受携带有效 `X-Vantu-Relay-Key`（按路由派生的 HMAC-SHA256 签名）的边缘 Caddy 请求，未授权探测直接返回 `403 Forbidden`。

## 报告安全漏洞

如发现潜在安全漏洞（如鉴权绕过、凭据泄露、命令注入、SSRF 等），**请勿直接公开创建 GitHub Issue**。

请通过以下方式私密报告：

1. 使用本仓库的 [GitHub Private Vulnerability Reporting](https://github.com/llanc/vantu/security/advisories/new) 提交安全通告。
2. 在报告中附上复现步骤、受影响版本或提交哈希，以及可能的影响范围。

维护者将在收到报告后尽快确认并协调修复。
