package docker

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

type stableJSONObject map[string]json.RawMessage

func stableObject(raw json.RawMessage) (stableJSONObject, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return stableJSONObject{}, nil
	}
	var object stableJSONObject
	if err := json.Unmarshal(raw, &object); err != nil {
		return nil, err
	}
	if object == nil {
		object = stableJSONObject{}
	}
	return object, nil
}

func (object stableJSONObject) set(key string, value any) {
	raw, _ := json.Marshal(value)
	object[key] = raw
}

// stableSettingsSpec updates only the settings owned by cobalt, retaining raw
// unknown fields at every level. Image defaults are loaded afresh so removing
// command, environment or health overrides actually restores the new image.
func (c *Client) stableSettingsSpec(ctx context.Context, opts ServiceCreateOpts, previous json.RawMessage) (json.RawMessage, error) {
	spec, err := stableObject(previous)
	if err != nil {
		return nil, err
	}
	if len(spec) == 0 {
		return nil, fmt.Errorf("missing stable service spec")
	}
	task, err := stableObject(spec["TaskTemplate"])
	if err != nil {
		return nil, err
	}
	container, err := stableObject(task["ContainerSpec"])
	if err != nil {
		return nil, err
	}
	imageRaw, err := c.output(ctx, "image", "inspect", "--format", "{{json .Config}}", opts.Image)
	if err != nil {
		return nil, err
	}
	image, err := stableObject(imageRaw)
	if err != nil {
		return nil, fmt.Errorf("inspect stable image defaults: %w", err)
	}
	if len(image) == 0 {
		return nil, fmt.Errorf("missing stable image configuration")
	}
	container.set("Image", opts.Image)
	// Docker's ContainerSpec.Command is the image entrypoint; Args is its CMD.
	// Keep exec-form arrays intact instead of converting them to shell commands.
	for _, pair := range [][2]string{{"Command", "Entrypoint"}, {"Args", "Cmd"}} {
		if raw, ok := image[pair[1]]; ok {
			container[pair[0]] = raw
		} else {
			delete(container, pair[0])
		}
	}
	if opts.Command != "" {
		container.set("Args", ShellSplit(opts.Command))
	}
	var imageEnv []string
	if raw := image["Env"]; len(raw) > 0 {
		if err := json.Unmarshal(raw, &imageEnv); err != nil {
			return nil, err
		}
	}
	env := map[string]string{}
	for _, entry := range imageEnv {
		if key, value, ok := strings.Cut(entry, "="); ok {
			env[key] = value
		}
	}
	for key, value := range opts.EnvVars {
		env[key] = value
	}
	envList := make([]string, 0, len(env))
	for _, key := range sortedKeys(env) {
		envList = append(envList, key+"="+env[key])
	}
	container.set("Env", envList)
	for _, target := range []stableJSONObject{spec, container} {
		labels, err := stableObject(target["Labels"])
		if err != nil {
			return nil, err
		}
		for _, entry := range serviceLabels(opts.ProjectID, opts.ProjectName, opts.ServiceName, opts.DeploymentNumber) {
			key, value, _ := strings.Cut(entry, "=")
			labels.set(key, value)
		}
		target.set("Labels", labels)
	}
	grace := opts.StopGracePeriod
	if grace == 0 {
		grace = 10 * time.Second
	}
	container.set("StopGracePeriod", int64(grace))
	if opts.HealthCommand == "" {
		if raw, ok := image["Healthcheck"]; ok {
			container["Healthcheck"] = raw
		} else {
			delete(container, "Healthcheck")
		}
	} else {
		start := opts.HealthStartPeriod
		if start == 0 {
			start = 5 * time.Minute
		}
		interval := opts.HealthInterval
		if interval == 0 {
			interval = 3 * time.Second
		}
		health, err := stableObject(image["Healthcheck"])
		if err != nil {
			return nil, fmt.Errorf("inspect image healthcheck defaults: %w", err)
		}
		health.set("Test", []string{"CMD-SHELL", opts.HealthCommand})
		health.set("StartPeriod", int64(start))
		health.set("StartInterval", int64(interval))
		container.set("Healthcheck", health)
	}
	hosts := make([]string, 0)
	for _, host := range hostParams(opts.ExtraParams) {
		name, value, ok := strings.Cut(host, ":")
		if !ok {
			return nil, fmt.Errorf("invalid stable service host entry")
		}
		hosts = append(hosts, value+" "+name)
	}
	container.set("Hosts", hosts)
	var oldMounts []json.RawMessage
	if raw := container["Mounts"]; len(raw) > 0 {
		if err := json.Unmarshal(raw, &oldMounts); err != nil {
			return nil, err
		}
	}
	mounts := make([]json.RawMessage, 0, len(opts.Volumes))
	for _, wanted := range opts.Volumes {
		var preserved json.RawMessage
		for _, raw := range oldMounts {
			var old stableServiceMount
			if err := json.Unmarshal(raw, &old); err != nil {
				return nil, err
			}
			if old.Type == "volume" && old.Source == wanted.VolumeName && old.Target == wanted.DestinationPath && !old.ReadOnly {
				preserved = raw
				break
			}
		}
		if preserved == nil {
			preserved, _ = json.Marshal(stableServiceMount{Type: "volume", Source: wanted.VolumeName, Target: wanted.DestinationPath})
		}
		mounts = append(mounts, preserved)
	}
	container.set("Mounts", mounts)
	networks, err := c.stableNetworkSpecs(ctx, task["Networks"], opts.Networks)
	if err != nil {
		return nil, err
	}
	task.set("Networks", networks)
	task.set("ContainerSpec", container)
	spec.set("TaskTemplate", task)
	mode, err := stableObject(spec["Mode"])
	if err != nil {
		return nil, err
	}
	replicated, err := stableObject(mode["Replicated"])
	if err != nil {
		return nil, err
	}
	replicas := opts.Replicas
	if replicas == 0 {
		replicas = 1
	}
	replicated.set("Replicas", replicas)
	mode.set("Replicated", replicated)
	spec.set("Mode", mode)
	update, err := stableObject(spec["UpdateConfig"])
	if err != nil {
		return nil, err
	}
	update.set("Order", "start-first")
	update.set("Parallelism", 1)
	update.set("FailureAction", "rollback")
	update.set("Monitor", int64(90*time.Second))
	spec.set("UpdateConfig", update)
	rollback, err := stableObject(spec["RollbackConfig"])
	if err != nil {
		return nil, err
	}
	// Engine-initiated failure recovery must retire each failed slot before
	// replacing it. Cobalt's exact snapshot recovery separately drains the
	// prior controller and retains its captured normal update policy.
	rollback.set("Order", "stop-first")
	spec.set("RollbackConfig", rollback)
	return json.Marshal(spec)
}
