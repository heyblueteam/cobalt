package output

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/sys/unix"
)

var (
	Stdout io.Writer = os.Stdout
	Stderr io.Writer = os.Stderr
)

var jsonMode bool

func SetJSON(enabled bool) { jsonMode = enabled }

func IsJSON() bool { return jsonMode }

func PrintJSON(v any) {
	b, err := json.Marshal(v)
	if err != nil {
		Errf("marshal json: %v", err)
		return
	}
	fmt.Fprintln(Stdout, string(b))
}

func PrintTable(headers []string, rows [][]string) {
	if jsonMode {
		return
	}
	cols := len(headers)
	if cols == 0 {
		return
	}
	widths := make([]int, cols)
	for i, h := range headers {
		widths[i] = len(h)
	}
	for _, row := range rows {
		for i, cell := range row {
			if i < cols && len(cell) > widths[i] {
				widths[i] = len(cell)
			}
		}
	}
	for i, h := range headers {
		fmt.Fprint(Stdout, padRight(h, widths[i]))
		if i < cols-1 {
			fmt.Fprint(Stdout, "  ")
		}
	}
	fmt.Fprintln(Stdout)
	for _, row := range rows {
		for i, cell := range row {
			c := cell
			if i >= cols {
				break
			}
			fmt.Fprint(Stdout, padRight(c, widths[i]))
			if i < cols-1 {
				fmt.Fprint(Stdout, "  ")
			}
		}
		fmt.Fprintln(Stdout)
	}
}

func PrintLines(lines ...string) {
	if jsonMode {
		return
	}
	for _, l := range lines {
		fmt.Fprintln(Stdout, l)
	}
}

func PrintKeyValue(pairs ...[2]string) {
	if jsonMode {
		return
	}
	maxKey := 0
	for _, p := range pairs {
		if len(p[0]) > maxKey {
			maxKey = len(p[0])
		}
	}
	for _, p := range pairs {
		fmt.Fprintf(Stdout, "%s  %s\n", padRight(p[0], maxKey), p[1])
	}
}

func Errf(format string, args ...any) {
	fmt.Fprintf(Stderr, format+"\n", args...)
}

func IsTTY(f *os.File) bool {
	_, err := unix.IoctlGetTermios(int(f.Fd()), unix.TIOCGETA)
	return err == nil
}

func IsStdoutTTY() bool { return IsTTY(os.Stdout) }

func padRight(s string, n int) string {
	if len(s) >= n {
		return s
	}
	return s + strings.Repeat(" ", n-len(s))
}
