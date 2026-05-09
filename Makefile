.PHONY: build test e2e

build:
	go build -o bin/cobalt ./cmd/cobalt

test:
	go test -race ./...

e2e:
	@: $${COBALT_E2E_HOST?set COBALT_E2E_HOST to your cobalt daemon (see e2e/README.md)}
	@: $${COBALT_E2E_API_KEY?set COBALT_E2E_API_KEY to a valid API key}
	go test -v -timeout 30m ./e2e/...
