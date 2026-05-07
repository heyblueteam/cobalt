# syntax=docker/dockerfile:1

FROM --platform=$BUILDPLATFORM golang:1.25-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG TARGETOS
ARG TARGETARCH
ARG VERSION=dev
RUN CGO_ENABLED=0 go build \
    -ldflags="-s -w -X main.version=${VERSION}" \
    -o /out/cobalt ./cmd/cobalt

# rqlited sidecar — download architecture-specific binary
FROM --platform=$TARGETOS/$TARGETARCH debian:stable-slim AS rqlited
ARG RQLITE_VERSION=10.0.3
ARG TARGETARCH
RUN apt-get update \
    && apt-get install -y --no-install-recommends curl \
    && case "${TARGETARCH}" in \
         amd64) ARCH="amd64" ;; \
         arm64) ARCH="arm64" ;; \
         *) ARCH="amd64" ;; \
       esac \
    && curl -sL "https://github.com/rqlite/rqlite/releases/download/v${RQLITE_VERSION}/rqlite-v${RQLITE_VERSION}-linux-${ARCH}.tar.gz" | tar xz \
    && mv rqlited /out/ \
    && rm -rf /var/lib/apt/lists/*

# Runtime image — includes git (for repo fetch) and docker CLI (for shelling
# out to the host docker daemon via the mounted socket). Distroless doesn't
# include either, so we use debian-slim.
FROM debian:stable-slim
RUN apt-get update \
    && apt-get install -y --no-install-recommends \
        ca-certificates \
        git \
        docker-cli \
    && rm -rf /var/lib/apt/lists/*
COPY --from=build /out/cobalt /usr/local/bin/cobalt
COPY --from=rqlited /out/rqlited /usr/local/bin/rqlited
EXPOSE 80
ENTRYPOINT ["/usr/local/bin/cobalt", "server"]