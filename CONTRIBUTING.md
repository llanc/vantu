# 贡献指南 (Contributing)

感谢关注与参与 Vantu（小凡兔）的开发。

## 开发环境要求

- **Go**：`1.27+`（纯 Go 编译，`CGO_ENABLED=0`，无 C 编译器依赖）
- **Node.js**：`20+`（推荐 LTS 24）
- **pnpm**：`12.16+`（见 `web/package.json` 中的 `packageManager` 字段）

## 本地构建与测试

```bash
# 1. 构建前端产物并编译单文件二进制 (./vantu 及软链 ./xft)
make build

# 2. 代码格式与静态检查
make lint

# 3. 运行单元与全链路集成测试
make test
```

前端独立开发调试：

```bash
# 终端 1：启动后端守护进程
./vantu serve --listen 127.0.0.1:8089 --data-dir ./data

# 终端 2：启动前端 Vite 开发服务器
pnpm --dir web dev
```

## 架构与代码约定

1. **边缘零自定义 Agent**：边缘节点仅运行官方 `caddy:2.8-alpine`，所有路由变更通过 Caddy Admin API `@id` 原子接口完成，不在边缘节点引入额外守护进程或数据库。
2. **单库单连接池**：状态存储使用 `modernc.org/sqlite`（WAL 模式），保持 schema 简洁向后兼容。
3. **CLI 与 Web 对等**：新增控制面能力时，优先在 `internal/orchestrator` 与 `internal/api` 实现，并同步支持 CLI（含 `--json` 结构化输出）与 Web 控制台。
4. **错误码与退出码**：CLI 退出码遵循 `0~7` 规范（`0` 成功、`2` 参数校验错误、`3` 鉴权失败、`4` DNS 冲突、`5` 边缘节点不可达、`6` 本地目标不可达、`7` 巡检异常），错误信息需包含可读原因与修复建议（`suggestion`）。

## 提交流程

1. Fork 本仓库并从 `main` 创建特性分支（如 `feat/xxx` 或 `fix/xxx`）。
2. 确保提交前通过 `make lint` 与 `make test`。
3. 提交信息推荐使用 Conventional Commits 格式（如 `feat: ...`、`fix: ...`、`docs: ...`）。
4. 发起 Pull Request 并简要说明变更背景与验证方式。
