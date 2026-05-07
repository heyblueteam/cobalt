# syntax=docker/dockerfile:1

FROM golang:1.25-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build \
    -ldflags="-s -w -X main.version=${VERSION}" \
    -o /out/cobalt-amd64 ./cmd/cobalt

FROM golang:1.25-alpine AS build-arm64
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build \
    -ldflags="-s -w -X main.version=${VERSION}" \
    -o /out/cobalt-arm64 ./cmd/cobalt

FROM debian:stable-slim
RUN apt-get update \
    && apt-get install -y --no-install-recommends \
        ca-certificates \
        git \
        docker-cli \
    && rm -rf /var/lib/apt/lists/*
COPY --from=build /out/cobalt-amd64 /usr/local/bin/cobalt
COPY --from=build-arm64 /out/cobalt-arm64 /usr/local/bin/cobalt
COPY --from=build-arm64 /out/cobalt-arm64 /cobalt-arm64
EXPOSE 80
ENTRYPOINT ["/usr/local/bin/cobalt", "server"]