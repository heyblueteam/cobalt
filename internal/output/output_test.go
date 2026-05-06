package output

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestPrintJSON(t *testing.T) {
	old := Stdout
	oldJSON := jsonMode
	defer func() { Stdout = old; jsonMode = oldJSON }()

	var buf bytes.Buffer
	Stdout = &buf
	jsonMode = true

	type item struct {
		Name string `json:"name"`
		Val  int    `json:"val"`
	}
	PrintJSON(item{Name: "test", Val: 42})

	var got item
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("invalid json: %v\n%s", err, buf.String())
	}
	if got.Name != "test" || got.Val != 42 {
		t.Errorf("got %+v", got)
	}
}

func TestPrintTable(t *testing.T) {
	old := Stdout
	oldJSON := jsonMode
	defer func() { Stdout = old; jsonMode = oldJSON }()

	var buf bytes.Buffer
	Stdout = &buf
	jsonMode = false

	headers := []string{"NAME", "STATUS"}
	rows := [][]string{
		{"api", "success"},
		{"web", "failed"},
	}

	PrintTable(headers, rows)
	got := buf.String()

	if !strings.Contains(got, "NAME") || !strings.Contains(got, "STATUS") {
		t.Errorf("missing headers: %s", got)
	}
	if !strings.Contains(got, "api") || !strings.Contains(got, "success") {
		t.Errorf("missing row 1: %s", got)
	}
	if !strings.Contains(got, "web") || !strings.Contains(got, "failed") {
		t.Errorf("missing row 2: %s", got)
	}
}

func TestPrintTableJSONMode(t *testing.T) {
	old := Stdout
	oldJSON := jsonMode
	defer func() { Stdout = old; jsonMode = oldJSON }()

	var buf bytes.Buffer
	Stdout = &buf
	jsonMode = true

	PrintTable([]string{"A"}, [][]string{{"1"}})
	if buf.Len() != 0 {
		t.Errorf("expected empty output in JSON mode, got: %s", buf.String())
	}
}

func TestPrintLines(t *testing.T) {
	old := Stdout
	oldJSON := jsonMode
	defer func() { Stdout = old; jsonMode = oldJSON }()

	var buf bytes.Buffer
	Stdout = &buf
	jsonMode = false

	PrintLines("line1", "line2")
	got := buf.String()
	if !strings.Contains(got, "line1\n") || !strings.Contains(got, "line2\n") {
		t.Errorf("unexpected output: %s", got)
	}
}

func TestPrintLinesJSONMode(t *testing.T) {
	old := Stdout
	oldJSON := jsonMode
	defer func() { Stdout = old; jsonMode = oldJSON }()

	var buf bytes.Buffer
	Stdout = &buf
	jsonMode = true

	PrintLines("should not appear")
	if buf.Len() != 0 {
		t.Errorf("expected empty output in JSON mode, got: %s", buf.String())
	}
}

func TestPrintKeyValue(t *testing.T) {
	old := Stdout
	oldJSON := jsonMode
	defer func() { Stdout = old; jsonMode = oldJSON }()

	var buf bytes.Buffer
	Stdout = &buf
	jsonMode = false

	PrintKeyValue([2]string{"Version", "1.0.0"}, [2]string{"Hostname", "test"})
	got := buf.String()
	if !strings.Contains(got, "Version") || !strings.Contains(got, "1.0.0") {
		t.Errorf("missing version: %s", got)
	}
	if !strings.Contains(got, "Hostname") || !strings.Contains(got, "test") {
		t.Errorf("missing hostname: %s", got)
	}
}

func TestErrf(t *testing.T) {
	old := Stderr
	defer func() { Stderr = old }()

	var buf bytes.Buffer
	Stderr = &buf

	Errf("error: %s", "test")
	got := buf.String()
	if !strings.Contains(got, "error: test") {
		t.Errorf("unexpected: %s", got)
	}
}

func TestColorStatus(t *testing.T) {
	SetColor(true)
	got := ColorStatus("success")
	if !strings.Contains(got, "success") {
		t.Errorf("expected color wrapping: %q", got)
	}
	SetColor(false)
}

func TestColorCodes(t *testing.T) {
	SetColor(true)
	tests := []string{"success", "failed", "canceled", "skipped", "queued", "fetching", "building", "swapping", "unknown"}
	for _, s := range tests {
		got := ColorStatus(s)
		if !strings.Contains(got, s) {
			t.Errorf("ColorStatus(%q) missing original: %q", s, got)
		}
	}
	SetColor(false)
}

func TestSetJSON(t *testing.T) {
	SetJSON(true)
	if !IsJSON() {
		t.Error("expected json mode")
	}
	SetJSON(false)
	if IsJSON() {
		t.Error("expected non-json mode")
	}
}

func TestPadRight(t *testing.T) {
	tests := []struct {
		s    string
		n    int
		want string
	}{
		{"ab", 5, "ab   "},
		{"abcde", 3, "abcde"},
		{"", 3, "   "},
	}
	for _, tt := range tests {
		got := padRight(tt.s, tt.n)
		if got != tt.want {
			t.Errorf("padRight(%q, %d): got %q, want %q", tt.s, tt.n, got, tt.want)
		}
	}
}
