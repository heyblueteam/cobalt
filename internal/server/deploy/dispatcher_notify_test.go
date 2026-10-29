package deploy

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/heyblueteam/cobalt/internal/server/store"
	"github.com/heyblueteam/cobalt/pkg/cobaltapi"
)

type notifyCall struct {
	dep    store.Deployment
	status cobaltapi.State
	err    error
}

type recordingNotifier struct {
	calls chan notifyCall
}

func newRecordingNotifier() *recordingNotifier {
	return &recordingNotifier{calls: make(chan notifyCall, 8)}
}

func (n *recordingNotifier) Notify(_ context.Context, dep store.Deployment, status cobaltapi.State, err error) error {
	n.calls <- notifyCall{dep: dep, status: status, err: err}
	return nil
}

func TestDispatcher_NotifiesSuccess(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	q := NewQueue(db)
	pid := newProject(t, db, "api")
	id, _, err := q.Enqueue(context.Background(), EnqueueRequest{ProjectID: pid})
	if err != nil {
		t.Fatal(err)
	}
	runner := newRecordingRunner()
	close(runner.gate)
	notifier := newRecordingNotifier()

	d := NewDispatcher(db, runner, quietLogger(), DispatcherOpts{
		PollInterval: 50 * time.Millisecond,
		Notifier:     notifier,
	})
	d.Start(context.Background())
	t.Cleanup(d.Stop)

	select {
	case call := <-notifier.calls:
		if call.dep.ID != id {
			t.Errorf("deployment id: got %d, want %d", call.dep.ID, id)
		}
		if call.status != cobaltapi.StateSuccess {
			t.Errorf("status: got %q, want success", call.status)
		}
		if call.err != nil {
			t.Errorf("err: got %v, want nil", call.err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for notification")
	}
}

func TestDispatcher_NotifiesFailureWithError(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	q := NewQueue(db)
	pid := newProject(t, db, "api")
	id, _, err := q.Enqueue(context.Background(), EnqueueRequest{ProjectID: pid})
	if err != nil {
		t.Fatal(err)
	}
	deployErr := errors.New("deploy: healthcheck timeout")
	runner := newRecordingRunner()
	runner.returns[id] = deployErr
	close(runner.gate)
	notifier := newRecordingNotifier()

	d := NewDispatcher(db, runner, quietLogger(), DispatcherOpts{
		PollInterval: 50 * time.Millisecond,
		Notifier:     notifier,
	})
	d.Start(context.Background())
	t.Cleanup(d.Stop)

	select {
	case call := <-notifier.calls:
		if call.dep.ID != id {
			t.Errorf("deployment id: got %d, want %d", call.dep.ID, id)
		}
		if call.status != cobaltapi.StateFailed {
			t.Errorf("status: got %q, want failed", call.status)
		}
		if !errors.Is(call.err, deployErr) {
			t.Errorf("err: got %v, want %v", call.err, deployErr)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for notification")
	}
}
