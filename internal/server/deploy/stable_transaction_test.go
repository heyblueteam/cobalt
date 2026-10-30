package deploy

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/heyblueteam/cobalt/internal/server/cobaltfile"
	"github.com/heyblueteam/cobalt/internal/server/docker"
	"github.com/heyblueteam/cobalt/internal/server/store"
)

func TestStableWebPrivateAliasStagedUntilHealth(t *testing.T) {
	for _, existing := range []bool{false, true} {
		t.Run(map[bool]string{false: "new alias", true: "existing alias"}[existing], func(t *testing.T) {
			runner := newOrchDocker()
			aliases := []string{"cobalt-web-7"}
			if existing {
				aliases = append(aliases, "sample-web")
			}
			raw, _ := json.Marshal(map[string]any{"Name": "cobalt-web-7", "TaskTemplate": map[string]any{"Networks": []map[string]any{{"Aliases": aliases}}}})
			runner.stdout["service inspect --format {{json .Spec}}"] = string(raw)
			cf, err := cobaltfile.Parse([]byte(`{"version":"1.0","stablePublicWeb":true,"stablePublicWebNetwork":"shared","services":{"web":{"privateAddress":true}}}`))
			if err != nil {
				t.Fatal(err)
			}
			p := store.Project{ID: 7, Name: "sample"}
			dep := store.Deployment{Number: 2}
			built := []BuiltService{{Name: "web", Service: cf.Services["web"], ImageTag: "app:2"}}
			tx, err := prepareStableWebTransaction(context.Background(), docker.NewWithRunner(runner), p, dep, cf, built, nil, true)
			if err != nil {
				t.Fatal(err)
			}
			opts := stablePublicWebServiceOpts(p, dep, built[0], nil)
			tx.stage(&opts)
			if got := len(opts.Networks[0].AdditionalAliases) > 0; got != existing {
				t.Fatalf("private alias advertised before health: %+v", opts.Networks)
			}
			if len(tx.desired.Networks[0].AdditionalAliases) != 1 {
				t.Fatal("staging mutated desired alias")
			}
			for _, call := range runner.calls {
				if !strings.HasPrefix(call, "service inspect") {
					t.Fatalf("prepare mutated service: %s", call)
				}
			}
		})
	}
}

func TestStableWebSnapshotFailureDoesNotMutate(t *testing.T) {
	runner := newOrchDocker()
	runner.errs["service inspect"] = errors.New("inspect unavailable")
	cf := cobaltfile.Default()
	cf.StablePublicWeb = true
	_, err := prepareStableWebTransaction(context.Background(), docker.NewWithRunner(runner), store.Project{ID: 7}, store.Deployment{}, cf, nil, nil, false)
	if err == nil {
		t.Fatal("expected snapshot failure")
	}
	if len(runner.calls) != 1 || !strings.HasPrefix(runner.calls[0], "service inspect") {
		t.Fatalf("calls=%v", runner.calls)
	}
}

func TestStablePrivateAliasCollisionRejectedBeforeMutation(t *testing.T) {
	runner := newOrchDocker()
	runner.stdout["service ls"] = "sample-1-web\t1/1"
	runner.stdout["service inspect --format {{json .Spec}}"] = `{"TaskTemplate":{"Networks":[{"Aliases":["sample-web"]}]}}`
	cf := cobaltfile.Default()
	cf.StablePublicWeb = true
	web := cf.Services["web"]
	web.PrivateAddress = true
	cf.Services["web"] = web
	err := validateStablePrivateAlias(context.Background(), docker.NewWithRunner(runner), store.Project{ID: 7, Name: "sample"}, cf)
	if err == nil || !strings.Contains(err.Error(), "already owned") {
		t.Fatalf("err=%v", err)
	}
	for _, call := range runner.calls {
		if !strings.HasPrefix(call, "service ls") && !strings.HasPrefix(call, "service inspect") {
			t.Fatalf("mutated before collision rejection: %s", call)
		}
	}
}

func TestStableRecoveryHonorsPreviousLongerStopGrace(t *testing.T) {
	cf := cobaltfile.Default()
	web := cf.Services["web"]
	web.StopGracePeriodSeconds = 35
	cf.Services["web"] = web
	tx := &stableWebTransaction{snapshot: json.RawMessage(`{"TaskTemplate":{"ContainerSpec":{"StopGracePeriod":3600000000000}}}`)}
	if got := tx.recoveryBudget(cf); got != time.Hour+2*HealthcheckTimeout {
		t.Fatalf("recovery budget=%s", got)
	}
}

func TestStablePublicToInternalRejectedBeforeMutation(t *testing.T) {
	runner := newOrchDocker()
	runner.stdout["service ls"] = "cobalt-web-7\t1/1\nsample-1-worker\t1/1"
	cf := cobaltfile.Default()
	web := cf.Services["web"]
	web.ExposedInternally = true
	cf.Services["web"] = web
	err := validateStablePrivateAlias(context.Background(), docker.NewWithRunner(runner), store.Project{ID: 7, Name: "sample"}, cf)
	if err == nil || !strings.Contains(err.Error(), "first deploy numbered public web") {
		t.Fatalf("err=%v", err)
	}
	if len(runner.calls) != 1 || !strings.HasPrefix(runner.calls[0], "service ls") {
		t.Fatalf("mutation before transition rejection: %v", runner.calls)
	}
}
