package docker

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"reflect"
	"slices"
)

func (c *Client) stableNetworkID(ctx context.Context, name string) (string, error) {
	out, err := c.output(ctx, "network", "inspect", "--format", "{{json .Id}}", name)
	if err != nil {
		return "", err
	}
	var id string
	if err := json.Unmarshal(out, &id); err != nil {
		return "", err
	}
	if id == "" {
		return "", fmt.Errorf("network %s missing ID", name)
	}
	return id, nil
}

func normalizedAliases(aliases []string) []string {
	result := append([]string(nil), aliases...)
	slices.Sort(result)
	return slices.Compact(result)
}

// stableNetworkSpecs preserves attachment options while changing owned target
// and alias fields. Keeping the same target retains the existing service VIP.
func (c *Client) stableNetworkSpecs(ctx context.Context, previous json.RawMessage, wanted []NetworkAttachment) ([]stableJSONObject, error) {
	var old []stableJSONObject
	if len(previous) > 0 {
		if err := json.Unmarshal(previous, &old); err != nil {
			return nil, err
		}
	}
	networks := make([]stableJSONObject, 0, len(wanted))
	for _, want := range wanted {
		id, err := c.stableNetworkID(ctx, want.Name)
		if err != nil {
			return nil, err
		}
		network := stableJSONObject{}
		for _, attachment := range old {
			var target string
			if err := json.Unmarshal(attachment["Target"], &target); err != nil {
				return nil, err
			}
			if target == id {
				network = maps.Clone(attachment)
				break
			}
		}
		aliases := append([]string(nil), want.AdditionalAliases...)
		if want.Alias != "" {
			aliases = append(aliases, want.Alias)
		}
		network.set("Target", id)
		network.set("Aliases", normalizedAliases(aliases))
		networks = append(networks, network)
	}
	return networks, nil
}

func stableNetworksEqual(old json.RawMessage, wanted []stableJSONObject) (bool, error) {
	var previous []stableJSONObject
	if len(old) > 0 {
		if err := json.Unmarshal(old, &previous); err != nil {
			return false, err
		}
	}
	normalize := func(networks []stableJSONObject) (map[string]map[string]any, error) {
		result := make(map[string]map[string]any, len(networks))
		for _, network := range networks {
			var target string
			if err := json.Unmarshal(network["Target"], &target); err != nil {
				return nil, err
			}
			if target == "" {
				return nil, fmt.Errorf("missing stable network target")
			}
			var aliases []string
			if raw := network["Aliases"]; len(raw) > 0 {
				if err := json.Unmarshal(raw, &aliases); err != nil {
					return nil, err
				}
			}
			network = maps.Clone(network)
			network.set("Aliases", normalizedAliases(aliases))
			raw, err := json.Marshal(network)
			if err != nil {
				return nil, err
			}
			value, err := stableSpecValue(raw)
			if err != nil {
				return nil, err
			}
			if _, exists := result[target]; exists {
				return nil, fmt.Errorf("duplicate stable network target")
			}
			result[target] = value
		}
		return result, nil
	}
	a, err := normalize(previous)
	if err != nil {
		return false, err
	}
	b, err := normalize(wanted)
	if err != nil {
		return false, err
	}
	return reflect.DeepEqual(a, b), nil
}

// ReconcileStableServiceNetworks updates aliases without replacing the image,
// healthcheck or other settings. The complete spec preserves attachment options
// and makes alias changes atomic. Identical targets and aliases are a no-op.
func (c *Client) ReconcileStableServiceNetworks(ctx context.Context, opts ServiceCreateOpts) error {
	if opts.Name == "" {
		return fmt.Errorf("stable service requires an explicit name")
	}
	snapshot, err := c.SnapshotStableService(ctx, opts.Name)
	if err != nil {
		return err
	}
	if snapshot == nil {
		return fmt.Errorf("stable service %s missing", opts.Name)
	}
	spec, err := stableObject(snapshot)
	if err != nil {
		return err
	}
	task, err := stableObject(spec["TaskTemplate"])
	if err != nil {
		return err
	}
	networks, err := c.stableNetworkSpecs(ctx, task["Networks"], opts.Networks)
	if err != nil {
		return err
	}
	equal, err := stableNetworksEqual(task["Networks"], networks)
	if err != nil {
		return err
	}
	if equal {
		return nil
	}
	task.set("Networks", networks)
	spec.set("TaskTemplate", task)
	update, err := stableObject(spec["UpdateConfig"])
	if err != nil {
		return err
	}
	update.set("Order", "start-first")
	update.set("FailureAction", "rollback")
	spec.set("UpdateConfig", update)
	rollback, err := stableObject(spec["RollbackConfig"])
	if err != nil {
		return err
	}
	// Engine-initiated failure recovery must retire each failed slot before
	// replacing it. Cobalt's exact snapshot recovery separately drains the
	// prior controller and retains its captured normal update policy.
	rollback.set("Order", "stop-first")
	spec.set("RollbackConfig", rollback)
	raw, err := json.Marshal(spec)
	if err != nil {
		return err
	}
	return c.UpdateServiceSpec(ctx, opts.Name, raw)
}
