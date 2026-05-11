.PHONY: build test e2e

build:
	go build -o bin/cobalt ./cmd/cobalt

# -p 1 runs packages serially. Several packages spawn their own rqlited
# docker container per test; running packages in parallel created 30+
# concurrent containers, starved CPU under -race, and caused websocket
# tests in internal/server/api to miss their 5s frame-round-trip deadlines.
# Serial packages add ~2min wall-clock and eliminate the flake class.
test:
	go test -race -p 1 ./...

e2e:
	@: $${COBALT_E2E_HOST?set COBALT_E2E_HOST to your cobalt daemon (see e2e/README.md)}
	@: $${COBALT_E2E_API_KEY?set COBALT_E2E_API_KEY to a valid API key}
	go test -v -timeout 30m ./e2e/...
