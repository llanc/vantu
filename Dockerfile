# syntax=docker/dockerfile:1

# Stage 1: build the web console
FROM --platform=$BUILDPLATFORM node:24-alpine AS web-builder
WORKDIR /src/web
COPY web/package.json web/pnpm-lock.yaml web/pnpm-workspace.yaml ./
RUN PNPM_VERSION="$(sed -n 's/.*"packageManager": *"pnpm@\([^"]*\)".*/\1/p' package.json)" \
    && npm install -g "pnpm@${PNPM_VERSION}" \
    && pnpm install --frozen-lockfile
COPY web/ ./
RUN pnpm build

# Stage 2: build the static Go binary (cross-compiled for the target platform)
FROM --platform=$BUILDPLATFORM golang:1.27-alpine AS go-builder
ARG VERSION=v0.0.0-dev
ARG TARGETOS
ARG TARGETARCH
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . ./
COPY --from=web-builder /src/web/dist ./web/dist
RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH} go build \
    -trimpath \
    -ldflags="-s -w -X github.com/llanc/vantu/internal/config.Version=${VERSION}" \
    -o /out/vantu ./cmd/vantu

# Stage 3: runtime
FROM alpine:3.22
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
