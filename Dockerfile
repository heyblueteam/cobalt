# syntax=docker/dockerfile:1

# Cross-compile from BUILDPLATFORM (native arch) to TARGETPLATFORM
# (the requested image arch). buildx + the docker driver supplies these
# automatically; for plain `docker build` they default to the host arch
# so single-arch builds keep working.
FROM --platform=$BUILDPLATFORM golang:1.25-alpine AS build
ARG TARGETOS
ARG TARGETARCH
ARG VERSION=dev
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build \
    -ldflags="-s -w -X main.version=${VERSION}" \
    -o /out/cobalt ./cmd/cobalt

FROM debian:stable-slim
ARG TARGETARCH
ARG BUILDX_VERSION=v0.19.0
ARG COMPOSE_VERSION=v2.32.4
RUN apt-get update \
    && apt-get install -y --no-install-recommends \
        ca-certificates \
        curl \
        git \
        docker-cli \
    && rm -rf /var/lib/apt/lists/* \
    && mkdir -p /usr/libexec/docker/cli-plugins \
    && curl -fsSL "https://github.com/docker/buildx/releases/download/${BUILDX_VERSION}/buildx-${BUILDX_VERSION}.linux-${TARGETARCH}" \
        -o /usr/libexec/docker/cli-plugins/docker-buildx \
    && chmod 0755 /usr/libexec/docker/cli-plugins/docker-buildx \
    && /usr/libexec/docker/cli-plugins/docker-buildx version \
    && case "${TARGETARCH}" in \
        amd64) COMPOSE_ARCH=x86_64 ;; \
        arm64) COMPOSE_ARCH=aarch64 ;; \
        *) echo "unsupported TARGETARCH=${TARGETARCH}" && exit 1 ;; \
    esac \
    && curl -fsSL "https://github.com/docker/compose/releases/download/${COMPOSE_VERSION}/docker-compose-linux-${COMPOSE_ARCH}" \
        -o /usr/libexec/docker/cli-plugins/docker-compose \
    && chmod 0755 /usr/libexec/docker/cli-plugins/docker-compose \
    && /usr/libexec/docker/cli-plugins/docker-compose version
COPY --from=build /out/cobalt /usr/local/bin/cobalt
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/cobalt", "server"]
