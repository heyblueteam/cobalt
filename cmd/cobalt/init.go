package main

import (
	"context"
	_ "embed"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/heyblueteam/cobalt/internal/cliconfig"
	"github.com/heyblueteam/cobalt/internal/output"
	"github.com/heyblueteam/cobalt/internal/ssh"
	"github.com/spf13/cobra"
)

//go:embed assets/init-docker-compose.yml
var initComposeTemplate string

//go:embed assets/init-Caddyfile-auto-https
var initCaddyfileAutoHTTPS string

//go:embed assets/init-Caddyfile-internal
var initCaddyfileInternal string

// caddyfileFor picks the Caddyfile shape that matches the public host:
// auto-HTTPS via Let's Encrypt for a real domain, tls-internal (self-signed)
// for an IP / localhost where Let's Encrypt can't issue.
func caddyfileFor(publicHost string) string {
	if isIPOrLocalhost(publicHost) {
		return initCaddyfileInternal
	}
	return initCaddyfileAutoHTTPS
}

func isIPOrLocalhost(host string) bool {
	if host == "" || host == "localhost" {
		return true
	}
	return net.ParseIP(host) != nil
}

func newInitCmd() *cobra.Command {
	var (
		composeFile   string
		publicHost    string
		cobaltVersion string
		dataDir       string
		keyPath       string
		keyPassphrase string
		password      string
		localImage    string
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

			if localImage != "" {
				fmt.Fprintf(output.Stderr, "[3c/8] Uploading local image %s...\n", localImage)
				if err := uploadLocalImage(ctx, conn, localImage); err != nil {
					return fmt.Errorf("upload local image: %w", err)
				}
				fmt.Fprintf(output.Stderr, "[3c/8] Image loaded on remote.\n")
			}

			fmt.Fprintf(output.Stderr, "[4/8] Creating /opt/cobalt directory...\n")
			if r := conn.Run(ctx, "mkdir -p /opt/cobalt"); r.Err != nil {
				return fmt.Errorf("create /opt/cobalt: %w", r.Err)
			}

			composePath := "/opt/cobalt/docker-compose.yml"
			caddyfilePath := "/opt/cobalt/Caddyfile"
			envPath := "/opt/cobalt/.env"

			// .env is always written so substitutions resolve even when a
			// custom compose is supplied. Variables a custom compose doesn't
			// reference are harmless.
			image := fmt.Sprintf("ghcr.io/heyblueteam/cobalt:%s", cobaltVersion)
			if localImage != "" {
				image = localImage
			}
			envContent := fmt.Sprintf("COBALT_IMAGE=%s\nCOBALT_PUBLIC_HOST=%s\nCOBALT_DATA_DIR=%s\n",
				image, publicHost, dataDir)

			if composeFile != "" {
				fmt.Fprintf(output.Stderr, "[5/8] Uploading custom compose file + .env: %s\n", composeFile)
				if err := conn.ScpTo(composeFile, composePath); err != nil {
					return fmt.Errorf("upload compose file: %w", err)
				}
				if err := writeRemoteFile(conn, envPath, envContent); err != nil {
					return fmt.Errorf("write .env: %w", err)
				}
			} else {
				caddyfile := caddyfileFor(publicHost)
				tlsKind := "auto-HTTPS"
				if caddyfile == initCaddyfileInternal {
					tlsKind = "self-signed (tls internal)"
				}
				fmt.Fprintf(output.Stderr, "[5/8] Writing docker-compose.yml, Caddyfile (%s), .env...\n", tlsKind)
				if err := writeRemoteFile(conn, composePath, initComposeTemplate); err != nil {
					return fmt.Errorf("write compose file: %w", err)
				}
				if err := writeRemoteFile(conn, caddyfilePath, caddyfile); err != nil {
					return fmt.Errorf("write Caddyfile: %w", err)
				}
				if err := writeRemoteFile(conn, envPath, envContent); err != nil {
					return fmt.Errorf("write .env: %w", err)
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
			if err := waitForHealthy(ctx, daemonURL, 120*time.Second); err != nil {
				return fmt.Errorf("daemon not healthy: %w", err)
			}

			fmt.Fprintf(output.Stderr, "[8/8] Reading bootstrap API key...\n")
			apiKey, err := readBootstrapKey(ctx, conn)
			if err != nil {
				return fmt.Errorf("read bootstrap key: %w", err)
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
	cmd.Flags().StringVar(&localImage, "local-image", "", "upload a local docker image (docker save piped to ssh docker load) and use it instead of pulling --version from the registry")

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

// uploadLocalImage runs `docker save <ref>` locally and pipes the tar stream
// over SSH into `docker load` on the remote. The image must already be
// present in the local docker daemon. Used by --local-image to side-load a
// freshly-built image without going through a registry.
func uploadLocalImage(ctx context.Context, conn *ssh.Conn, ref string) error {
	save := exec.CommandContext(ctx, "docker", "save", ref)
	save.Stderr = output.Stderr
	stdout, err := save.StdoutPipe()
	if err != nil {
		return fmt.Errorf("docker save stdout pipe: %w", err)
	}
	if err := save.Start(); err != nil {
		return fmt.Errorf("docker save start: %w", err)
	}

	pipeErr := conn.Pipe(ctx, "docker load", stdout, output.Stderr, output.Stderr)
	waitErr := save.Wait()

	if pipeErr != nil {
		return fmt.Errorf("remote docker load: %w", pipeErr)
	}
	if waitErr != nil {
		return fmt.Errorf("local docker save: %w", waitErr)
	}
	return nil
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

// readBootstrapKey reads the daemon's first-boot bootstrap API key from
// inside the cobalt container. The daemon writes it to
// {dataDir}/bootstrap-api-key (mode 0600) the first time it starts against
// an empty apikeys table, then never recreates it.
//
// We exec into the container rather than reading the host volume mount
// directly so we don't have to know docker's volume layout on the host.
func readBootstrapKey(ctx context.Context, conn *ssh.Conn) (string, error) {
	const cmd = "cd /opt/cobalt && docker compose exec -T cobalt cat /cobalt/data/bootstrap-api-key"
	r := conn.Run(ctx, cmd)
	if r.Err != nil {
		return "", fmt.Errorf("ssh exec: %w", r.Err)
	}
	if r.ExitCode != 0 {
		return "", fmt.Errorf("read bootstrap-api-key (exit %d): %s", r.ExitCode, strings.TrimSpace(r.Stderr))
	}
	key := strings.TrimSpace(r.Stdout)
	if key == "" {
		return "", fmt.Errorf("bootstrap-api-key file is empty")
	}
	return key, nil
}