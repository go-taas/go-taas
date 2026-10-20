# Unified multi-stage image for the platform binaries.
#
# Two build targets are exposed so CI can publish one image per binary:
#   docker build --target taas-server -t <registry>/taas-server:tag .
#   docker build --target controller    -t <registry>/controller:tag .
#
# Build metadata is injected via --build-arg by CI and persisted as
# OCI image labels (visible via `docker inspect --format '{{json .Config.Labels}}'`).

# ---- Build metadata ---------------------------------------------------
ARG VERSION=0.0.0
ARG COMMIT=unknown
ARG BUILD_TIME=unknown
# Module proxy; override for restricted networks, e.g.
#   --build-arg GOPROXY=https://goproxy.cn,direct
ARG GOPROXY=https://proxy.golang.org,direct
# npm registry mirror for the console build; override for restricted
# networks, e.g. --build-arg NPM_REGISTRY=https://registry.npmmirror.com
ARG NPM_REGISTRY=https://registry.npmjs.org
# Debian apt mirror for the taas-server runtime stage; override for
# restricted networks, e.g. --build-arg APT_MIRROR=http://mirrors.tuna.tsinghua.edu.cn
ARG APT_MIRROR=

# ---- Console (web/) build stage ---------------------------------------
# Node 20 matches the documented minimum (README: "Node.js 20+").
FROM node:20-alpine AS console-builder
WORKDIR /web

# Cache npm downloads: copy manifests first. The registry is only
# overridden when NPM_REGISTRY is set to a mirror.
COPY web/package.json web/package-lock.json ./
RUN if [ "$NPM_REGISTRY" != "https://registry.npmjs.org" ]; then \
      npm config set registry "$NPM_REGISTRY"; \
    fi && \
    npm ci --no-audit --no-fund

COPY web/ .
RUN npm run build

# ---- Protobuf codegen stage --------------------------------------------
# Generated Go code is not committed (only *.proto is tracked), so it is
# regenerated here before the Go build.
#
# The buf stage uses the golang image so it can install the protoc plugins
# locally (buf.gen.yaml references them via `local:`). This keeps the build
# offline: buf generate resolves the googleapis dependency from the
# checked-in buf.lock and runs the plugins from PATH, so neither the buf
# remote (BSR) nor a remote plugin download is needed. `buf dep update` is
# intentionally NOT run here: it always contacts the BSR and fails on
# restricted networks.
FROM golang:1.26-alpine AS buf
WORKDIR /src
ARG GOPROXY
ENV GOPROXY=${GOPROXY}
# Install buf and the protoc plugins used by buf.gen.yaml. Versions match
# the local plugins the host uses (protoc-gen-go v1.36.6, go-grpc 1.5.1,
# grpc-gateway v2.27.1).
RUN go install github.com/bufbuild/buf/cmd/buf@v1.70.0 \
    && go install google.golang.org/protobuf/cmd/protoc-gen-go@v1.36.6 \
    && go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@v1.5.1 \
    && go install github.com/grpc-ecosystem/grpc-gateway/v2/protoc-gen-grpc-gateway@v2.27.1 \
    && go install github.com/grpc-ecosystem/grpc-gateway/v2/protoc-gen-openapiv2@v2.27.1
ENV PATH="/root/go/bin:${PATH}"
COPY buf.yaml buf.gen.yaml buf.lock ./
COPY proto/ ./proto/
RUN buf generate

# ---- Go build stage ---------------------------------------------------
FROM golang:1.26-alpine AS builder
WORKDIR /src
ARG GOPROXY
ENV GOPROXY=${GOPROXY}

# Cache module downloads: copy manifests first, everything else after.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

# Overlay the freshly generated protobuf code (the checkout does not
# contain it) and the console bundle built above.
COPY --from=buf /src/proto/ /src/proto/
COPY --from=console-builder /web/dist/ /src/pkg/server/console/

ARG TARGETARCH
ARG VERSION
ARG COMMIT
ARG BUILD_TIME
RUN CGO_ENABLED=0 GOOS=linux GOARCH=${TARGETARCH} \
    go build -trimpath \
    -ldflags "-s -w \
      -X main.version=${VERSION} \
      -X main.commit=${COMMIT} \
      -X main.buildTime=${BUILD_TIME}" \
    -o /out/taas-server ./apps/taas-server && \
    CGO_ENABLED=0 GOOS=linux GOARCH=${TARGETARCH} \
    go build -trimpath \
    -ldflags "-s -w \
      -X main.version=${VERSION} \
      -X main.commit=${COMMIT} \
      -X main.buildTime=${BUILD_TIME}" \
    -o /out/controller ./apps/controller

# ---- taas-server ------------------------------------------------------
# The runtime image carries skopeo (used by the image-import flow to copy
# engine images into the internal Harbor project) and runs as a non-root
# user. Debian-slim is used because distroless has no package manager to
# install skopeo.
FROM debian:bookworm-slim AS taas-server
ARG VERSION
ARG COMMIT
ARG BUILD_TIME
ARG APT_MIRROR
LABEL org.opencontainers.image.title="taas-server" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.revision="${COMMIT}" \
      org.opencontainers.image.created="${BUILD_TIME}"
# Point apt at a reachable mirror when requested (restricted networks).
RUN if [ -n "$APT_MIRROR" ]; then \
      sed -i "s|http://deb.debian.org/debian|${APT_MIRROR}/debian|; s|http://deb.debian.org/debian-security|${APT_MIRROR}/debian-security|" /etc/apt/sources.list.d/debian.sources; \
    fi && \
    apt-get update \
    && apt-get install -y --no-install-recommends skopeo ca-certificates \
    && rm -rf /var/lib/apt/lists/* \
    && useradd --uid 65532 --create-home nonroot
COPY --from=builder /out/taas-server /usr/local/bin/taas-server
COPY configs /etc/taas/configs
WORKDIR /etc/taas
USER nonroot:nonroot
ENTRYPOINT ["/usr/local/bin/taas-server"]

# ---- controller -------------------------------------------------------
FROM gcr.io/distroless/static-debian12:nonroot AS controller
ARG VERSION
ARG COMMIT
ARG BUILD_TIME
LABEL org.opencontainers.image.title="controller" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.revision="${COMMIT}" \
      org.opencontainers.image.created="${BUILD_TIME}"
COPY --from=builder /out/controller /usr/local/bin/controller
COPY configs /etc/taas/configs
WORKDIR /etc/taas
USER nonroot:nonroot
ENTRYPOINT ["/usr/local/bin/controller"]

# ---- console ----------------------------------------------------------
# Standalone admin-console image: the same Vite bundle that is embedded
# into taas-server, served by nginx with /api/ reverse-proxied to the
# gateway. Use it when the console should scale or be released
# independently of the control plane.
#   docker build --target console -t <registry>/console:tag .
# The gateway address is set at runtime via TASS_PROXY_PASS (default
# http://taas-server:9091); see build/docker/console.nginx.conf.
FROM nginx:1.29-alpine AS console
ARG VERSION
ARG COMMIT
ARG BUILD_TIME
LABEL org.opencontainers.image.title="console" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.revision="${COMMIT}" \
      org.opencontainers.image.created="${BUILD_TIME}"
COPY build/docker/console.nginx.conf /etc/nginx/templates/default.conf.template
COPY --from=console-builder /web/dist/ /usr/share/nginx/html/
# 8080 is unprivileged; nginx:alpine supports running as root only for
# the master process, which drops to the nginx user for workers.
EXPOSE 8080
ENV TASS_PROXY_PASS=http://taas-server:9091
HEALTHCHECK --interval=30s --timeout=3s --retries=3 \
  CMD wget -q -O /dev/null http://127.0.0.1:8080/ || exit 1
CMD ["nginx", "-g", "daemon off;"]
