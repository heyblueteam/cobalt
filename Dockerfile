FROM golang:1.25-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 go build \
    -ldflags="-s -w -X main.version=${VERSION}" \
    -o /out/cobalt ./cmd/cobalt

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/cobalt /usr/local/bin/cobalt
EXPOSE 80
ENTRYPOINT ["/usr/local/bin/cobalt", "server"]
