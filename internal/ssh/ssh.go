package ssh

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
)

type AuthMethod interface {
	auth() []ssh.AuthMethod
}

type PasswordAuth struct {
	Password string
}

func (a PasswordAuth) auth() []ssh.AuthMethod {
	return []ssh.AuthMethod{ssh.Password(a.Password)}
}

type PublicKeyAuth struct {
	KeyPath    string
	Passphrase string
}

func (a PublicKeyAuth) auth() []ssh.AuthMethod {
	key, err := parsePrivateKey(a.KeyPath, a.Passphrase)
	if err != nil {
		return []ssh.AuthMethod{}
	}
	return []ssh.AuthMethod{ssh.PublicKeys(key)}
}

func parsePrivateKey(path, passphrase string) (ssh.Signer, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read key file: %w", err)
	}
	key, err := ssh.ParsePrivateKey(data)
	if err != nil {
		if _, ok := err.(*ssh.PassphraseMissingError); ok && passphrase != "" {
			key, err = ssh.ParsePrivateKeyWithPassphrase(data, []byte(passphrase))
		}
		if err != nil {
			return nil, fmt.Errorf("parse private key: %w", err)
		}
	}
	return key, nil
}

type AgentAuth struct {
	Socket string
}

func (a AgentAuth) auth() []ssh.AuthMethod {
	conn, err := net.Dial("unix", a.Socket)
	if err != nil {
		return []ssh.AuthMethod{}
	}
	// Intentionally do not close conn: the SSH library invokes the
	// signer callback during the handshake, which requires a live
	// agent connection. The OS reaps the fd on process exit.
	return []ssh.AuthMethod{ssh.PublicKeysCallback(agent.NewClient(conn).Signers)}
}

func DefaultAgentSocket() string {
	return os.Getenv("SSH_AUTH_SOCK")
}

func AskPassword(prompt string) string {
	fmt.Print(prompt + ": ")
	reader := bufio.NewReader(os.Stdin)
	password, _ := reader.ReadString('\n')
	return strings.TrimSuffix(password, "\n")
}

type Client struct {
	config *ssh.ClientConfig
	addr   string
}

func NewClient(user, host string, auth AuthMethod) *Client {
	return &Client{
		addr: fmt.Sprintf("%s:22", host),
		config: &ssh.ClientConfig{
			User:            user,
			Auth:            auth.auth(),
			HostKeyCallback: ssh.InsecureIgnoreHostKey(),
			Timeout:         10 * time.Second,
		},
	}
}

type Conn struct {
	client *ssh.Client
}

func (c *Client) Connect() (*Conn, error) {
	conn, err := ssh.Dial("tcp", c.addr, c.config)
	if err != nil {
		return nil, fmt.Errorf("dial %s: %w", c.addr, err)
	}
	return &Conn{client: conn}, nil
}

func (c *Conn) Close() error {
	return c.client.Close()
}

type Result struct {
	Stdout   string
	Stderr   string
	ExitCode int
	Err      error
}

func (c *Conn) Run(ctx context.Context, cmd string) *Result {
	session, err := c.client.NewSession()
	if err != nil {
		return &Result{Err: fmt.Errorf("new session: %w", err)}
	}
	defer session.Close()

	var stdout, stderr bytes.Buffer
	session.Stdout = &stdout
	session.Stderr = &stderr

	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			session.Signal(ssh.SIGTERM)
		case <-done:
		}
	}()

	if err := session.Start(cmd); err != nil {
		return &Result{Err: fmt.Errorf("start: %w", err)}
	}

	err = session.Wait()
	close(done)

	if err != nil {
		if exitErr, ok := err.(*ssh.ExitError); ok {
			return &Result{Stdout: stdout.String(), Stderr: stderr.String(), ExitCode: exitErr.ExitStatus()}
		}
		return &Result{Stdout: stdout.String(), Stderr: stderr.String(), Err: fmt.Errorf("wait: %w", err)}
	}

	return &Result{Stdout: stdout.String(), Stderr: stderr.String(), ExitCode: 0}
}

// ScpTo uploads localPath to remotePath by streaming the file contents into
// a remote `cat > path` shell redirect. Not the SCP wire protocol — just a
// trivial pipe-into-file that works whether or not scp(1) is installed on
// the remote. Creates the parent directory and applies the local file's mode.
func (c *Conn) ScpTo(localPath, remotePath string) error {
	file, err := os.Open(localPath)
	if err != nil {
		return err
	}
	defer file.Close()

	stat, err := file.Stat()
	if err != nil {
		return err
	}

	session, err := c.client.NewSession()
	if err != nil {
		return err
	}
	defer session.Close()

	stdin, err := session.StdinPipe()
	if err != nil {
		return err
	}

	var errBuf bytes.Buffer
	session.Stderr = &errBuf

	rq := shellSingleQuote(remotePath)
	dq := shellSingleQuote(filepathDir(remotePath))
	cmd := fmt.Sprintf("mkdir -p %s && cat > %s && chmod %o %s", dq, rq, stat.Mode().Perm(), rq)
	if err := session.Start(cmd); err != nil {
		return fmt.Errorf("start upload: %w", err)
	}

	if _, err := io.Copy(stdin, file); err != nil {
		stdin.Close()
		return fmt.Errorf("copy data: %w", err)
	}
	if err := stdin.Close(); err != nil {
		return fmt.Errorf("close stdin: %w", err)
	}

	if err := session.Wait(); err != nil {
		return fmt.Errorf("upload: %v: %s", err, errBuf.String())
	}
	return nil
}

// shellSingleQuote wraps s in single quotes for a POSIX shell, escaping any
// embedded single quotes via the standard `'\''` trick.
func shellSingleQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// filepathDir returns everything before the last '/' in p, or "." if there
// is no slash. Avoids importing path/filepath which on Windows would use '\\'.
func filepathDir(p string) string {
	for i := len(p) - 1; i >= 0; i-- {
		if p[i] == '/' {
			if i == 0 {
				return "/"
			}
			return p[:i]
		}
	}
	return "."
}

func ParseSSHURL(raw string) (user, host string) {
	host = raw
	if idx := strings.IndexByte(raw, '@'); idx != -1 {
		user = raw[:idx]
		host = raw[idx+1:]
	}
	return
}

func WaitForInterrupt() {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, os.Interrupt, syscall.SIGTERM)
	<-ch
}

func CheckSSHKey(keyPath string) error {
	_, err := parsePrivateKey(keyPath, "")
	if err != nil {
		return fmt.Errorf("invalid SSH key: %w", err)
	}
	return nil
}

type copier struct {
	w   io.Writer
	r   io.Reader
	buf [32 << 10]byte
}

func (c *copier) Run() error {
	for {
		n, err := c.r.Read(c.buf[:])
		if n > 0 {
			if _, err := c.w.Write(c.buf[:n]); err != nil {
				return err
			}
		}
		if err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}
	}
}