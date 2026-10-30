package docker

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"reflect"
	"testing"
)

func TestReconcileStableServiceNetworks(t *testing.T) {
	for _, tc := range []struct {
		name, old string
		update    bool
	}{
		{name: "identical no mutation", old: `[{"Target":"id-main","Aliases":["stable"]}]`},
		{name: "alias handoff preserves attachment options", old: `[{"Target":"id-main","Aliases":["stable","private"],"DriverOpts":{"retain":"yes"}}]`, update: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := &serviceSpecRunner{fakeRunner: newFakeRunner()}
			r.answerStdout("service inspect --format {{json .Spec}}", `{"Name":"stable","TaskTemplate":{"ContainerSpec":{"Image":"unchanged","Healthcheck":{"Test":["CMD","/health"]}},"Networks":`+tc.old+`},"UpdateConfig":{"Monitor":90000000000}}`)
			r.answerStdout("service inspect --format {{json .}}", `{"ID":"id","Version":{"Index":4}}`)
			r.answerStdout("network inspect", `"id-main"`)
			r.answerStdout("version --format", `{"Client":{"ApiVersion":"1.56"},"Server":{"ApiVersion":"1.56","MinAPIVersion":"1.40"}}`)
			r.answerStdout("system dial-stdio", "HTTP/1.1 200 OK\r\nContent-Length: 2\r\n\r\n{}")
			err := NewWithRunner(r).ReconcileStableServiceNetworks(context.Background(), ServiceCreateOpts{Name: "stable", Networks: []NetworkAttachment{{Name: "main", Alias: "stable"}}})
			if err != nil {
				t.Fatal(err)
			}
			if got := len(r.requests); got != map[bool]int{false: 0, true: 1}[tc.update] {
				t.Fatalf("update requests=%d", got)
			}
			if !tc.update {
				return
			}
			req, err := http.ReadRequest(bufio.NewReader(bytes.NewReader(r.requests[0])))
			if err != nil {
				t.Fatal(err)
			}
			body, _ := io.ReadAll(req.Body)
			spec := decodeStableSettings(t, body)
			task := spec["TaskTemplate"].(map[string]any)
			network := task["Networks"].([]any)[0].(map[string]any)
			if network["Target"] != "id-main" || !reflect.DeepEqual(network["Aliases"], []any{"stable"}) || network["DriverOpts"].(map[string]any)["retain"] != "yes" {
				t.Fatalf("network=%v", network)
			}
			container := task["ContainerSpec"].(map[string]any)
			if container["Image"] != "unchanged" || !reflect.DeepEqual(container["Healthcheck"].(map[string]any)["Test"], []any{"CMD", "/health"}) {
				t.Fatalf("container settings changed=%v", container)
			}
			if got := spec["RollbackConfig"].(map[string]any)["Order"]; got != "stop-first" {
				t.Fatalf("alias automatic rollback order=%v", got)
			}
			update := spec["UpdateConfig"].(map[string]any)
			if update["Order"] != "start-first" || update["FailureAction"] != "rollback" || update["Monitor"] != float64(90000000000) {
				t.Fatalf("update policy=%v", update)
			}
		})
	}
}

func TestStableNetworksEqualIgnoresOrdering(t *testing.T) {
	old := json.RawMessage(`[{"Target":"a","Aliases":["stable","private"],"DriverOpts":{"preserve":"yes"}},{"Target":"b"}]`)
	var desired []stableJSONObject
	_ = json.Unmarshal([]byte(`[{"Target":"b","Aliases":null},{"Target":"a","Aliases":["private","stable","stable"],"DriverOpts":{"preserve":"yes"}}]`), &desired)
	equal, err := stableNetworksEqual(old, desired)
	if err != nil || !equal {
		t.Fatalf("equal=%v err=%v", equal, err)
	}
}

func TestStableNetworksFailClosed(t *testing.T) {
	for _, snapshot := range []string{`invalid`, `{"Name":"stable","TaskTemplate":{"Networks":[{"Target":"id","Aliases":"bad"}]}}`} {
		t.Run(snapshot, func(t *testing.T) {
			r := &serviceSpecRunner{fakeRunner: newFakeRunner()}
			r.answerStdout("service inspect", snapshot)
			r.answerStdout("network inspect", `"id"`)
			err := NewWithRunner(r).ReconcileStableServiceNetworks(context.Background(), ServiceCreateOpts{Name: "stable", Networks: []NetworkAttachment{{Name: "main", Alias: "stable"}}})
			if err == nil || len(r.requests) != 0 {
				t.Fatalf("err=%v updates=%d", err, len(r.requests))
			}
		})
	}
}
