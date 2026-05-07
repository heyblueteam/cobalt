# Build context contains only: Dockerfile + pre-built cobalt binary
# GoReleaser cross-compiles the binary locally, then copies it into this context
FROM debian:stable-slim
ARG TARGETPLATFORM
RUN apt-get update \
    && apt-get install -y --no-install-recommends \
        ca-certificates \
        git \
        docker-cli \
    && rm -rf /var/lib/apt/lists/*
COPY ${TARGETPLATFORM}/cobalt /usr/local/bin/cobalt
EXPOSE 80
ENTRYPOINT ["/usr/local/bin/cobalt", "server"]