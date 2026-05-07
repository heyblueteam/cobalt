# Multi-stage Dockerfile for goreleaser docker builds.
# GoReleaser cross-compiles the binary locally, then creates a build context
# containing just the Dockerfile + the pre-built binary.
# The binary lands at linux/amd64/cobalt or linux/arm64/cobalt depending on
# which goarch the dockers entry targets.
FROM debian:stable-slim

RUN apt-get update \
    && apt-get install -y --no-install-recommends \
        ca-certificates \
        git \
        docker-cli \
    && rm -rf /var/lib/apt/lists/*

# goreleaser copies linux/<arch>/cobalt into the context root for each
# docker build entry, so the binary is always at the context root.
COPY cobalt /usr/local/bin/cobalt

EXPOSE 80

ENTRYPOINT ["/usr/local/bin/cobalt", "server"]