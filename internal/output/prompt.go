package output

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"
)

// Stdin is the reader prompts read from. Overridable in tests.
var Stdin io.Reader = os.Stdin

// Confirm prompts the operator with "<icon> <question>  (Y/n) ›" (or
// (y/N) when defaultYes is false) and returns the answer.
//
// When assumeYes is true (e.g. global --yes flag), the prompt is
// skipped and the default is returned. When stdin is not a terminal
// and assumeYes is false, returns an error so unattended runs fail
// loudly instead of silently defaulting.
func Confirm(icon, question string, defaultYes, assumeYes bool) (bool, error) {
	if assumeYes {
		return defaultYes, nil
	}
	if !IsTTY(os.Stdin) {
		return false, fmt.Errorf("stdin is not a terminal; pass --yes for unattended runs")
	}
	suffix := "(Y/n)"
	if !defaultYes {
		suffix = "(y/N)"
	}
	fmt.Fprintf(Stderr, "%s %s  %s › ", icon, question, suffix)
	line, err := readLine(Stdin)
	if err != nil {
		return false, err
	}
	switch strings.ToLower(line) {
	case "":
		return defaultYes, nil
	case "y", "yes":
		return true, nil
	case "n", "no":
		return false, nil
	default:
		return false, fmt.Errorf("invalid response %q (expected y or n)", line)
	}
}

// Input prompts "<icon> <question>  (<default>) ›" and returns the
// operator's response, or defaultValue if they hit enter.
//
// When assumeYes is true, returns defaultValue unprompted. Errors out
// on a non-TTY stdin to keep unattended runs honest about which
// values they're leaning on.
func Input(icon, question, defaultValue string, assumeYes bool) (string, error) {
	if assumeYes {
		return defaultValue, nil
	}
	if !IsTTY(os.Stdin) {
		return "", fmt.Errorf("stdin is not a terminal; pass --yes (or the relevant flag) for unattended runs")
	}
	fmt.Fprintf(Stderr, "%s %s  (%s) › ", icon, question, defaultValue)
	line, err := readLine(Stdin)
	if err != nil {
		return "", err
	}
	if line == "" {
		return defaultValue, nil
	}
	return line, nil
}

// InputOptional is like Input but treats an empty default as
// "leave empty for X" rather than substituting a value. Returns the
// raw line (possibly empty).
func InputOptional(icon, question, hint string, assumeYes bool) (string, error) {
	if assumeYes {
		return "", nil
	}
	if !IsTTY(os.Stdin) {
		return "", fmt.Errorf("stdin is not a terminal; pass --yes for unattended runs")
	}
	fmt.Fprintf(Stderr, "%s %s  (%s) › ", icon, question, hint)
	return readLine(Stdin)
}

func readLine(r io.Reader) (string, error) {
	br := bufio.NewReader(r)
	line, err := br.ReadString('\n')
	if err != nil && line == "" {
		return "", err
	}
	return strings.TrimSpace(line), nil
}
