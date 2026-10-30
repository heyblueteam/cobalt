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
	"time"
)

const (
	stableSettingsBefore = `{"Name":"stable","FutureSpec":{"keep":9007199254740993},"Labels":{"custom":"keep","cobalt.deployment.number":"1"},"TaskTemplate":{"FutureTask":"keep","ContainerSpec":{"Image":"old","Command":["old-entrypoint"],"Args":["old-cmd"],"Env":["REMOVED=old","OVERRIDE=old"],"Labels":{"custom":"keep"},"Healthcheck":{"Test":["CMD-SHELL","old-health"],"Interval":7000000000},"StopGracePeriod":75000000000,"Hosts":["1.2.3.4 removed"],"Mounts":[{"Type":"volume","Source":"cache","Target":"/cache","VolumeOptions":{"NoCopy":true}},{"Type":"volume","Source":"old","Target":"/replace"},{"Type":"volume","Source":"gone","Target":"/gone"}],"FutureContainer":"keep"},"Networks":[{"Target":"main-id","Aliases":["stable","old-private"],"DriverOpts":{"preserve":"yes"}}]},"Mode":{"Replicated":{"Replicas":2}},"UpdateConfig":{"Delay":1000000000},"RollbackConfig":{"Monitor":5000000000}}`
	stableSettingsImage  = `{"Entrypoint":["/entrypoint"],"Cmd":["image-default","arg with space"],"Env":["IMAGE=default","OVERRIDE=image"],"Healthcheck":{"Test":["CMD","/health","--exec"],"Interval":30000000000,"Timeout":5000000000,"Retries":4}}`
)

func decodeStableSettings(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	var result map[string]any
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatal(err)
	}
	return result
}

func stableSettingsContainer(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	return decodeStableSettings(t, raw)["TaskTemplate"].(map[string]any)["ContainerSpec"].(map[string]any)
}

func TestStableSettingsRestoresImageDefaultsAndPreservesUnknownFields(t *testing.T) {
	f := newFakeRunner()
	f.answerStdout("image inspect", stableSettingsImage)
	f.answerStdout("network inspect", `"main-id"`)
	opts := ServiceCreateOpts{Name: "stable", Image: "new", ProjectID: 7, ProjectName: "project", ServiceName: "web", DeploymentNumber: 2, EnvVars: map[string]string{"OVERRIDE": "user", "USER": "new"}, Networks: []NetworkAttachment{{Name: "main", Alias: "stable", AdditionalAliases: []string{"private"}}}, Volumes: []ServiceVolume{{VolumeName: "cache", DestinationPath: "/cache"}, {VolumeName: "new", DestinationPath: "/replace"}}, ExtraParams: []string{"--host", "fresh:5.6.7.8"}}
	raw, err := NewWithRunner(f).stableSettingsSpec(context.Background(), opts, json.RawMessage(stableSettingsBefore))
	if err != nil {
		t.Fatal(err)
	}
	spec := decodeStableSettings(t, raw)
	task := spec["TaskTemplate"].(map[string]any)
	container := stableSettingsContainer(t, raw)
	for key, want := range map[string]any{"Image": "new", "Command": []any{"/entrypoint"}, "Args": []any{"image-default", "arg with space"}, "Env": []any{"IMAGE=default", "OVERRIDE=user", "USER=new"}, "StopGracePeriod": float64(10 * time.Second), "Hosts": []any{"5.6.7.8 fresh"}, "FutureContainer": "keep"} {
		if !reflect.DeepEqual(container[key], want) {
			t.Errorf("%s=%v want=%v", key, container[key], want)
		}
	}
	var image map[string]any
	_ = json.Unmarshal([]byte(stableSettingsImage), &image)
	if !reflect.DeepEqual(container["Healthcheck"], image["Healthcheck"]) {
		t.Fatalf("exec image healthcheck not restored: %v", container["Healthcheck"])
	}
	if !bytes.Contains(raw, []byte(`9007199254740993`)) || task["FutureTask"] != "keep" {
		t.Fatalf("unknown spec fields lost: %s", raw)
	}
	mounts := container["Mounts"].([]any)
	if len(mounts) != 2 || mounts[0].(map[string]any)["VolumeOptions"].(map[string]any)["NoCopy"] != true || mounts[1].(map[string]any)["Source"] != "new" {
		t.Fatalf("mount reconciliation: %v", mounts)
	}
	network := task["Networks"].([]any)[0].(map[string]any)
	if !reflect.DeepEqual(network["Aliases"], []any{"private", "stable"}) || network["Target"] != "main-id" || network["DriverOpts"].(map[string]any)["preserve"] != "yes" {
		t.Fatalf("network reconciliation: %v", network)
	}
	for _, labels := range []any{spec["Labels"], container["Labels"]} {
		m := labels.(map[string]any)
		if m["custom"] != "keep" || m[LabelDeploymentNumber] != "2" {
			t.Fatalf("labels=%v", m)
		}
	}
	update := spec["UpdateConfig"].(map[string]any)
	if update["Order"] != "start-first" || update["FailureAction"] != "rollback" || update["Monitor"] != float64(90*time.Second) || update["Delay"] != float64(time.Second) {
		t.Fatalf("update=%v", update)
	}
}

func TestStableSettingsCustomHealthAndCommand(t *testing.T) {
	f := newFakeRunner()
	f.answerStdout("image inspect", stableSettingsImage)
	opts := ServiceCreateOpts{Name: "stable", Image: "new", Command: `sh -c 'serve --port 8080'`, HealthCommand: "check", StopGracePeriod: 75 * time.Second, Replicas: 3}
	raw, err := NewWithRunner(f).stableSettingsSpec(context.Background(), opts, json.RawMessage(stableSettingsBefore))
	if err != nil {
		t.Fatal(err)
	}
	container := stableSettingsContainer(t, raw)
	health := container["Healthcheck"].(map[string]any)
	if !reflect.DeepEqual(container["Args"], []any{"sh", "-c", "serve --port 8080"}) || container["StopGracePeriod"] != float64(75*time.Second) {
		t.Fatalf("container=%v", container)
	}
	if !reflect.DeepEqual(health["Test"], []any{"CMD-SHELL", "check"}) || health["StartPeriod"] != float64(5*time.Minute) || health["StartInterval"] != float64(3*time.Second) {
		t.Fatalf("health=%v", health)
	}
	if health["Interval"] != float64(30*time.Second) || health["Timeout"] != float64(5*time.Second) || health["Retries"] != float64(4) {
		t.Fatalf("image health defaults were not preserved: %v", health)
	}
	if health["Interval"] == float64(7*time.Second) {
		t.Fatal("stale previous health interval retained")
	}
}

func TestStableSettingsRemovesHealthWhenNewImageHasNone(t *testing.T) {
	f := newFakeRunner()
	f.answerStdout("image inspect", `{"Cmd":["serve"]}`)
	raw, err := NewWithRunner(f).stableSettingsSpec(context.Background(), ServiceCreateOpts{Image: "no-health"}, json.RawMessage(stableSettingsBefore))
	if err != nil {
		t.Fatal(err)
	}
	container := stableSettingsContainer(t, raw)
	if _, ok := container["Healthcheck"]; ok {
		t.Fatalf("stale health retained: %v", container["Healthcheck"])
	}
	if _, ok := container["Command"]; ok {
		t.Fatal("stale entrypoint retained")
	}
	if !reflect.DeepEqual(container["Hosts"], []any{}) || !reflect.DeepEqual(container["Mounts"], []any{}) {
		t.Fatalf("removed settings retained: %v", container)
	}
}

func TestReconcileStableServiceAtomicSettings(t *testing.T) {
	r := &serviceSpecRunner{fakeRunner: newFakeRunner()}
	r.answerStdout("service inspect --format {{json .Spec}}", stableSettingsBefore)
	r.answerStdout("service inspect --format {{json .}}", `{"ID":"id","Version":{"Index":4}}`)
	r.answerStdout("image inspect", stableSettingsImage)
	r.answerStdout("version --format", `{"Client":{"ApiVersion":"1.56"},"Server":{"ApiVersion":"1.56","MinAPIVersion":"1.40"}}`)
	r.answerStdout("system dial-stdio", "HTTP/1.1 200 OK\r\nContent-Length: 2\r\n\r\n{}")
	err := NewWithRunner(r).ReconcileStableService(context.Background(), ServiceCreateOpts{Name: "stable", Image: "new", DeploymentNumber: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(r.requests) != 1 {
		t.Fatalf("expected one atomic spec update, got %d", len(r.requests))
	}
	req, err := http.ReadRequest(bufio.NewReader(bytes.NewReader(r.requests[0])))
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(req.Body)
	container := stableSettingsContainer(t, body)
	if container["Image"] != "new" || !reflect.DeepEqual(container["Args"], []any{"image-default", "arg with space"}) {
		t.Fatalf("outgoing spec=%s", body)
	}
	for _, call := range r.calls {
		if argSequence(call.Args, "service", "update") {
			t.Fatal("intermediate CLI settings update")
		}
	}
}

func TestStableSettingsInvalidImageDoesNotUpdate(t *testing.T) {
	for _, image := range []string{"", `{broken`, `null`, `{}`} {
		t.Run(image, func(t *testing.T) {
			f := newFakeRunner()
			f.answerStdout("service inspect", stableSettingsBefore)
			f.answerStdout("image inspect", image)
			if err := NewWithRunner(f).ReconcileStableService(context.Background(), ServiceCreateOpts{Name: "stable", Image: "new"}); err == nil {
				t.Fatal("expected image metadata error")
			}
			for _, call := range f.calls {
				if argSequence(call.Args, "system", "dial-stdio") {
					t.Fatal("mutated service after metadata failure")
				}
			}
		})
	}
}
