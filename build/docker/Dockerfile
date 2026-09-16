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

FROM golang:1.26-alpine AS builder
WORKDIR /src
ARG GOPROXY
ENV GOPROXY=${GOPROXY}

# Cache module downloads: copy manifests first, everything else after.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

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
