package docker

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

type serviceSpecRunner struct {
	*fakeRunner
	requests [][]byte
}

func (r *serviceSpecRunner) Run(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	if stdin != nil {
		raw, _ := io.ReadAll(stdin)
		r.requests = append(r.requests, raw)
		stdin = bytes.NewReader(raw)
	}
	return r.fakeRunner.Run(ctx, args, stdin, stdout, stderr)
}

func TestUpdateServiceSpec(t *testing.T) {
	for _, tc := range []struct {
		name, inspect, response string
		transportErr, wantErr   bool
		requests                int
	}{
		{name: "complete spec preserved", response: "HTTP/1.1 200 OK\r\nContent-Length: 2\r\n\r\n{}", requests: 1},
		{name: "conflict retries bounded", response: "HTTP/1.1 409 Conflict\r\nContent-Length: 0\r\n\r\n", wantErr: true, requests: 3},
		{name: "no retry forbidden", response: "HTTP/1.1 403 Forbidden\r\nContent-Length: 0\r\n\r\n", wantErr: true, requests: 1},
		{name: "malformed response", response: "bad", wantErr: true, requests: 1},
		{name: "missing metadata", inspect: `{}`, wantErr: true},
		{name: "transport failure", transportErr: true, wantErr: true, requests: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := &serviceSpecRunner{fakeRunner: newFakeRunner()}
			inspect := tc.inspect
			if inspect == "" {
				inspect = `{"ID":"abc/def","Version":{"Index":9007199254740993}}`
			}
			r.answerStdout("version --format", `{"Client":{"ApiVersion":"1.56"},"Server":{"ApiVersion":"1.56","MinAPIVersion":"1.40"}}`)
			r.answerStdout("service inspect", inspect)
			r.answerStdout("system dial-stdio", tc.response)
			if tc.transportErr {
				r.answerErr("system dial-stdio", staticErr("closed"))
			}
			spec := json.RawMessage(`{"Name":"s","TaskTemplate":{"ContainerSpec":{"Image":"v2"}},"UnknownFutureField":{"retain":true}}`)
			err := NewWithRunner(r).UpdateServiceSpec(context.Background(), "s", spec)
			if (err != nil) != tc.wantErr || len(r.requests) != tc.requests {
				t.Fatalf("err=%v requests=%d", err, len(r.requests))
			}
			if tc.name == "conflict retries bounded" {
				var status *StableServiceUpdateError
				if !errors.As(err, &status) || status.StatusCode != 409 {
					t.Fatalf("err=%v", err)
				}
			}
			if tc.wantErr {
				return
			}
			req, err := http.ReadRequest(bufio.NewReader(bytes.NewReader(r.requests[0])))
			if err != nil {
				t.Fatal(err)
			}
			body, _ := io.ReadAll(req.Body)
			if req.Method != "POST" || req.URL.EscapedPath() != "/v1.56/services/abc%2Fdef/update" || req.URL.Query().Get("version") != "9007199254740993" || !req.Close || string(body) != string(spec) {
				t.Fatalf("request=%s body=%s", r.requests[0], body)
			}
		})
	}
}

func TestUpdateServiceSpecRejectsInvalidSpecBeforeDocker(t *testing.T) {
	f := newFakeRunner()
	err := NewWithRunner(f).UpdateServiceSpec(context.Background(), "s", json.RawMessage(`{} garbage`))
	if err == nil || !strings.Contains(err.Error(), "invalid service spec") || f.callCount() != 0 {
		t.Fatalf("err=%v calls=%v", err, f.calls)
	}
}

func TestStableServiceAPIVersion(t *testing.T) {
	for _, tc := range []struct{ name, client, server, minimum, want string }{
		{"new server", "1.56", "1.56", "1.40", "1.56"},
		{"older server", "1.56", "1.41", "1.12", "1.41"},
		{"configured lower client", "1.44", "1.56", "1.40", "1.44"},
		{"unsupported client", "1.39", "1.56", "1.40", ""},
		{"missing metadata", "", "1.56", "1.40", ""},
		{"invalid server", "1.56", "1.56/unsafe", "1.40", ""},
		{"invalid major", "1.56", "2.56", "1.40", ""},
		{"invalid minimum", "1.56", "1.56", "1.57", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeRunner()
			raw, _ := json.Marshal(map[string]any{"Client": map[string]string{"ApiVersion": tc.client}, "Server": map[string]string{"ApiVersion": tc.server, "MinAPIVersion": tc.minimum}})
			f.answerStdout("version --format", string(raw))
			got, err := NewWithRunner(f).stableServiceAPIVersion(context.Background())
			if got != tc.want || (err != nil) != (tc.want == "") {
				t.Fatalf("version=%q err=%v", got, err)
			}
		})
	}
}

func TestUpdateServiceSpecInvalidVersionDoesNotMutate(t *testing.T) {
	f := newFakeRunner()
	f.answerStdout("service inspect", `{"ID":"id","Version":{"Index":1}}`)
	f.answerStdout("version --format", `{"Client":{"ApiVersion":"bad"}}`)
	err := NewWithRunner(f).UpdateServiceSpec(context.Background(), "s", json.RawMessage(`{"Name":"s"}`))
	if err == nil {
		t.Fatal("expected version error")
	}
	for _, call := range f.calls {
		if argSequence(call.Args, "system", "dial-stdio") {
			t.Fatal("mutated service with invalid version")
		}
	}
}
