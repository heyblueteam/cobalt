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
	defer conn.Close()

	agentClient := agent.NewClient(conn)
	signers, err := agentClient.Signers()
	if err != nil || len(signers) == 0 {
		return []ssh.AuthMethod{}
	}
	return []ssh.AuthMethod{ssh.PublicKeys(signers...)}
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
			Timeout:         10,
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
	defer stdin.Close()

	var errBuf bytes.Buffer
	session.Stderr = &errBuf

	mkdirCmd := fmt.Sprintf(`sudo /bin/sh -c 'mkdir -p "$(dirname '%s')" && cat > '%s' && chmod %o '%s'<'`, remotePath, remotePath, stat.Mode().Perm(), remotePath)
	if err := session.Start(mkdirCmd); err != nil {
		return fmt.Errorf("start scp: %w", err)
	}

	header := fmt.Sprintf("C%04o %d %s\n", stat.Mode().Perm(), stat.Size(), remotePath)
	if _, err := stdin.Write([]byte(header)); err != nil {
		return fmt.Errorf("write header: %w", err)
	}

	if _, err := io.Copy(stdin, file); err != nil {
		return fmt.Errorf("copy data: %w", err)
	}

	_, err = stdin.Write([]byte("\x00"))
	if err != nil {
		return fmt.Errorf("write terminator: %w", err)
	}

	err = session.Wait()
	if err != nil {
		return fmt.Errorf("scp: %v: %s", err, errBuf.String())
	}
	return nil
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