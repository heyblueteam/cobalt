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
RUN apt-get update \
    && apt-get install -y --no-install-recommends \
        ca-certificates \
        git \
        docker-cli \
    && rm -rf /var/lib/apt/lists/*
COPY --from=build /out/cobalt /usr/local/bin/cobalt
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/cobalt", "server"]
