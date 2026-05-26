package main

import (
	"errors"
	"os"

	"github.com/heyblueteam/cobalt/internal/client"
	"github.com/heyblueteam/cobalt/internal/output"
)

func main() {
	if err := newRootCmd().Execute(); err != nil {
		// A propagated exit code (from `cobalt run` etc.) wins over
		// the generic 1 — and we suppress the error print for it
		// because the upstream command already wrote its output.
		var exitErr exitCodeError
		if errors.As(err, &exitErr) {
			os.Exit(exitErr.Code)
		}
		var apiErr *client.APIError
		if errors.As(err, &apiErr) {
			output.Errf("%s", apiErr.Message)
		} else {
			output.Errf("%v", err)
		}
		os.Exit(1)
	}
}
