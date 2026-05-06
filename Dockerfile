FROM golang:1.25-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 go build \
    -ldflags="-s -w -X main.version=${VERSION}" \
    -o /out/cobalt ./cmd/cobalt

# Runtime needs git (for repo fetch) and docker CLI (for shelling out to
# the host docker daemon via the mounted socket). Distroless doesn't
# include either, so we use debian-slim.
FROM debian:stable-slim
RUN apt-get update \
    && apt-get install -y --no-install-recommends \
        ca-certificates \
        git \
        docker-cli \
    && rm -rf /var/lib/apt/lists/*
COPY --from=build /out/cobalt /usr/local/bin/cobalt
EXPOSE 80
ENTRYPOINT ["/usr/local/bin/cobalt", "server"]
