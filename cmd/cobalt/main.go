package main

import (
	"errors"
	"os"

	"github.com/heyblueteam/cobalt/internal/client"
	"github.com/heyblueteam/cobalt/internal/output"
)

func main() {
	if err := newRootCmd().Execute(); err != nil {
		var apiErr *client.APIError
		if errors.As(err, &apiErr) {
			output.Errf("%s", apiErr.Message)
		} else {
			output.Errf("%v", err)
		}
		if confirmErr, ok := err.(confirmError); ok {
			_ = confirmErr
			os.Exit(1)
		}
		os.Exit(1)
	}
}
