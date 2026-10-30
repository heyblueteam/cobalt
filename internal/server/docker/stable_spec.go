package docker

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// StableServiceUpdateError reports the status of a rejected Docker API update.
// It intentionally omits request bodies, which can contain environment secrets.
type StableServiceUpdateError struct{ StatusCode int }

func (e *StableServiceUpdateError) Error() string {
	return fmt.Sprintf("Docker service spec update: HTTP %d", e.StatusCode)
}

// UpdateServiceSpec restores a complete captured spec through the Docker CLI's
// configured authenticated transport. An optimistic version check prevents an
// update based on a stale manager version. Only conflicts are retried.
func (c *Client) UpdateServiceSpec(ctx context.Context, name string, spec json.RawMessage) error {
	if _, err := stableSpecValue(spec); err != nil {
		return fmt.Errorf("invalid service spec: %w", err)
	}
	for attempt := 0; attempt < 3; attempt++ {
		err := c.updateServiceSpec(ctx, name, spec)
		var status *StableServiceUpdateError
		if !errors.As(err, &status) || status.StatusCode != http.StatusConflict || attempt == 2 {
			return err
		}
	}
	return nil
}

func (c *Client) updateServiceSpec(ctx context.Context, name string, spec json.RawMessage) error {
	out, err := c.output(ctx, "service", "inspect", "--format", "{{json .}}", name)
	if err != nil {
		return err
	}
	var service struct {
		ID      string
		Version struct{ Index *uint64 }
	}
	if err := json.Unmarshal(out, &service); err != nil {
		return fmt.Errorf("inspect service update version: %w", err)
	}
	if service.ID == "" || service.Version.Index == nil {
		return fmt.Errorf("service %s missing ID or version", name)
	}
	apiVersion, err := c.stableServiceAPIVersion(ctx)
	if err != nil {
		return err
	}
	endpoint := "http://docker/v" + apiVersion + "/services/" + url.PathEscape(service.ID) + "/update?version=" + strconv.FormatUint(*service.Version.Index, 10)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(spec))
	if err != nil {
		return err
	}
	req.Close = true
	req.Header.Set("Content-Type", "application/json")
	var request, response bytes.Buffer
	if err := req.Write(&request); err != nil {
		return err
	}
	if err := c.runner.Run(ctx, []string{"system", "dial-stdio"}, &request, &response, nil); err != nil {
		return fmt.Errorf("update service transport: %w", err)
	}
	result, err := http.ReadResponse(bufio.NewReader(&response), req)
	if err != nil {
		return fmt.Errorf("update service response: %w", err)
	}
	defer result.Body.Close()
	if _, err := io.Copy(io.Discard, io.LimitReader(result.Body, 1<<20)); err != nil {
		return fmt.Errorf("read service update response: %w", err)
	}
	if result.StatusCode < 200 || result.StatusCode >= 300 {
		return &StableServiceUpdateError{StatusCode: result.StatusCode}
	}
	return nil
}

// stableServiceAPIVersion uses the configured CLI transport and its negotiated
// client version, including DOCKER_API_VERSION when explicitly configured.
func (c *Client) stableServiceAPIVersion(ctx context.Context) (string, error) {
	out, err := c.output(ctx, "version", "--format", "{{json .}}")
	if err != nil {
		return "", err
	}
	var versions struct {
		Client struct{ APIVersion string }
		Server struct {
			APIVersion    string
			MinAPIVersion string
		}
	}
	if err := json.Unmarshal(out, &versions); err != nil {
		return "", fmt.Errorf("inspect Docker API versions: %w", err)
	}
	client, err := stableAPIMinor(versions.Client.APIVersion)
	if err != nil {
		return "", err
	}
	server, err := stableAPIMinor(versions.Server.APIVersion)
	if err != nil {
		return "", err
	}
	minimum, err := stableAPIMinor(versions.Server.MinAPIVersion)
	if err != nil {
		return "", err
	}
	selected := min(client, server)
	if selected < minimum || minimum > server {
		return "", fmt.Errorf("docker client and server have no supported API version")
	}
	return "1." + strconv.Itoa(selected), nil
}

func stableAPIMinor(version string) (int, error) {
	major, minor, ok := strings.Cut(version, ".")
	if !ok || major != "1" || minor == "" {
		return 0, fmt.Errorf("invalid Docker API version")
	}
	for _, digit := range minor {
		if digit < '0' || digit > '9' {
			return 0, fmt.Errorf("invalid Docker API version")
		}
	}
	n, err := strconv.Atoi(minor)
	if err != nil || n < 1 {
		return 0, fmt.Errorf("invalid Docker API version")
	}
	return n, nil
}
