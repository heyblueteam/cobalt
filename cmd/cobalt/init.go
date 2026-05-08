package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/heyblueteam/cobalt/internal/cliconfig"
	"github.com/heyblueteam/cobalt/internal/output"
	"github.com/heyblueteam/cobalt/internal/ssh"
	"github.com/spf13/cobra"
)

func newInitCmd() *cobra.Command {
	var (
		composeFile   string
		publicHost    string
		cobaltVersion string
		dataDir       string
		keyPath       string
		keyPassphrase string
		password      string
	)

		cmd := &cobra.Command{
		Use:   "init <user@host>",
		Short: "Initialize cobalt on a remote server",
		Long: `SSH into a target host, install Docker if needed, initialize Docker Swarm,
and start a cobalt stack using Docker Compose.

This command will:
  1. Connect to the target host via SSH
  2. Install Docker if not already installed
  3. Initialize Docker Swarm if not already initialized
  4. Create /opt/cobalt directory
  5. Upload docker-compose.yml and start the stack
  6. Wait for the cobalt daemon to become healthy
  7. Create an initial API key
  8. Save the server configuration locally

Examples:
  # Initialize on a server using default latest tag
  cobalt init root@server.blue.cc

  # Use a specific version and public hostname
  cobalt init root@server.blue.cc --version v1.0.0 --public-host cobalt.blue.cc

  # Use a custom compose file for air-gapped deployments
  cobalt init user@192.168.1.100 --compose-file ./my-compose.yml

  # Use password authentication (not recommended, use --key or SSH agent instead)
  cobalt init root@server.blue.cc --password mypassword
`,
		Args: cobra.ExactArgs(1),
		RunE: runE(func(cmd *cobra.Command, args []string) error {
			target := args[0]
			user, host := ssh.ParseSSHURL(target)

			if publicHost == "" {
				publicHost = host
			}

		ctx, cancel := context.WithTimeout(cmd.Context(), 10*time.Minute)
		defer cancel()

			fmt.Fprintf(output.Stderr, "[1/8] Connecting to %s...\n", target)

			// Auth precedence: explicit flags first, then ambient SSH agent,
			// then interactive prompt. Without this order an SSH_AUTH_SOCK in
			// the environment silently overrides --password, which has bitten us.
			var auth ssh.AuthMethod
			switch {
			case keyPath != "":
				auth = ssh.PublicKeyAuth{KeyPath: keyPath, Passphrase: keyPassphrase}
			case password != "":
				auth = ssh.PasswordAuth{Password: password}
			default:
				if socket := ssh.DefaultAgentSocket(); socket != "" {
					if conn, err := net.Dial("unix", socket); err == nil {
						conn.Close()
						auth = ssh.AgentAuth{Socket: socket}
					}
				}
			}

			if auth == nil {
				p := ssh.AskPassword("SSH password")
				auth = ssh.PasswordAuth{Password: p}
			}

			if user == "" {
				user = "root"
			}

			client := ssh.NewClient(user, host, auth)
			conn, err := client.Connect()
			if err != nil {
				return fmt.Errorf("SSH connection failed: %w", err)
			}
			defer conn.Close()

			fmt.Fprintf(output.Stderr, "[2/8] Connected. Checking Docker installation...\n")

			dockerCheck := conn.Run(ctx, "docker --version")
			if dockerCheck.Err != nil || dockerCheck.ExitCode != 0 {
				fmt.Fprintf(output.Stderr, "[2b/8] Docker not found. Installing Docker...\n")
				installDocker := conn.Run(ctx, "curl -fsSL https://get.docker.com | sh")
				if installDocker.ExitCode != 0 {
					return fmt.Errorf("docker installation failed: %s", installDocker.Stderr)
				}
				fmt.Fprintf(output.Stderr, "[2b/8] Docker installed.\n")
			} else {
				fmt.Fprintf(output.Stderr, "[2/8] Docker found: %s", strings.TrimSpace(dockerCheck.Stdout))
			}

			fmt.Fprintf(output.Stderr, "[3/8] Checking Docker Swarm...\n")
			swarmResult := conn.Run(ctx, "docker info --format '{{.Swarm.LocalNodeState}}'")
			swarmState := strings.TrimSpace(swarmResult.Stdout)
			if swarmState != "active" {
				fmt.Fprintf(output.Stderr, "[3b/8] Docker Swarm not initialized. Initializing...\n")
				initSwarm := conn.Run(ctx, "docker swarm init")
				if initSwarm.ExitCode != 0 {
					return fmt.Errorf("swarm init failed: %s", initSwarm.Stderr)
				}
				fmt.Fprintf(output.Stderr, "[3b/8] Docker Swarm initialized.\n")
			} else {
				fmt.Fprintf(output.Stderr, "[3/8] Docker Swarm active.\n")
			}

			fmt.Fprintf(output.Stderr, "[4/8] Creating /opt/cobalt directory...\n")
			if r := conn.Run(ctx, "mkdir -p /opt/cobalt"); r.Err != nil {
				return fmt.Errorf("create /opt/cobalt: %w", r.Err)
			}

			composePath := "/opt/cobalt/docker-compose.yml"
			if composeFile != "" {
				fmt.Fprintf(output.Stderr, "[5/8] Uploading custom compose file: %s\n", composeFile)
				if err := conn.ScpTo(composeFile, composePath); err != nil {
					return fmt.Errorf("upload compose file: %w", err)
				}
			} else {
				fmt.Fprintf(output.Stderr, "[5/8] Creating docker-compose.yml...\n")
				composeContent, err := defaultComposeYAML(cobaltVersion, publicHost, dataDir)
				if err != nil {
					return fmt.Errorf("generate compose file: %w", err)
				}
				if err := writeRemoteFile(conn, composePath, composeContent); err != nil {
					return fmt.Errorf("write compose file: %w", err)
				}
			}

			fmt.Fprintf(output.Stderr, "[6/8] Starting Docker Compose stack...\n")
			result := conn.Run(ctx, "cd /opt/cobalt && docker compose up -d")
			if result.Err != nil {
				return fmt.Errorf("docker compose up failed: %w", result.Err)
			}
			if result.ExitCode != 0 {
				return fmt.Errorf("docker compose up failed (exit %d): %s", result.ExitCode, result.Stderr)
			}

			fmt.Fprintf(output.Stderr, "[7/8] Waiting for cobalt to be healthy...\n")
			daemonURL := fmt.Sprintf("http://%s/healthz", host)
			if err := waitForHealthy(ctx, daemonURL, 60*time.Second); err != nil {
				return fmt.Errorf("daemon not healthy: %w", err)
			}

			fmt.Fprintf(output.Stderr, "[8/8] Creating API key...\n")
			apiKey, err := createAPIKey(ctx, daemonURL)
			if err != nil {
				return fmt.Errorf("create API key: %w", err)
			}

			cfg := &cliconfig.Config{
				Servers: map[string]cliconfig.Server{
					host: {
						Host:   host,
						APIKey: apiKey,
					},
				},
				DefaultServer: host,
			}

			cfgPath, err := cliconfig.DefaultPath()
			if err != nil {
				return fmt.Errorf("config path: %w", err)
			}
			if err := cliconfig.Save(cfgPath, cfg); err != nil {
				return fmt.Errorf("save config: %w", err)
			}

			output.PrintLines(
				fmt.Sprintf("✓ Cobalt initialized on %s", host),
				fmt.Sprintf("  API key saved to %s", cfgPath),
				"  Run 'cobalt servers' to verify connection",
			)

			return nil
		}),
	}

	cmd.Flags().StringVar(&composeFile, "compose-file", "", "path to custom docker-compose.yml")
	cmd.Flags().StringVar(&publicHost, "public-host", "", "public hostname for the daemon (defaults to SSH host)")
	cmd.Flags().StringVar(&cobaltVersion, "version", "latest", "cobalt image version to deploy")
	cmd.Flags().StringVar(&dataDir, "data-dir", "/cobalt/data", "data directory for cobalt")
	cmd.Flags().StringVar(&keyPath, "key", "", "path to SSH private key")
	cmd.Flags().StringVar(&keyPassphrase, "key-passphrase", "", "passphrase for SSH private key (if encrypted)")
	cmd.Flags().StringVar(&password, "password", "", "SSH password (use interactively or via SSH agent for better security)")

	return cmd
}

func writeRemoteFile(conn *ssh.Conn, path, content string) error {
	tmp, err := os.CreateTemp("", "cobalt-remote-*")
	if err != nil {
		return err
	}
	if _, err := tmp.WriteString(content); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	tmp.Close()
	defer os.Remove(tmp.Name())

	return conn.ScpTo(tmp.Name(), path)
}

func waitForHealthy(ctx context.Context, url string, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	client := &http.Client{Timeout: 5 * time.Second}
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			resp, err := client.Get(url)
			if err == nil {
				resp.Body.Close()
				if resp.StatusCode == http.StatusOK {
					return nil
				}
			}
		}
	}
}

func createAPIKey(ctx context.Context, baseURL string) (string, error) {
	reqBody := map[string]string{"name": "init-created-key"}
	body, err := json.Marshal(reqBody)
	if err != nil {
		return "", err
	}

	req, err := http.NewRequestWithContext(ctx, "POST", baseURL+"/api/apikeys", strings.NewReader(string(body)))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("create API key request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return "", fmt.Errorf("API key creation failed: status %d: %s", resp.StatusCode, string(bodyBytes))
	}

	var result struct {
		Key string `json:"key"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("parse API key response: %w", err)
	}

	return result.Key, nil
}

func defaultComposeYAML(version, publicHost, dataDir string) (string, error) {
	return fmt.Sprintf(`---
version: "3.9"

services:
  rqlite:
    image: ghcr.io/rqlite/rqlite:v8.31.0
    command: >
      rqlited
      -d /db
      -o /rqlite
      --join ""
    volumes:
      - rqlite-data:/db
    healthcheck:
      test: ["CMD", "rqlite", "nodes"]
      interval: 5s
      timeout: 3s
      retries: 5
    restart: unless-stopped

  cobalt:
    image: ghcr.io/heyblueteam/cobalt:%s
    depends_on:
      rqlite:
        condition: service_healthy
    volumes:
      - cobalt-data:%s
      - /var/run/docker.sock:/var/run/docker.sock
      - caddy-socket:/cobalt/caddy-socket:shared
    environment:
      COBALT_DATA_DIR: %s
      COBALT_PUBLIC_HOST: %s
    command: >
      server
      --addr :80
      --rqlite-url http://rqlite:4001
      --data-dir /cobalt/data
      --caddy-socket /cobalt/caddy-socket/caddy.sock
      --public-host %s
    restart: unless-stopped

  caddy:
    image: caddy:2.7.6
    volumes:
      - caddy-data:/data
      - caddy-config:/config
      - caddy-socket:/cobalt/caddy-socket:shared
    ports:
      - "80:80"
      - "443:443"
    command: >
      caddy run
      --config /etc/caddy/Caddyfile
      --adapter caddyfile
    restart: unless-stopped

networks:
  default:
    driver: bridge

volumes:
  cobalt-data:
  rqlite-data:
  caddy-data:
  caddy-config:
  caddy-socket:
`, version, dataDir, dataDir, publicHost, publicHost), nil
}