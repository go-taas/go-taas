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

# ---- Console (web/) build stage ---------------------------------------
# Node 20 matches the documented minimum (README: "Node.js 20+").
FROM node:20-alpine AS console-builder
WORKDIR /web

# Cache npm downloads: copy manifests first.
COPY web/package.json web/package-lock.json ./
RUN npm ci --no-audit --no-fund

COPY web/ .
RUN npm run build

# ---- Protobuf codegen stage --------------------------------------------
# Generated Go code is not committed (only *.proto is tracked), so it is
# regenerated here before the Go build.
FROM bufbuild/buf:1.70.0 AS buf
WORKDIR /src
COPY buf.yaml buf.gen.yaml ./
COPY proto/ ./proto/
# buf dep update fetches the googleapis dependency declared in buf.yaml.
RUN buf dep update && buf generate

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
FROM gcr.io/distroless/static-debian12:nonroot AS taas-server
ARG VERSION
ARG COMMIT
ARG BUILD_TIME
LABEL org.opencontainers.image.title="taas-server" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.revision="${COMMIT}" \
      org.opencontainers.image.created="${BUILD_TIME}"
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
