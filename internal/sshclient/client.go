package sshclient

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net"
	"regexp"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
)

var _ CommandExecutor = &Client{}

// byteResult carries a single byte pumped from the SSH stdout pipe, or an error.
type byteResult struct {
	b   byte
	err error
}

// errDeadlineExceeded is returned by readByteWithDeadline when the deadline
// passes before a byte arrives from the switch.
var errDeadlineExceeded = errors.New("deadline exceeded")

// Client manages an interactive SSH shell session to an ICX switch.
type Client struct {
	mu           sync.Mutex
	sshClient    *ssh.Client
	session      *ssh.Session
	stdin        io.WriteCloser
	byteCh       chan byteResult // pumped by the background reader goroutine
	promptRegex  *regexp.Regexp
	promptString string // The exact detected prompt (e.g., "ICX7250-24 Router#")
	inConfigMode bool
	options      Options
}

// NewClient connects to the switch, starts an interactive shell, disables paging,
// enters enable mode if needed, and returns a ready-to-use Client.
func NewClient(opts Options) (*Client, error) {
	if opts.Port == 0 {
		opts.Port = 22
	}
	if opts.TimeoutSeconds == 0 {
		opts.TimeoutSeconds = 30
	}

	hostKeyCallback, err := buildHostKeyCallback(opts)
	if err != nil {
		return nil, fmt.Errorf("host key config: %w", err)
	}

	config := &ssh.ClientConfig{
		User: opts.Username,
		Auth: []ssh.AuthMethod{
			ssh.Password(opts.Password),
		},
		HostKeyCallback: hostKeyCallback,
		Timeout:         time.Duration(opts.TimeoutSeconds) * time.Second,
	}

	addr := fmt.Sprintf("%s:%d", opts.Host, opts.Port)
	sshClient, err := ssh.Dial("tcp", addr, config)
	if err != nil {
		return nil, fmt.Errorf("ssh dial: %w", err)
	}

	session, err := sshClient.NewSession()
	if err != nil {
		_ = sshClient.Close()
		return nil, fmt.Errorf("ssh session: %w", err)
	}

	// Request a pseudo-terminal so the switch sends prompts.
	if err := session.RequestPty("xterm", 80, 256, ssh.TerminalModes{
		ssh.ECHO: 0,
	}); err != nil {
		_ = session.Close()
		_ = sshClient.Close()
		return nil, fmt.Errorf("request pty: %w", err)
	}

	stdin, err := session.StdinPipe()
	if err != nil {
		_ = session.Close()
		_ = sshClient.Close()
		return nil, fmt.Errorf("stdin pipe: %w", err)
	}

	stdout, err := session.StdoutPipe()
	if err != nil {
		_ = session.Close()
		_ = sshClient.Close()
		return nil, fmt.Errorf("stdout pipe: %w", err)
	}

	if err := session.Shell(); err != nil {
		_ = session.Close()
		_ = sshClient.Close()
		return nil, fmt.Errorf("start shell: %w", err)
	}

	c := &Client{
		sshClient: sshClient,
		session:   session,
		stdin:     stdin,
		byteCh:    make(chan byteResult, 256),
		options:   opts,
	}

	// Background goroutine: pump bytes from stdout into byteCh.
	// Closes byteCh when the pipe errors or reaches EOF (including on session close).
	go func() {
		buf := make([]byte, 1)
		defer close(c.byteCh)
		for {
			_, err := stdout.Read(buf)
			if err != nil {
				return
			}
			c.byteCh <- byteResult{b: buf[0]}
		}
	}()

	// Wait for the initial prompt.
	initialOutput, err := c.readUntilPromptInitial()
	if err != nil {
		_ = c.Close()
		return nil, fmt.Errorf("waiting for initial prompt: %w", err)
	}

	// Detect if we're in user mode (>) and need to enable.
	if strings.HasSuffix(strings.TrimSpace(initialOutput), ">") {
		if opts.EnablePassword == "" {
			_ = c.Close()
			return nil, fmt.Errorf("switch requires enable password but none was provided")
		}
		if err := c.enterEnableMode(); err != nil {
			_ = c.Close()
			return nil, fmt.Errorf("enter enable mode: %w", err)
		}
	}

	// Build the prompt regex now that we're in privileged mode.
	if err := c.detectPrompt(); err != nil {
		_ = c.Close()
		return nil, fmt.Errorf("detect prompt: %w", err)
	}

	// Disable paging and verify the command succeeded.
	output, err := c.Execute("skip-page-display")
	if err != nil {
		_ = c.Close()
		return nil, fmt.Errorf("disable paging: %w", err)
	}
	if errMsg := detectCLIError(output); errMsg != "" {
		_ = c.Close()
		return nil, fmt.Errorf("disable paging: %s", errMsg)
	}

	return c, nil
}

// buildHostKeyCallback constructs the ssh.HostKeyCallback from Options.
// Returns an error if neither HostKey nor InsecureSkipHostKeyVerify is set.
func buildHostKeyCallback(opts Options) (ssh.HostKeyCallback, error) {
	if opts.InsecureSkipHostKeyVerify {
		return ssh.InsecureIgnoreHostKey(), nil //nolint:gosec // caller-requested
	}

	if opts.HostKey == "" {
		return nil, fmt.Errorf(
			"host key verification required: set host_key to the switch's SSH public key or SHA256 fingerprint "+
				"(obtain with: ssh-keyscan %s), or set insecure_skip_host_key_verify = true for trusted lab networks only",
			opts.Host,
		)
	}

	line := strings.TrimSpace(opts.HostKey)

	// SHA256 fingerprint format: "SHA256:base64..."
	if strings.HasPrefix(line, "SHA256:") {
		expected := line
		return func(hostname string, _ net.Addr, key ssh.PublicKey) error {
			presented := ssh.FingerprintSHA256(key)
			if presented != expected {
				return fmt.Errorf(
					"SSH host key fingerprint mismatch for %s: presented %s, expected %s",
					hostname, presented, expected,
				)
			}
			return nil
		}, nil
	}

	// Authorized_keys / known_hosts line format.
	// known_hosts lines begin with a hostname field before the key type.
	// SSH key types always start with "ssh-", "ecdsa-", or "sk-"; if the first
	// token is not a key type, strip the leading hostname field.
	if fields := strings.Fields(line); len(fields) >= 2 && !isSSHKeyType(fields[0]) {
		line = strings.Join(fields[1:], " ")
	}

	parsedKey, _, _, _, err := ssh.ParseAuthorizedKey([]byte(line))
	if err != nil {
		return nil, fmt.Errorf("parse host_key %q: %w", opts.HostKey, err)
	}
	expectedMarshal := parsedKey.Marshal()

	return func(hostname string, _ net.Addr, key ssh.PublicKey) error {
		if !bytes.Equal(key.Marshal(), expectedMarshal) {
			return fmt.Errorf(
				"SSH host key mismatch for %s: presented key fingerprint is %s; "+
					"update host_key or run ssh-keyscan %s to get the current key",
				hostname, ssh.FingerprintSHA256(key), opts.Host,
			)
		}
		return nil
	}, nil
}

// isSSHKeyType returns true if s is a recognized SSH public key type prefix.
func isSSHKeyType(s string) bool {
	return strings.HasPrefix(s, "ssh-") ||
		strings.HasPrefix(s, "ecdsa-") ||
		strings.HasPrefix(s, "sk-")
}

// readByteWithDeadline reads the next byte from the background reader goroutine.
// It blocks until a byte arrives or the deadline is reached.  One timer is
// allocated per call (far cheaper than time.After, which cannot be GC'd early).
func (c *Client) readByteWithDeadline(deadline time.Time) (byte, error) {
	remaining := time.Until(deadline)
	if remaining <= 0 {
		return 0, errDeadlineExceeded
	}
	timer := time.NewTimer(remaining)
	defer timer.Stop()
	select {
	case res, ok := <-c.byteCh:
		if !ok {
			// Channel closed — the SSH pipe reached EOF or the session was closed.
			return 0, io.EOF
		}
		return res.b, res.err
	case <-timer.C:
		return 0, errDeadlineExceeded
	}
}

// Execute sends a command and returns the output (excluding the prompt).
func (c *Client) Execute(command string) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.execute(command)
}

// ExecuteInConfigMode enters config mode if needed and runs the commands sequentially.
func (c *Client) ExecuteInConfigMode(commands []string) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if !c.inConfigMode {
		if _, err := c.execute("configure terminal"); err != nil {
			return fmt.Errorf("enter config mode: %w", err)
		}
		c.inConfigMode = true
		// Prompt regex already handles (config) variants from detectPrompt.
	}

	for _, cmd := range commands {
		output, err := c.execute(cmd)
		if err != nil {
			return fmt.Errorf("command %q: %w", cmd, err)
		}
		if errMsg := detectCLIError(output); errMsg != "" {
			return fmt.Errorf("command %q: %s", cmd, errMsg)
		}
	}

	return nil
}

// WriteMemory saves the running configuration to startup configuration.
func (c *Client) WriteMemory() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Exit config mode first if we're in it.
	if c.inConfigMode {
		if _, err := c.execute("end"); err != nil {
			return fmt.Errorf("exit config mode: %w", err)
		}
		c.inConfigMode = false
	}

	output, err := c.execute("write memory")
	if err != nil {
		return fmt.Errorf("write memory: %w", err)
	}
	if errMsg := detectCLIError(output); errMsg != "" {
		return fmt.Errorf("write memory: %s", errMsg)
	}

	return nil
}

// GetRunningConfig returns the full running configuration.
func (c *Client) GetRunningConfig() (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Must be in privileged EXEC mode for show commands.
	if c.inConfigMode {
		if _, err := c.execute("end"); err != nil {
			return "", fmt.Errorf("exit config mode: %w", err)
		}
		c.inConfigMode = false
	}

	return c.execute("show running-config")
}

// Close exits config mode, closes the SSH session and connection.
// Closing the session causes the reader goroutine to exit, so Close never deadlocks.
func (c *Client) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	var errs []string

	if c.inConfigMode {
		if _, err := c.execute("end"); err != nil {
			errs = append(errs, fmt.Sprintf("exit config mode: %v", err))
		}
		c.inConfigMode = false
	}
	if c.session != nil {
		if err := c.session.Close(); err != nil {
			errs = append(errs, fmt.Sprintf("session close: %v", err))
		}
	}
	if c.sshClient != nil {
		if err := c.sshClient.Close(); err != nil {
			errs = append(errs, fmt.Sprintf("client close: %v", err))
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("%s", strings.Join(errs, "; "))
	}
	return nil
}

// execute sends a command and reads output until the prompt. Must be called with mu held.
func (c *Client) execute(command string) (string, error) {
	// Reject commands containing control characters — defense-in-depth against
	// CLI injection via embedded newlines, carriage returns, or other controls.
	for _, b := range []byte(command) {
		if b < 0x20 {
			return "", fmt.Errorf("command contains control characters and was not sent")
		}
	}

	// Send the command.
	if _, err := fmt.Fprintf(c.stdin, "%s\n", command); err != nil {
		return "", fmt.Errorf("write command: %w", err)
	}

	// Read until we see the prompt.
	output, err := c.readUntilPrompt()
	if err != nil {
		return "", err
	}

	// Strip ANSI escape codes and terminal artifacts.
	output = sanitizeOutput(output)

	// Strip the echoed command from the beginning of output.
	output = stripEchoedCommand(output, command)

	return strings.TrimSpace(output), nil
}

// readUntilPrompt reads from the shell until the prompt regex matches the current line.
func (c *Client) readUntilPrompt() (string, error) {
	var outputLines []string
	var currentLine strings.Builder
	deadline := time.Now().Add(time.Duration(c.options.TimeoutSeconds) * time.Second)

	for {
		b, err := c.readByteWithDeadline(deadline)
		if err != nil {
			buf := strings.Join(outputLines, "\n") + currentLine.String()
			if errors.Is(err, errDeadlineExceeded) {
				return buf, fmt.Errorf("timeout waiting for prompt")
			}
			return buf, fmt.Errorf("read: %w", err)
		}

		if b == '\n' {
			outputLines = append(outputLines, currentLine.String())
			currentLine.Reset()
			continue
		}

		currentLine.WriteByte(b)

		// Check if the current (incomplete) line matches the prompt.
		// We only check after non-newline chars to avoid false matches mid-output.
		if c.promptRegex != nil && currentLine.Len() > 3 {
			line := strings.TrimRight(currentLine.String(), "\r ")
			if c.promptRegex.MatchString(line) {
				// Current line is the prompt — return all previous lines.
				return strings.Join(outputLines, "\n"), nil
			}
		}
	}
}

// readUntilPromptInitial reads until we see either > or # at the end of a line.
// Used before prompt detection is set up.
func (c *Client) readUntilPromptInitial() (string, error) {
	var buf strings.Builder
	deadline := time.Now().Add(time.Duration(c.options.TimeoutSeconds) * time.Second)
	initialPrompt := regexp.MustCompile(`[\w@\-]+(?:\s+\w+)?\s*[#>]\s*$`)

	for {
		b, err := c.readByteWithDeadline(deadline)
		if err != nil {
			bufStr := buf.String()
			if errors.Is(err, errDeadlineExceeded) {
				return bufStr, fmt.Errorf("timeout waiting for initial prompt (buffer: %q)", bufStr)
			}
			return bufStr, fmt.Errorf("read: %w", err)
		}

		buf.WriteByte(b)

		if initialPrompt.MatchString(buf.String()) {
			return buf.String(), nil
		}
	}
}

// enterEnableMode sends the enable command and password.
func (c *Client) enterEnableMode() error {
	if _, err := fmt.Fprintf(c.stdin, "enable\n"); err != nil {
		return fmt.Errorf("send enable: %w", err)
	}

	// Read until we see a password prompt or the privileged prompt.
	// The switch may go straight to # if no enable password is configured
	// ("No password has been assigned yet...").
	var buf strings.Builder
	deadline := time.Now().Add(time.Duration(c.options.TimeoutSeconds) * time.Second)
	// Password prompt is literally "Password:" at the end of a line — not
	// "password" appearing in a sentence.
	passwordPrompt := regexp.MustCompile(`(?i)^Password:\s*$`)
	privPrompt := regexp.MustCompile(`[\w@\-]+(?:\s+\w+)?#\s*$`)
	passwordSent := false

	for {
		b, err := c.readByteWithDeadline(deadline)
		if err != nil {
			bufStr := buf.String()
			if errors.Is(err, errDeadlineExceeded) {
				return fmt.Errorf("timeout waiting for enable prompt (buffer: %q)", bufStr)
			}
			return fmt.Errorf("read: %w", err)
		}

		buf.WriteByte(b)
		content := buf.String()

		// Check for privileged prompt — the whole buffer ends with hostname#.
		if privPrompt.MatchString(content) {
			return nil
		}

		// Check for password prompt — only on the current line after a newline.
		if !passwordSent {
			lines := strings.Split(content, "\n")
			lastLine := strings.TrimRight(lines[len(lines)-1], "\r ")
			if passwordPrompt.MatchString(lastLine) {
				if _, err := fmt.Fprintf(c.stdin, "%s\n", c.options.EnablePassword); err != nil {
					return fmt.Errorf("send enable password: %w", err)
				}
				passwordSent = true
				buf.Reset()
			}
		}
	}
}

// detectPrompt sends an empty line and captures the exact prompt string.
// FastIron prompts look like "SSH@ICX7250-24 Router#" or "SSH@ICX7250-24 Router(config)#".
// We capture the base portion before the # or > and build a regex that matches it in any mode.
func (c *Client) detectPrompt() error {
	if _, err := fmt.Fprintf(c.stdin, "\n"); err != nil {
		return fmt.Errorf("send newline: %w", err)
	}

	var buf strings.Builder
	deadline := time.Now().Add(time.Duration(c.options.TimeoutSeconds) * time.Second)
	// Match prompts ending in # or > with optional (config...) before the # or >.
	// The base portion can include @, letters, digits, hyphens, spaces.
	anyPrompt := regexp.MustCompile(`([\w@\-]+(?:\s+\w+)?)\s*(?:\([^\)]*\))?[#>]\s*$`)

	for {
		b, err := c.readByteWithDeadline(deadline)
		if err != nil {
			bufStr := buf.String()
			if errors.Is(err, errDeadlineExceeded) {
				return fmt.Errorf("timeout detecting prompt (buffer: %q)", bufStr)
			}
			return fmt.Errorf("read: %w", err)
		}

		buf.WriteByte(b)

		// Check the last line of the buffer for a prompt match.
		content := buf.String()
		lines := strings.Split(content, "\n")
		lastLine := strings.TrimRight(lines[len(lines)-1], "\r ")
		if m := anyPrompt.FindStringSubmatch(lastLine); m != nil {
			// m[1] is the base portion, e.g., "SSH@ICX7250-24 Router"
			c.promptString = strings.TrimSpace(lastLine)
			// Build a regex that matches this exact base with any mode suffix.
			escaped := regexp.QuoteMeta(m[1])
			c.promptRegex = regexp.MustCompile(escaped + `\s*(?:\([^\)]*\))?[#>]\s*$`)
			return nil
		}
	}
}

// ansiRegex matches ANSI escape sequences.
var ansiRegex = regexp.MustCompile(`\x1b\[[0-9;]*[a-zA-Z]|\x1b\].*?\x07|\x1b[^[\]].?`)

// carriageReturnRegex matches carriage returns (often paired with newlines in terminal output).
var carriageReturnRegex = regexp.MustCompile(`\r`)

// sanitizeOutput strips ANSI escape codes and carriage returns from terminal output.
func sanitizeOutput(s string) string {
	s = ansiRegex.ReplaceAllString(s, "")
	s = carriageReturnRegex.ReplaceAllString(s, "")
	return s
}

// detectCLIError checks command output for known FastIron error patterns.
func detectCLIError(output string) string {
	errorPatterns := []string{
		"Invalid input ->",
		"Error:",
		"Error -",
		"Not authorized",
		"Incomplete command",
		"Ambiguous input",
	}

	for _, pattern := range errorPatterns {
		if strings.Contains(output, pattern) {
			return output
		}
	}
	return ""
}

// stripEchoedCommand removes the echoed command line from the beginning of output.
func stripEchoedCommand(output, command string) string {
	lines := strings.SplitN(output, "\n", 2)
	if len(lines) > 1 && strings.Contains(lines[0], command) {
		return lines[1]
	}
	return output
}
