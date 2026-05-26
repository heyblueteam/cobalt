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
	"github.com/heyblueteam/cobalt/internal/client"
	"github.com/heyblueteam/cobalt/internal/output"
	"github.com/heyblueteam/cobalt/internal/ssh"
	"github.com/heyblueteam/cobalt/pkg/cobaltapi"
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
// for an IP / localhost or when the operator explicitly opts into insecure
// TLS for a dev install.
func caddyfileFor(publicHost string, insecureTLS bool) string {
	if insecureTLS || isIPOrLocalhost(publicHost) {
		return initCaddyfileInternal
	}
	return initCaddyfileAutoHTTPS
}

// caddyfileForInit returns the Caddyfile body to write to /opt/cobalt/Caddyfile
// during `cobalt init`, or "" when the operator opted out via --no-caddyfile.
//
// Why this isn't gated on --compose-file: the embedded compose template
// bind-mounts /opt/cobalt/Caddyfile, and operators who pass a custom compose
// almost always start from a near-identical variant that keeps the same
// mount (e.g. they only changed port bindings). Skipping the Caddyfile in
// that path produces a Rejected restart loop on cobalt_caddy with
// "bind source path does not exist". Writing it by default is harmless when
// the operator's compose doesn't mount it; --no-caddyfile is the explicit
// opt-out for operators who really do replace the Caddy setup wholesale.
func caddyfileForInit(noCaddyfile bool, publicHost string, insecureTLS bool) string {
	if noCaddyfile {
		return ""
	}
	return caddyfileFor(publicHost, insecureTLS)
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
		insecureTLS   bool
		noGitHubApp   bool
		noCaddyfile   bool
	)

	cmd := &cobra.Command{
		Use:   "init <user@host>",
		Short: "Initialize cobalt on a remote server",
		Long: `SSH into a target host, install Docker if needed, initialize Docker Swarm,
and start a cobalt stack using Docker Compose.

This command will:
  1. Connect to the target host via SSH
  2. Detect the environment and confirm the public hostname
  3. Optionally kick off the GitHub App registration flow
  4. Install Docker if not already installed
  5. Initialize Docker Swarm if not already initialized
  6. Create /opt/cobalt and deploy the cobalt stack
  7. Wait for the cobalt daemon to become healthy
  8. Save the bootstrap API key locally

Examples:
  # Initialize on a server (interactive)
  cobalt init root@server.blue.cc

  # Use a specific version and public hostname
  cobalt init root@server.blue.cc --version v1.0.0 --public-host cobalt.blue.cc

  # Local dev install against an IP (self-signed Caddy cert; no GitHub App webhooks)
  cobalt init root@192.168.1.100 --insecure-tls

  # Unattended install (CI). --yes skips all prompts and the GitHub App handoff.
  cobalt init root@server.blue.cc --yes --public-host cobalt.blue.cc

  # Use a custom compose file for air-gapped deployments
  cobalt init user@192.168.1.100 --compose-file ./my-compose.yml

  # Custom compose + custom Caddy setup (skip writing /opt/cobalt/Caddyfile)
  cobalt init user@192.168.1.100 --compose-file ./my-compose.yml --no-caddyfile
`,
		Args: cobra.ExactArgs(1),
		RunE: runE(func(cmd *cobra.Command, args []string) error {
			// --no-caddyfile only makes sense alongside --compose-file: the
			// embedded compose template bind-mounts /opt/cobalt/Caddyfile, so
			// suppressing the write on the default path reproduces the exact
			// crash-loop this command fix was meant to prevent.
			if noCaddyfile && composeFile == "" {
				return fmt.Errorf("--no-caddyfile requires --compose-file (the embedded compose template bind-mounts /opt/cobalt/Caddyfile)")
			}

			target := args[0]
			user, host := ssh.ParseSSHURL(target)
			assumeYes := cmd.Flag("yes").Value.String() == "true"

			ctx, cancel := context.WithTimeout(cmd.Context(), 10*time.Minute)
			defer cancel()

			// 🔌 SSH connect.
			conn, err := dialSSH(target, user, host, keyPath, keyPassphrase, password)
			if err != nil {
				return err
			}
			defer conn.Close()

			// 🔍 Detect environment and show what we're about to do.
			env, err := detectEnv(ctx, conn, host, publicHost, insecureTLS, composeFile, noCaddyfile)
			if err != nil {
				return err
			}

			// 🌐 Confirm public hostname (unless --public-host was supplied).
			if publicHost == "" {
				answer, err := output.Input(output.IconPublicHost,
					"Public hostname for the cobalt dashboard?",
					env.proposedPublicHost, assumeYes)
				if err != nil {
					return err
				}
				publicHost = answer
			}

			// Re-validate TLS now that the hostname might have changed.
			// GitHub App webhook callbacks need an HTTPS URL with a
			// publicly-trusted certificate; an IP literal can't get one
			// from Let's Encrypt. Refuse by default and force the operator
			// to opt into the self-signed (tls internal) Caddyfile via
			// --insecure-tls. Custom-compose installs are exempt — the
			// operator presumably knows what they're configuring.
			if !insecureTLS && composeFile == "" && isIPOrLocalhost(publicHost) {
				return fmt.Errorf(
					"public host %q is not a domain name; GitHub App webhooks require a real hostname with public TLS.\n"+
						"  • Use --public-host <domain> if your SSH host is internal,\n"+
						"  • or pass --insecure-tls to install with a self-signed cert (dev only)",
					publicHost,
				)
			}

			// 🦊 Ask whether to register the GitHub App after the daemon is up.
			//
			// Skip the prompt + handoff entirely when:
			//   - --no-github-app was passed, or
			//   - --yes was passed (CI/unattended; opening a browser is
			//     useless and printing the manifest URL into CI logs is
			//     noise — operators wanting it should drop --yes), or
			//   - --insecure-tls was passed (the manifest URL is
			//     served with a self-signed cert, so our own HTTP
			//     client can't reach it without InsecureSkipVerify, and
			//     GitHub webhooks can't reach back either — nothing to
			//     gain by attempting).
			setupGitHubApp := false
			ghOrg := ""
			switch {
			case noGitHubApp:
				// silent skip — operator opted out explicitly
			case assumeYes:
				// silent skip under --yes (matches CI default)
			case insecureTLS:
				fmt.Fprintf(output.Stderr,
					"%s GitHub App skipped (--insecure-tls; webhooks need a publicly-trusted cert).\n",
					output.IconGitHub)
			default:
				yes, err := output.Confirm(output.IconGitHub,
					"Set up GitHub App now for repo deploys?",
					true, assumeYes)
				if err != nil {
					return err
				}
				setupGitHubApp = yes
				if setupGitHubApp {
					ghOrg, err = output.InputOptional(output.IconGitHub,
						"GitHub organization for the App?",
						"leave empty for personal account", assumeYes)
					if err != nil {
						return err
					}
				}
			}

			// 🐳 Install Docker if needed.
			step := output.StartStep(output.IconDocker, "Installing Docker")
			if env.dockerInstalled {
				step.Skip("Docker already installed")
			} else {
				r := conn.Run(ctx, "curl -fsSL https://get.docker.com | sh")
				if r.ExitCode != 0 {
					step.Fail(strings.TrimSpace(r.Stderr))
					return fmt.Errorf("docker installation failed: %s", r.Stderr)
				}
				step.OK()
			}

			// 🐝 Initialize swarm if needed.
			step = output.StartStep(output.IconSwarm, "Initializing Docker Swarm")
			if env.swarmActive {
				step.Skip("Swarm already initialized")
			} else {
				r := conn.Run(ctx, "docker swarm init")
				if r.ExitCode != 0 {
					step.Fail(strings.TrimSpace(r.Stderr))
					return fmt.Errorf("swarm init failed: %s", r.Stderr)
				}
				step.OK()
			}

			// 🕸 cobalt-main is the shared overlay network every project service
			// gets attached to. Caddy joins it via the compose stack so it can
			// resolve service hostnames; deploy hooks run one-shot containers
			// here too. Must exist before `docker compose up` so the compose
			// file's `external: true` reference resolves.
			step = output.StartStep(output.IconNetwork, "Ensuring cobalt-main overlay network")
			netCheck := conn.Run(ctx,
				"docker network inspect cobalt-main >/dev/null 2>&1 && echo present || "+
					"docker network create --driver overlay --attachable cobalt-main")
			if netCheck.Err != nil {
				step.Fail(netCheck.Err.Error())
				return fmt.Errorf("ensure cobalt-main network: %w", netCheck.Err)
			}
			step.OK()

			// 🔑 cobalt_encryption_key is a Docker Swarm secret holding the
			// AES-256 key the daemon uses to encrypt env values at rest.
			// The bytes live in the swarm Raft log (encrypted by Docker)
			// and are mounted into the daemon as tmpfs at
			// /run/secrets/cobalt_encryption_key. Backing up the cobalt-data
			// volume yields ciphertext only; the key itself isn't on disk
			// anywhere outside the Raft log.
			//
			// Idempotent: reuse the existing secret on subsequent inits.
			// Rotation is a separate operator flow.
			step = output.StartStep(output.IconSecret, "Generating encryption key (swarm secret)")
			secretCheck := conn.Run(
				ctx,
				"if docker secret inspect cobalt_encryption_key >/dev/null 2>&1; then "+
					"  echo present; "+
					"else "+
					"  head -c 32 /dev/urandom | docker secret create cobalt_encryption_key - >/dev/null && "+
					"  echo generated; "+
					"fi",
			)
			if secretCheck.Err != nil {
				step.Fail(secretCheck.Err.Error())
				return fmt.Errorf("ensure cobalt_encryption_key secret: %w", secretCheck.Err)
			}
			if strings.Contains(secretCheck.Stdout, "present") {
				step.Skip("Key already present in Raft log")
			} else {
				step.OK()
			}

			if localImage != "" {
				step = output.StartStep(output.IconDocker, fmt.Sprintf("Uploading local image %s", localImage))
				if err := uploadLocalImage(ctx, conn, localImage); err != nil {
					step.Fail(err.Error())
					return fmt.Errorf("upload local image: %w", err)
				}
				step.OK()
			}

			// 📝 Write compose / Caddyfile / .env.
			step = output.StartStep(output.IconWriting, "Writing /opt/cobalt config")
			if r := conn.Run(ctx, "mkdir -p /opt/cobalt"); r.Err != nil {
				step.Fail(r.Err.Error())
				return fmt.Errorf("create /opt/cobalt: %w", r.Err)
			}

			composePath := "/opt/cobalt/docker-compose.yml"
			caddyfilePath := "/opt/cobalt/Caddyfile"
			envPath := "/opt/cobalt/.env"

			image := fmt.Sprintf("ghcr.io/heyblueteam/cobalt:%s", cobaltVersion)
			if localImage != "" {
				image = localImage
			}
			envContent := fmt.Sprintf("COBALT_IMAGE=%s\nCOBALT_PUBLIC_HOST=%s\nCOBALT_DATA_DIR=%s\n",
				image, publicHost, dataDir)

			// Compose: operator's file via scp, otherwise the embedded template.
			if composeFile != "" {
				if err := conn.ScpTo(composeFile, composePath); err != nil {
					step.Fail(err.Error())
					return fmt.Errorf("upload compose file: %w", err)
				}
			} else {
				if err := writeRemoteFile(conn, composePath, initComposeTemplate); err != nil {
					step.Fail(err.Error())
					return fmt.Errorf("write compose file: %w", err)
				}
			}

			// Caddyfile written by default — including the --compose-file path,
			// because the embedded compose template (and most operator variants
			// of it) bind-mount /opt/cobalt/Caddyfile. Opt out with --no-caddyfile.
			if caddyfile := caddyfileForInit(noCaddyfile, publicHost, insecureTLS); caddyfile != "" {
				if err := writeRemoteFile(conn, caddyfilePath, caddyfile); err != nil {
					step.Fail(err.Error())
					return fmt.Errorf("write Caddyfile: %w", err)
				}
			}

			// .env always written so substitutions in either the embedded or
			// operator-supplied compose resolve. Vars the compose doesn't use
			// are harmless.
			if err := writeRemoteFile(conn, envPath, envContent); err != nil {
				step.Fail(err.Error())
				return fmt.Errorf("write .env: %w", err)
			}
			step.OK()

			// 🚀 Deploy the cobalt swarm stack.
			step = output.StartStep(output.IconDeploy, "Deploying cobalt stack")
			// `docker stack deploy` doesn't auto-load .env files the way
			// `docker compose up` does, so source the file into the calling
			// shell first. set -a / set +a auto-exports every assignment
			// without us listing the var names.
			result := conn.Run(
				ctx,
				"set -a && . /opt/cobalt/.env && set +a && "+
					"docker stack deploy --with-registry-auth -c /opt/cobalt/docker-compose.yml cobalt",
			)
			if result.Err != nil {
				step.Fail(result.Err.Error())
				return fmt.Errorf("docker stack deploy failed: %w", result.Err)
			}
			if result.ExitCode != 0 {
				step.Fail(strings.TrimSpace(result.Stderr))
				return fmt.Errorf("docker stack deploy failed (exit %d): %s", result.ExitCode, result.Stderr)
			}
			step.OK()

			// 💚 Wait for the daemon to be healthy.
			//
			// Auto-HTTPS Caddyfile only declares the site on
			// {$COBALT_PUBLIC_HOST}, so a request to http://<sshHost>/healthz
			// (by IP / different host) doesn't match any block and Caddy
			// returns its default response — the original bug here was
			// hitting the SSH host on plain HTTP. We probe the public host
			// directly: HTTPS for auto-HTTPS installs (also exercises the
			// Let's Encrypt cert provisioning), HTTP for --insecure-tls
			// (self-signed installs use the internal Caddyfile's :80
			// catch-all reverse_proxy). Custom-compose installs default to
			// HTTP; operators bringing their own compose can override
			// expectations there.
			step = output.StartStep(output.IconHealth, "Waiting for daemon to become healthy")
			scheme := "https"
			if insecureTLS || composeFile != "" {
				scheme = "http"
			}
			daemonURL := fmt.Sprintf("%s://%s/healthz", scheme, publicHost)
			if err := waitForHealthy(ctx, daemonURL, 180*time.Second); err != nil {
				step.Fail(err.Error())
				return fmt.Errorf("daemon not healthy: %w", err)
			}
			step.OK()

			// 🎟 Read the bootstrap API key from the daemon's data volume.
			step = output.StartStep(output.IconAPIKey, "Reading bootstrap API key")
			apiKey, err := readBootstrapKey(ctx, conn)
			if err != nil {
				step.Fail(err.Error())
				return fmt.Errorf("read bootstrap key: %w", err)
			}
			step.OK()

			// For --insecure-tls installs the daemon serves a cert
			// signed by Caddy's local CA, which isn't in the system
			// trust store. Pull that root cert now and stash it in
			// cliconfig.Server.CACertPEM so the CLI can verify the
			// chain without falling back to plain HTTP or
			// InsecureSkipVerify. Public-CA installs skip this — their
			// chain validates against the system pool.
			caCertPEM := ""
			if insecureTLS {
				step = output.StartStep(output.IconTLS, "Trusting daemon's local CA")
				pem, err := readCaddyRootCert(ctx, conn)
				if err != nil {
					step.Fail(err.Error())
					return fmt.Errorf("read caddy root cert: %w", err)
				}
				caCertPEM = pem
				step.OK()
			}

			// 💾 Save config locally.
			//
			// The saved Host is the *public* hostname, not the SSH host.
			// SSH was just used to drive the install; from now on the CLI
			// talks to the daemon over HTTPS, and the daemon's TLS cert
			// is for the public host. Using the SSH host (often an IP)
			// gives a TLS handshake error from cert/SNI mismatch.
			step = output.StartStep(output.IconSave, "Saving config to ~/.cobalt/config.toml")
			cfg := &cliconfig.Config{
				Servers: map[string]cliconfig.Server{
					publicHost: {
						Host:      publicHost,
						APIKey:    apiKey,
						CACertPEM: caCertPEM,
					},
				},
				DefaultServer: publicHost,
			}
			cfgPath, err := cliconfig.DefaultPath()
			if err != nil {
				step.Fail(err.Error())
				return fmt.Errorf("config path: %w", err)
			}
			if err := cliconfig.Save(cfgPath, cfg); err != nil {
				step.Fail(err.Error())
				return fmt.Errorf("save config: %w", err)
			}
			step.OK()

			// Now that the key is safely persisted locally, scrub it from
			// the daemon's data volume. The daemon won't recreate the file
			// (apikeys is no longer empty), so this turns the bootstrap
			// key into a true single-use credential. Best-effort —
			// failures here aren't fatal; the user already has the key.
			if err := removeBootstrapKey(ctx, conn); err != nil {
				fmt.Fprintf(output.Stderr, "  warning: could not scrub bootstrap key on remote: %v\n", err)
			}

			// 🦊 GitHub App handoff (only if the operator opted in).
			if setupGitHubApp {
				if err := openGitHubAppFlow(ctx, cfg.Servers[host], ghOrg); err != nil {
					fmt.Fprintf(output.Stderr, "  warning: GitHub App setup failed: %v\n", err)
					fmt.Fprintf(output.Stderr, "  Run `cobalt github apps add` later to retry.\n")
				}
			}

			// 🎉 Done.
			fmt.Fprintf(output.Stderr, "\n%s Cobalt initialized at https://%s\n", output.IconDone, publicHost)
			fmt.Fprintf(output.Stderr, "   Run `cobalt servers` to verify, or `cobalt deploy` to ship something.\n")
			return nil
		}),
	}

	cmd.Flags().StringVar(&composeFile, "compose-file", "", "path to custom docker-compose.yml")
	cmd.Flags().StringVar(&publicHost, "public-host", "", "public hostname for the daemon (defaults to SSH host; suppresses the 🌐 prompt)")
	cmd.Flags().StringVar(&cobaltVersion, "version", "latest", "cobalt image version to deploy")
	cmd.Flags().StringVar(&dataDir, "data-dir", "/cobalt/data", "data directory for cobalt")
	cmd.Flags().StringVar(&keyPath, "key", "", "path to SSH private key")
	cmd.Flags().StringVar(&keyPassphrase, "key-passphrase", "", "passphrase for SSH private key (if encrypted)")
	cmd.Flags().StringVar(&password, "password", "", "SSH password (use interactively or via SSH agent for better security)")
	cmd.Flags().StringVar(&localImage, "local-image", "", "upload a local docker image (docker save piped to ssh docker load) and use it instead of pulling --version from the registry")
	cmd.Flags().BoolVar(&insecureTLS, "insecure-tls", false, "allow installing against an IP / localhost with a self-signed Caddy cert (dev only; GitHub App webhooks won't work)")
	cmd.Flags().BoolVar(&noGitHubApp, "no-github-app", false, "skip the GitHub App registration prompt and browser handoff")
	cmd.Flags().BoolVar(&noCaddyfile, "no-caddyfile", false, "skip writing /opt/cobalt/Caddyfile (only useful with --compose-file when the operator's compose doesn't mount it)")

	return cmd
}

// detectEnv probes the remote host for docker, swarm, and the
// proposed public hostname, then prints the 🔍 detection summary
// block. Returns the probed values so subsequent steps can short-
// circuit work that's already done.
type envState struct {
	dockerInstalled    bool
	swarmActive        bool
	proposedPublicHost string
}

func detectEnv(ctx context.Context, conn *ssh.Conn, sshHost, publicHostFlag string, insecureTLS bool, composeFile string, noCaddyfile bool) (envState, error) {
	step := output.StartStep(output.IconDetect, "Detecting environment")

	dockerCheck := conn.Run(ctx, "docker --version")
	dockerInstalled := dockerCheck.Err == nil && dockerCheck.ExitCode == 0

	swarmResult := conn.Run(ctx, "docker info --format '{{.Swarm.LocalNodeState}}'")
	swarmActive := strings.TrimSpace(swarmResult.Stdout) == "active"

	proposed := publicHostFlag
	if proposed == "" {
		proposed = sshHost
	}

	tlsLabel := "Let's Encrypt (auto-HTTPS)"
	if insecureTLS || isIPOrLocalhost(proposed) {
		tlsLabel = "self-signed (tls internal)"
	}
	// --no-caddyfile means we won't write /opt/cobalt/Caddyfile, so the
	// Caddyfile shape this label describes is moot — the operator owns
	// TLS termination via their own compose / config.
	if composeFile != "" && noCaddyfile {
		tlsLabel = "(custom compose, no Caddyfile)"
	}

	dockerLabel := "not installed, will install"
	if dockerInstalled {
		dockerLabel = "installed (" + strings.TrimSpace(dockerCheck.Stdout) + ")"
	}
	swarmLabel := "not initialized, will initialize"
	if swarmActive {
		swarmLabel = "active"
	}

	step.Detail(output.IconPublicHost, "Public hostname:   "+proposed)
	step.Detail(output.IconTLS, "TLS:               "+tlsLabel)
	step.Detail(output.IconDocker, "Docker:            "+dockerLabel)
	step.Detail(output.IconSwarm, "Swarm:             "+swarmLabel)
	fmt.Fprintln(output.Stderr)
	// Detection step has no checkmark — the indented detail block is
	// the result. Print a newline to close it cleanly.
	fmt.Fprintln(output.Stderr)

	return envState{
		dockerInstalled:    dockerInstalled,
		swarmActive:        swarmActive,
		proposedPublicHost: proposed,
	}, nil
}

// dialSSH wraps the auth selection and connect into a single 🔌 step.
// Auth precedence: explicit flags first, then ambient SSH agent, then
// interactive prompt. Without this order an SSH_AUTH_SOCK in the
// environment silently overrides --password, which has bitten us.
func dialSSH(target, user, host, keyPath, keyPassphrase, password string) (*ssh.Conn, error) {
	step := output.StartStep(output.IconSSH, "Connecting to "+target)

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
		// No flag, no agent — fall back to interactive prompt. We
		// can't ask through the step helper because the prompt needs
		// to print before the trailing status mark.
		step.Fail("no auth method (re-run with --key, --password, or an SSH agent)")
		return nil, fmt.Errorf("no SSH auth method available; pass --key <path>, --password, or start ssh-agent")
	}

	if user == "" {
		user = "root"
	}

	c := ssh.NewClient(user, host, auth)
	conn, err := c.Connect()
	if err != nil {
		step.Fail(err.Error())
		return nil, fmt.Errorf("SSH connection failed: %w", err)
	}
	step.OK()
	return conn, nil
}

// openGitHubAppFlow mints a pending-app URL via the daemon and opens
// it in the operator's browser. Best-effort — if the browser open
// fails, the URL is printed for manual copy-paste.
func openGitHubAppFlow(ctx context.Context, srv cliconfig.Server, org string) error {
	c := client.New(srv)
	resp, err := c.CreatePendingApp(ctx, cobaltapi.PendingAppCreateRequest{
		Organization: org,
	})
	if err != nil {
		return fmt.Errorf("create pending app: %w", err)
	}
	step := output.StartStep(output.IconBrowser, "Opening browser to register GitHub App")
	step.OK()
	fmt.Fprintf(output.Stderr, "   → %s\n", resp.URL)
	if err := client.OpenBrowser(resp.URL); err != nil {
		fmt.Fprintf(output.Stderr, "   could not open browser automatically; copy the URL above\n")
	}
	return nil
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

// daemonExec runs cmd inside the live cobalt daemon container. It
// resolves the swarm task's container ID by docker-ps filter on the
// stack-managed service label so we don't have to know the dynamic
// task suffix swarm appends.
//
// Used by readBootstrapKey / removeBootstrapKey now that the stack is
// deployed with `docker stack deploy` instead of `docker compose up`
// (compose-exec by service name doesn't apply).
func daemonExec(ctx context.Context, conn *ssh.Conn, cmd string) *ssh.Result {
	full := "id=$(docker ps --filter label=com.docker.swarm.service.name=cobalt_cobalt -q | head -1); " +
		"if [ -z \"$id\" ]; then echo 'cobalt_cobalt task not running' >&2; exit 1; fi; " +
		"docker exec \"$id\" sh -c " + shellSingleQuote(cmd)
	return conn.Run(ctx, full)
}

// shellSingleQuote wraps s in single quotes for a POSIX shell,
// escaping any embedded single quotes via the standard `'\”` trick.
// (Mirrors the helper of the same name in internal/ssh/ssh.go to keep
// init.go free of new internal-package imports for this one-liner.)
func shellSingleQuote(s string) string {
	out := make([]byte, 0, len(s)+2)
	out = append(out, '\'')
	for i := 0; i < len(s); i++ {
		if s[i] == '\'' {
			out = append(out, '\'', '\\', '\'', '\'')
			continue
		}
		out = append(out, s[i])
	}
	out = append(out, '\'')
	return string(out)
}

// readBootstrapKey reads the daemon's first-boot bootstrap API key
// from inside the live cobalt task. The daemon writes it to
// {dataDir}/bootstrap-api-key (mode 0600) the first time it starts
// against an empty apikeys table, then never recreates it.
func readBootstrapKey(ctx context.Context, conn *ssh.Conn) (string, error) {
	r := daemonExec(ctx, conn, "cat /cobalt/data/bootstrap-api-key")
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

// removeBootstrapKey deletes the bootstrap-api-key file from the
// daemon's data volume. Called after the local cliconfig has been
// saved so the key only ever lives in two places: in our local
// config, and (hashed) in the daemon's apikeys table.
func removeBootstrapKey(ctx context.Context, conn *ssh.Conn) error {
	r := daemonExec(ctx, conn, "rm -f /cobalt/data/bootstrap-api-key")
	if r.Err != nil {
		return fmt.Errorf("ssh exec: %w", r.Err)
	}
	if r.ExitCode != 0 {
		return fmt.Errorf("rm bootstrap-api-key (exit %d): %s", r.ExitCode, strings.TrimSpace(r.Stderr))
	}
	return nil
}

// readCaddyRootCert pulls Caddy's local-CA root certificate (the one
// it's signing the daemon's TLS leaf with, under `local_certs`) out
// of the cobalt_caddy task so we can pin it in cliconfig.Server.
//
// Caddy provisions the CA on first boot — by the time the daemon is
// healthy the file is on disk. We still retry briefly to absorb the
// rare case where caddy is up but hasn't quite written root.crt yet.
func readCaddyRootCert(ctx context.Context, conn *ssh.Conn) (string, error) {
	const path = "/data/caddy/pki/authorities/local/root.crt"
	const attempts = 10
	var last string
	for range attempts {
		r := caddyExec(ctx, conn, "cat "+path)
		if r.Err == nil && r.ExitCode == 0 && strings.Contains(r.Stdout, "BEGIN CERTIFICATE") {
			return r.Stdout, nil
		}
		if r.Err != nil {
			last = r.Err.Error()
		} else {
			last = strings.TrimSpace(r.Stderr)
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
	return "", fmt.Errorf("caddy root cert not readable after %d attempts: %s", attempts, last)
}

// caddyExec is daemonExec for the cobalt_caddy service task. Kept
// separate so changes to the daemon's docker-exec contract don't
// silently retarget calls meant for Caddy.
func caddyExec(ctx context.Context, conn *ssh.Conn, cmd string) *ssh.Result {
	full := "id=$(docker ps --filter label=com.docker.swarm.service.name=cobalt_caddy -q | head -1); " +
		"if [ -z \"$id\" ]; then echo 'cobalt_caddy task not running' >&2; exit 1; fi; " +
		"docker exec \"$id\" sh -c " + shellSingleQuote(cmd)
	return conn.Run(ctx, full)
}
