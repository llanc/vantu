# Vantu (小凡兔)

[![CI](https://github.com/llanc/vantu/actions/workflows/ci.yml/badge.svg)](https://github.com/llanc/vantu/actions/workflows/ci.yml)
[![Go](https://img.shields.io/badge/Go-1.27+-00ADD8?logo=go&logoColor=white)](./go.mod)
[![Vue](https://img.shields.io/badge/Vue-3.5-4FC08D?logo=vuedotjs&logoColor=white)](./web/package.json)
[![Docker](https://img.shields.io/badge/Docker-ghcr.io%2Fllanc%2Fvantu-2496ED?logo=docker&logoColor=white)](./Dockerfile)
[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](./LICENSE)

[简体中文](./README.md) | **English**

Lightweight edge reverse-proxy and tunnel orchestrator. Publish unexposed local Docker containers or internal ports as public HTTPS services with a built-in Web console and AI-friendly CLI (`vantu` / `xft`).

---

## Key Features

1. **End-to-End Route Orchestration**: A single action synchronizes **DNS records (Cloudflare / Tencent Cloud DNSPod)** → **Tailscale direct mesh or local secure relay** → **Edge VPS Caddy `@id` atomic route provisioning & ACME TLS issuance**.
2. **Agentless Edge Nodes**: Public edge VPS instances only need the official `caddy:2.8-alpine` image. Vantu generates bootstrap configurations with Tailscale subnet allowlists and Bearer secret guards out of the box.
3. **Built-in Local Relay (`:18080`)**: Reach local Docker containers that do not publish host ports (`-p`) or only bind to `127.0.0.1` via single-port HTTP multiplexing signed with per-route `X-Vantu-Relay-Key` HMAC—without restarting containers or exposing host interfaces.
4. **Dynamic 3 / 4 / 5-Hop Topology & Origin Certificate Probing**: Renders the exact forwarding path (**3, 4, or 5 hops** depending on whether `Local Relay` and `Cloudflare CDN` are enabled). When CDN proxy is enabled, Vantu also dials `<EdgePublicIP>:443` with SNI directly to verify the real origin certificate expiration.
5. **CLI & AI Agent Friendly**: Ships with `vantu` (and short alias `xft`), supporting `--json` structured envelopes, `--dry-run` change previews, deterministic exit codes (`0~7`), and one-command `SKILL.md` export.
6. **Single Static Binary**: Compiled with `CGO_ENABLED=0`, embedding the Vue 3 + TypeScript console and a pure-Go SQLite engine in WAL mode.

---

## Quick Start

### Option 1: Docker Compose (Recommended)

```bash
docker compose up -d
```

The console listens on `http://<host-ip>:8089` by default. Open it in a browser to initialize the administrator password. Both `vantu` and `xft` are available inside the container:

```bash
docker exec -it vantu xft capabilities --json
docker exec -it vantu xft doctor
```

### Option 2: Build Single Binary from Source

Requirements: Go `1.27+`, Node.js `20+`, pnpm `12+`.

```bash
make build
./vantu serve --listen 0.0.0.0:8089 --data-dir ./data
```

### Self-Proxying the Console After Fresh Deployment

Useful for headless servers or remote hosts where `:8089` is not directly reachable. Run the following commands on the host running Vantu—the CLI authenticates passwordlessly via the local Unix Domain Socket (prefix commands with `docker exec -it vantu` when running via Docker):

```bash
# 1. Initialize admin password (--generate prints a random password, or pass --password)
xft admin passwd --generate

# 2. Add DNS provider credentials
xft dns add-cf --api-token <CLOUDFLARE_API_TOKEN>
# Or: xft dns add-dnspod --secret-id <ID> --secret-key <KEY>

# 3. Generate edge Caddy bootstrap script and run it on your edge VPS
xft node bootstrap --name edge --public-ip <EDGE_PUBLIC_IP> --ts-ip <EDGE_TAILSCALE_IP>

# 4. Register the edge node (use the Auth Secret printed in step 3)
xft node add --name edge --public-ip <EDGE_PUBLIC_IP> --ts-ip <EDGE_TAILSCALE_IP> --secret <AUTH_SECRET> --default

# 5. Publish the Vantu console itself to public HTTPS
xft publish --target self --domain vantu.example.com --proxied --wait
```

Notes:
- `--target self` automatically resolves the current instance's listening port and enables the `sse` preset; if `--domain` is omitted, it defaults to `vantu.<default-zone>`.
- Before the admin password is initialized, `--target self` is rejected to prevent exposing the setup page to the public internet.
- If you forget the password, reset it locally via `xft admin passwd --password <new-password>`.
- You can also select **Local Console** (`本机控制台`) in the Web UI's **Publish Service** modal.

---

## Common CLI (`vantu` / `xft`) Examples

```bash
# 1. Inspect available DNS zones, edge nodes, and discovered local containers
xft capabilities --json

# 2. Preview publish plan and generated Caddy route JSON (side-effect free)
xft publish --target container:sub2api --domain api.example.com --preset sse --dry-run

# 3. Publish a local service with Cloudflare CDN proxy enabled
xft publish --target 127.0.0.1:8080 --domain demo.example.com --proxied --wait

# 4. Inspect a route and trigger an immediate live health probe
xft route inspect demo.example.com --probe

# 5. Export SKILL.md for Claude Code / Cursor / Codex / Antigravity
xft skill export -o ~/.claude/skills/vantu-publish/SKILL.md

# 6. Check for a new version; binary installs can download, verify and restart in one step
xft update
xft update --apply
```

### Updates

The console checks GitHub Releases every 12 hours and shows a badge in the top bar when a new version is available. Release notes are under **Settings → 版本与更新** (Version & Updates).

- **Binary install**: click **更新至 vX.Y.Z** or run `xft update --apply`. Vantu downloads the package for the current platform, verifies its SHA-256 against `checksums.txt`, atomically replaces the executable and re-execs in place (same PID, transparent to systemd). The service user needs write access to the binary's directory.
- **Docker install**: the container never replaces itself; the console shows the upgrade command `docker compose pull vantu && docker compose up -d vantu`.
- Set `VANTU_UPDATE_CHECK=off` to disable version checks entirely (e.g. air-gapped hosts).

### Local Socket & Remote Endpoint Resolution Order

When `--endpoint` or `VANTU_ENDPOINT` is not explicitly set, the CLI searches for a local Unix Socket in the following order:

1. `--socket <path>` flag
2. `VANTU_SOCK_PATH` environment variable
3. `$VANTU_DATA_DIR/vantu.sock`
4. `./data/vantu.sock`
5. `/data/vantu.sock`

If no local socket is found, it falls back to `~/.config/vantu/credentials.json` (created by `vantu login`) or `http://127.0.0.1:8089`.

---

## Environment Variables

| Variable | Default | Description |
| :--- | :--- | :--- |
| `VANTU_LISTEN_ADDR` | `0.0.0.0:8089` | Web console & HTTP API listen address |
| `VANTU_DATA_DIR` | `./data` (`/data` in Docker) | Data directory (stores SQLite DB, master key, and Unix socket) |
| `VANTU_DB_PATH` | `<VANTU_DATA_DIR>/vantu.db` | SQLite database file path |
| `VANTU_SOCK_PATH` | `<VANTU_DATA_DIR>/vantu.sock` | Local administration Unix Domain Socket path |
| `VANTU_RELAY_PORT` | `18080` | Local Relay multiplexed listener port |
| `VANTU_RELAY_BIND_IP` | Local Tailscale IP | Interface IP for Local Relay to bind |
| `VANTU_DOCKER_SOCK` | `/var/run/docker.sock` | Docker Engine socket path |
| `VANTU_TAILSCALE_SOCK` | `/var/run/tailscale/tailscaled.sock` | Tailscale LocalAPI socket path |
| `VANTU_PROBE_INTERVAL_SEC` | `60` | Background route & certificate probe interval in seconds (min `10`) |
| `VANTU_UPDATE_CHECK` | `on` | Set to `off` to disable GitHub Releases version checks |
| `VANTU_MASTER_KEY` | `<VANTU_DATA_DIR>/.master.key` | Master encryption key for credentials (optional env override) |
| `VANTU_ADMIN_PASSWORD` | Empty | Bootstrap administrator password on first start (optional) |

---

## Development & Security

- [CONTRIBUTING.md](./CONTRIBUTING.md): Development setup, testing & contribution guide
- [SECURITY.md](./SECURITY.md): Security model & vulnerability reporting

---

## License

Released under the [MIT License](./LICENSE).
