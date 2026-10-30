package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/heyblueteam/cobalt/internal/server/cobaltfile"
	"github.com/heyblueteam/cobalt/internal/server/docker"
	"github.com/heyblueteam/cobalt/internal/server/store"
)

type aliasReconcilerFake struct {
	raw   json.RawMessage
	calls []docker.ServiceCreateOpts
}

func (f *aliasReconcilerFake) SnapshotStableService(context.Context, string) (json.RawMessage, error) {
	return f.raw, nil
}

func (f *aliasReconcilerFake) ReconcileStableServiceNetworks(_ context.Context, o docker.ServiceCreateOpts) error {
	f.calls = append(f.calls, o)
	return nil
}

func aliasSpec(project, service string, generation int) json.RawMessage {
	return json.RawMessage(fmt.Sprintf(`{"Labels":{"cobalt.project.id":%q,"cobalt.service.name":%q,"cobalt.deployment.number":%q}}`, project, service, fmt.Sprint(generation)))
}

func TestStableWebAliasesFollowConfiguration(t *testing.T) {
	t.Parallel()
	p := store.Project{ID: 7, Name: "renamed"}
	live := store.Deployment{Number: 42}
	d := &aliasReconcilerFake{raw: aliasSpec("7", "web", 42)}
	for _, private := range []bool{true, false, true} {
		cf, err := cobaltfile.Parse([]byte(fmt.Sprintf(`{"version":"1.0","stablePublicWeb":true,"services":{"web":{"port":3000,"privateAddress":%t}}}`, private)))
		if err != nil {
			t.Fatal(err)
		}
		if err := reconcileStableWebAliases(context.Background(), quietCronLog(), d, p, live, cf); err != nil {
			t.Fatal(err)
		}
		got := d.calls[len(d.calls)-1]
		if got.Name != "cobalt-web-7" || len(got.Networks) != 1 || got.Networks[0].Name != "cobalt-main" || got.Networks[0].Alias != "cobalt-web-7" {
			t.Fatalf("stable web networking: %+v", got)
		}
		aliases := got.Networks[0].AdditionalAliases
		if private {
			if len(aliases) != 1 || aliases[0] != "renamed-web" {
				t.Fatalf("private alias: %v", aliases)
			}
		} else if len(aliases) != 0 {
			t.Fatalf("removed alias remains: %v", aliases)
		}
	}
}

func TestStableWebAliasesSkipAbsentAndFailedOwnership(t *testing.T) {
	t.Parallel()
	cf, err := cobaltfile.Parse([]byte(`{"version":"1.0","stablePublicWeb":true,"services":{"web":{"port":3000,"privateAddress":true}}}`))
	if err != nil {
		t.Fatal(err)
	}
	for _, raw := range []json.RawMessage{nil, aliasSpec("7", "web", 43), aliasSpec("8", "web", 42), aliasSpec("7", "worker", 42), json.RawMessage(`{}`)} {
		d := &aliasReconcilerFake{raw: raw}
		if err := reconcileStableWebAliases(context.Background(), quietCronLog(), d, store.Project{ID: 7, Name: "app"}, store.Deployment{Number: 42}, cf); err != nil {
			t.Fatal(err)
		}
		if len(d.calls) != 0 {
			t.Fatalf("mutated unowned/failed/absent service: %s", raw)
		}
	}
	cf.StablePublicWeb = false
	d := &aliasReconcilerFake{raw: aliasSpec("7", "web", 42)}
	if err := reconcileStableWebAliases(context.Background(), quietCronLog(), d, store.Project{ID: 7}, store.Deployment{Number: 42}, cf); err != nil {
		t.Fatal(err)
	}
	if len(d.calls) != 0 {
		t.Fatal("mutated legacy service")
	}
}
