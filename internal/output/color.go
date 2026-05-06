package output

import (
	"fmt"
	"strings"
)

const (
	colorReset  = "\033[0m"
	colorRed    = "\033[31m"
	colorGreen  = "\033[32m"
	colorYellow = "\033[33m"
)

var colorEnabled bool

func SetColor(enabled bool) { colorEnabled = enabled }

func ColorStatus(status string) string {
	if !colorEnabled {
		return status
	}
	lower := strings.ToLower(status)
	switch lower {
	case "success":
		return fmt.Sprintf("%s%s%s", colorGreen, status, colorReset)
	case "failed", "canceled", "skipped":
		return fmt.Sprintf("%s%s%s", colorRed, status, colorReset)
	case "queued", "fetching", "building", "swapping":
		return fmt.Sprintf("%s%s%s", colorYellow, status, colorReset)
	default:
		return status
	}
}
