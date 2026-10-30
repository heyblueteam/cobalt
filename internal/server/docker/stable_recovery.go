package docker

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"reflect"
	"strings"
	"time"
)

// SnapshotStableService captures the complete rollback target before mutating a
// stable service. Only an absent service produces a nil snapshot.
func (c *Client) SnapshotStableService(ctx context.Context, name string) (json.RawMessage, error) {
	out, err := c.output(ctx, "service", "inspect", "--format", "{{json .Spec}}", name)
	if err != nil {
		if isNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	if _, err := stableSpecValue(out); err != nil {
		return nil, fmt.Errorf("snapshot stable service %s: %w", name, err)
	}
	return json.RawMessage(out), nil
}

func stableSpecValue(raw []byte) (map[string]any, error) {
	var spec map[string]any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&spec); err != nil {
		return nil, err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return nil, fmt.Errorf("invalid trailing service spec data")
	}
	if len(spec) == 0 {
		return nil, fmt.Errorf("missing service spec")
	}
	return spec, nil
}

// RestoreStableService removes a first-deploy service or requests rollback to
// the captured spec. Per-project deployment locking owns mutations; Docker's
// optimistic version check protects each complete-spec update.
// Call WaitForStableServiceSettled before retiring dependencies of the old spec.
func (c *Client) RestoreStableService(ctx context.Context, name string, snapshot json.RawMessage) error {
	if snapshot == nil {
		return c.RetireService(ctx, name)
	}
	want, err := stableSpecValue(snapshot)
	if err != nil {
		return fmt.Errorf("invalid stable service snapshot: %w", err)
	}
	out, err := c.output(ctx, "service", "inspect", "--format", "{{json .}}", name)
	if err != nil {
		return err
	}
	var service struct {
		Spec json.RawMessage
	}
	if err := json.Unmarshal(out, &service); err != nil {
		return err
	}
	current, err := stableSpecValue(service.Spec)
	if err != nil {
		return err
	}
	if reflect.DeepEqual(current, want) {
		return nil
	}
	return c.UpdateServiceSpec(ctx, name, snapshot)
}

// WaitForStableServiceSettled waits on manager-visible Swarm tasks, including
// tasks on remote nodes. Old live generations block dependency cleanup even
// after the replacement generation has reached its desired replica count.
// The caller must supply a bounded context.
func (c *Client) WaitForStableServiceSettled(ctx context.Context, name string) error {
	for {
		settled, err := c.stableServiceSettled(ctx, name)
		if err != nil {
			return fmt.Errorf("wait stable service %s: %w", name, err)
		}
		if settled {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
	}
}

func (c *Client) stableServiceSettled(ctx context.Context, name string) (bool, error) {
	out, err := c.output(ctx, "service", "inspect", "--format", "{{json .}}", name)
	if err != nil {
		return false, err
	}
	var service struct {
		Spec struct {
			TaskTemplate struct {
				ContainerSpec struct{ Labels map[string]string }
			}
			Mode struct{ Replicated *struct{ Replicas *uint64 } }
		}
		UpdateStatus *struct{ State string }
	}
	if err := json.Unmarshal(out, &service); err != nil {
		return false, err
	}
	generation := service.Spec.TaskTemplate.ContainerSpec.Labels[LabelDeploymentNumber]
	if generation == "" || service.Spec.Mode.Replicated == nil || service.Spec.Mode.Replicated.Replicas == nil {
		return false, fmt.Errorf("missing stable service generation or replica count")
	}
	if service.UpdateStatus != nil {
		switch service.UpdateStatus.State {
		case "completed", "rollback_completed":
		case "paused", "rollback_paused":
			return false, fmt.Errorf("service update %s", service.UpdateStatus.State)
		default:
			return false, nil
		}
	}
	ids, err := c.output(ctx, "service", "ps", name, "--no-trunc", "--quiet")
	if err != nil {
		return false, err
	}
	taskIDs := strings.Fields(string(ids))
	if len(taskIDs) == 0 {
		return false, nil
	}
	args := append([]string{"inspect", "--type", "task"}, taskIDs...)
	out, err = c.output(ctx, args...)
	if err != nil {
		return false, err
	}
	var tasks []struct {
		Spec struct {
			ContainerSpec struct{ Labels map[string]string }
		}
		Status       struct{ State string }
		DesiredState string
	}
	if err := json.Unmarshal(out, &tasks); err != nil {
		return false, err
	}
	if len(tasks) != len(taskIDs) {
		return false, fmt.Errorf("incomplete swarm task inspection")
	}
	var running uint64
	for _, task := range tasks {
		switch task.Status.State {
		case "shutdown", "complete", "failed", "rejected", "remove":
			continue
		}
		if task.Spec.ContainerSpec.Labels[LabelDeploymentNumber] != generation {
			return false, nil
		}
		if task.Status.State == "running" && task.DesiredState == "running" {
			running++
		} else {
			return false, nil
		}
	}
	return running >= *service.Spec.Mode.Replicated.Replicas, nil
}
