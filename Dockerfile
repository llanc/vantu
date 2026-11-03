# Stage 1: Build Vue 3 + TypeScript Console Bundle
FROM node:22-alpine AS web-builder
RUN corepack enable && corepack prepare pnpm@latest --activate
WORKDIR /src/web
COPY web/package.json web/pnpm-lock.yaml ./
RUN pnpm install --frozen-lockfile --allow-build=esbuild
COPY web/ ./
RUN pnpm build

# Stage 2: Build Pure-Go Static Binary (CGO_ENABLED=0)
FROM golang:1.24-alpine AS go-builder
ARG VERSION=v0.1.0
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . ./
COPY --from=web-builder /src/web/dist ./web/dist
RUN CGO_ENABLED=0 GOOS=linux go build \
    -trimpath \
    -ldflags="-s -w -X github.com/xiaofantu/vantu/internal/config.Version=${VERSION}" \
    -o /out/vantu ./cmd/vantu

# Stage 3: Minimal Production Runtime (~20MB total)
FROM alpine:3.21
RUN apk add --no-cache ca-certificates tzdata \
    && mkdir -p /data
COPY --from=go-builder /out/vantu /usr/local/bin/vantu
RUN ln -s /usr/local/bin/vantu /usr/local/bin/xft

ENV VANTU_DATA_DIR=/data \
    VANTU_LISTEN_ADDR=0.0.0.0:8089 \
    VANTU_RELAY_PORT=18080

EXPOSE 8089 18080
VOLUME ["/data"]

HEALTHCHECK --interval=30s --timeout=4s --start-period=5s --retries=3 \
    CMD wget -qO- http://127.0.0.1:8089/api/v1/healthz || exit 1

ENTRYPOINT ["/usr/local/bin/vantu"]
CMD ["serve"]
