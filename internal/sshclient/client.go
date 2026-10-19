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

// ErrSessionSevered is returned (wrapped) once the shell stopped answering
// after a command that was expected to cut the connection — a management IP
// change sent over the very address the provider is connected to. Every later
// command on the client fails immediately with it instead of waiting out the
// read timeout, so an apply that continues past the change fails fast with a
// clear cause.
var ErrSessionSevered = errors.New("session severed by a management address change; " +
	"update the provider's host and run terraform again")

// ErrSessionLostAfterHostname is returned (wrapped) when the prompt could not
// be re-detected after a `hostname` command. The switch may or may not have
// applied the new name, so the client can no longer tell where one command's
// output ends — it is marked disconnected and every later command fails
// immediately with this error rather than waiting out the read timeout each
// time. Nothing after the hostname command, `write memory` included, is sent.
var ErrSessionLostAfterHostname = errors.New("session lost after a hostname change (the new prompt " +
	"was not recognised); nothing after it was sent or saved — run terraform again to reconnect, then " +
	"check the hostname and save with `write memory` if needed")

// ErrTelnetOutOfSync is returned (wrapped) when a telnet command's output does
// not begin with the echo of that command — the sign that a stray prompt has
// shifted the session so that every read returns the previous command's
// output. The session is marked lost: failing loudly is the only safe answer,
// because returning the shifted output would hand resources the wrong data.
var ErrTelnetOutOfSync = errors.New("telnet session out of sync: a command's output did not start with its echo, " +
	"so it may belong to an earlier command; nothing more was sent — run terraform again to reconnect")

// anyPrompt matches a FastIron prompt of any hostname and mode:
// "SSH@ICX7250-24 Router#", "SSH@lab-sw.site Router(config)#", "ICX7150>" ...
// The base portion (hostname plus optional "Router"/"Switch" word) is captured
// so an exact prompt regex can be built from it. It is anchored to the start
// of the line: the match is attempted byte by byte, and an unanchored pattern
// would accept the moment FastIron's "Invalid input -> ..." error line ends in
// ">" and adopt "-" as the prompt.
var anyPrompt = regexp.MustCompile(`^\s*([\w@.\-]+(?:\s+\w+)?)\s*(?:\([^\)]*\))?[#>]\s*$`)

// Client manages an interactive CLI session to an ICX switch. The session
// layer — prompt detection, enable mode, paging, the byte pump with its
// deadlines, control-character rejection — is transport-agnostic: it reads
// from and writes to whatever interactive terminal newSessionClient was
// handed. NewClient supplies an SSH shell, NewTelnetClient a telnet
// connection; Connect picks one from Options.Transport.
type Client struct {
	mu             sync.Mutex
	stdin          io.Writer       // the terminal's input: SSH stdin pipe or telnet connection
	closeTransport func() error    // releases the SSH session + connection, or the TCP socket
	byteCh         chan byteResult // pumped by the background reader goroutine
	promptRegex    *regexp.Regexp
	promptString   string // The exact detected prompt (e.g., "ICX7250-24 Router#")
	inConfigMode   bool
	disconnected   bool  // set once the session is known to be unusable; see markLost
	lostErr        error // why; returned by every later call (ErrSessionSevered if unset)
	options        Options

	// Telnet-only desync defences; all inert over SSH (telnet is false).
	telnet     bool
	telnetEcho echoMode // whether the switch echoes commands, learned from the first one
}

// echoMode records whether a telnet switch echoes the commands it is sent.
// It is learned from the first command run through execute (skip-page-display,
// on a freshly drained session) and decides whether every later command's
// echo can be verified.
type echoMode int

const (
	echoUnknown echoMode = iota
	echoOn
	echoOff
)

// telnetQuietWindow is how long a telnet session must stay silent before a
// drain considers the switch done talking. A switch answers an Enter in
// milliseconds, so any stray prompt arrives well within it.
var telnetQuietWindow = 250 * time.Millisecond

// Connect opens a session over the transport named in opts.Transport ("ssh",
// the default when empty, or "telnet") and returns a ready-to-use Client.
func Connect(opts Options) (*Client, error) {
	switch opts.Transport {
	case "", TransportSSH:
		return NewClient(opts)
	case TransportTelnet:
		return NewTelnetClient(opts)
	default:
		return nil, fmt.Errorf("unknown transport %q (want %q or %q)", opts.Transport, TransportSSH, TransportTelnet)
	}
}

// NewClient connects to the switch over SSH, starts an interactive shell,
// disables paging, enters enable mode if needed, and returns a ready-to-use
// Client.
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
		User:            opts.Username,
		Auth:            authMethods(opts),
		HostKeyCallback: hostKeyCallback,
		Timeout:         time.Duration(opts.TimeoutSeconds) * time.Second,
	}

	if opts.LegacySSHAlgorithms {
		config.KeyExchanges, config.Ciphers, config.HostKeyAlgorithms = legacyAlgorithms()
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

	closeTransport := func() error {
		var errs []string
		if err := session.Close(); err != nil {
			errs = append(errs, fmt.Sprintf("session close: %v", err))
		}
		if err := sshClient.Close(); err != nil {
			errs = append(errs, fmt.Sprintf("client close: %v", err))
		}
		if len(errs) > 0 {
			return fmt.Errorf("%s", strings.Join(errs, "; "))
		}
		return nil
	}

	c := newSessionClient(stdout, stdin, closeTransport, opts)

	// SSH authenticated already, so the shell opens straight at a CLI prompt.
	initialOutput, err := c.readUntilPromptInitial(false)
	if err != nil {
		_ = c.Close()
		return nil, fmt.Errorf("waiting for initial prompt: %w", err)
	}
	if err := c.start(initialOutput); err != nil {
		_ = c.Close()
		return nil, err
	}
	return c, nil
}

// authMethods returns the SSH authentication methods offered for opts. The
// x/crypto client always tries the "none" method first (RFC 4252 §5.2) and
// only then the methods listed here, in the order the server suggests them —
// so a switch with no credentials configured is already covered when it
// accepts "none". Password authentication is always offered. With
// AllowEmptyCredentials the password (empty or not) is additionally given as
// the answer to every keyboard-interactive question, which is how some SSH
// stacks present their (possibly empty) password prompt instead of the
// "password" method; it is gated so that the default connection behaviour —
// a single password attempt — does not change for anyone else.
func authMethods(opts Options) []ssh.AuthMethod {
	methods := []ssh.AuthMethod{ssh.Password(opts.Password)}
	if opts.AllowEmptyCredentials {
		methods = append(methods, ssh.KeyboardInteractive(
			func(_, _ string, questions []string, _ []bool) ([]string, error) {
				answers := make([]string, len(questions))
				for i := range answers {
					answers[i] = opts.Password
				}
				return answers, nil
			}))
	}
	return methods
}

// newSessionClient wraps an already-connected interactive terminal — the SSH
// shell's stdout/stdin pipes, or a telnet connection — in a Client and starts
// the background byte pump. closeTransport releases the underlying
// connection; Close calls it. The caller then reads the login/initial prompt
// and calls start.
func newSessionClient(r io.Reader, w io.Writer, closeTransport func() error, opts Options) *Client {
	c := &Client{
		stdin:          w,
		closeTransport: closeTransport,
		byteCh:         make(chan byteResult, 256),
		options:        opts,
	}

	// Background goroutine: pump bytes from the terminal into byteCh.
	// Closes byteCh when the reader errors or reaches EOF (including on close).
	go func() {
		buf := make([]byte, 1)
		defer close(c.byteCh)
		for {
			_, err := r.Read(buf)
			if err != nil {
				// Surface non-EOF transport errors (e.g. connection reset) to the
				// reader instead of collapsing them into a generic EOF. The send is
				// non-blocking so the goroutine cannot leak if nothing is draining.
				if err != io.EOF {
					select {
					case c.byteCh <- byteResult{err: err}:
					default:
					}
				}
				return
			}
			c.byteCh <- byteResult{b: buf[0]}
		}
	}()

	return c
}

// start brings a freshly connected session to a usable state: it enters
// enable mode when the switch landed in user mode (a ">" prompt), learns the
// exact prompt, and disables paging. initialOutput is everything read up to
// and including the first CLI prompt. The caller closes the client on error.
func (c *Client) start(initialOutput string) error {
	// Detect if we're in user mode (>) and need to enable.
	if strings.HasSuffix(strings.TrimSpace(initialOutput), ">") {
		// A switch with no enable password configured answers `enable` with
		// "No password has been assigned yet" and a "#" prompt, which
		// enterEnableMode handles; and if it does prompt, an empty line is
		// sent. Both are only acceptable when the caller opted in.
		if c.options.EnablePassword == "" && !c.options.AllowEmptyCredentials {
			return fmt.Errorf("switch requires enable password but none was provided")
		}
		if err := c.enterEnableMode(); err != nil {
			return fmt.Errorf("enter enable mode: %w", err)
		}
	}

	// Over telnet, absorb anything still arriving (a second login prompt, a
	// banner repeated per line ending) so the prompt probe's answer is the
	// next thing read. Inert over SSH.
	if err := c.drainQuietIfTelnet(); err != nil {
		return err
	}

	// Build the prompt regex now that we're in privileged mode.
	if err := c.detectPrompt(); err != nil {
		return fmt.Errorf("detect prompt: %w", err)
	}
	if err := c.drainQuietIfTelnet(); err != nil {
		return err
	}

	// Disable paging and verify the command succeeded.
	output, err := c.Execute("skip-page-display")
	if err != nil {
		return fmt.Errorf("disable paging: %w", err)
	}
	if errMsg := detectCLIError(output); errMsg != "" {
		return fmt.Errorf("disable paging: %s", errMsg)
	}
	return nil
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
				"(obtain with: ssh-keyscan %s — or, on older FastIron whose SSH stack ssh-keyscan cannot negotiate with, "+
				"the provider repo's debug tool: go run ./cmd/debug-ssh -print-host-key), "+
				"or set insecure_skip_host_key_verify = true for trusted lab networks only",
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
	// known_hosts marker lines (@cert-authority, @revoked) are not a host key
	// pin — a CA key is not the switch's own key, and a revoked key must never
	// be accepted — so reject them outright rather than mis-parsing.
	fields := strings.Fields(line)
	if len(fields) > 0 && strings.HasPrefix(fields[0], "@") {
		return nil, fmt.Errorf(
			"host_key begins with known_hosts marker %q: @cert-authority and @revoked entries cannot be used as a pinned host key; "+
				"provide the switch's own public key (ssh-keyscan %s)",
			fields[0], opts.Host,
		)
	}
	// known_hosts lines begin with a hostname field before the key type.
	// SSH key types always start with "ssh-", "ecdsa-", or "sk-"; if the first
	// token is not a key type, strip the leading hostname field.
	if len(fields) >= 2 && !isSSHKeyType(fields[0]) {
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
					"update host_key with the current key (ssh-keyscan %s, or the provider repo's "+
					"debug tool if ssh-keyscan returns nothing: go run ./cmd/debug-ssh -print-host-key)",
				hostname, ssh.FingerprintSHA256(key), opts.Host,
			)
		}
		return nil
	}, nil
}

// legacyAlgorithms returns the key exchange, cipher, and host key algorithm
// lists used when Options.LegacySSHAlgorithms is set: x/crypto's current
// secure defaults extended with the SHA-1/CBC-era algorithms that old
// FastIron SSH stacks require. The legacy entries are appended after the
// secure ones, so a switch that supports a modern algorithm still negotiates
// it. The lists are built from ssh.InsecureAlgorithms() (filtered to the
// algorithms we actually intend to offer — RC4/arcfour is never offered)
// rather than hard-coded, so an algorithm x/crypto drops in a future release
// simply disappears from the offer instead of failing the handshake setup.
func legacyAlgorithms() (kex, ciphers, hostKeys []string) {
	supported := ssh.SupportedAlgorithms()
	insecure := ssh.InsecureAlgorithms()

	kex = append(append([]string{}, supported.KeyExchanges...), insecure.KeyExchanges...)

	allowedCiphers := map[string]bool{"aes128-cbc": true, "3des-cbc": true}
	ciphers = append([]string{}, supported.Ciphers...)
	for _, c := range insecure.Ciphers {
		if allowedCiphers[c] {
			ciphers = append(ciphers, c)
		}
	}

	allowedHostKeys := map[string]bool{"ssh-rsa": true, "ssh-dss": true}
	hostKeys = append([]string{}, supported.HostKeys...)
	for _, hk := range insecure.HostKeys {
		if allowedHostKeys[hk] {
			hostKeys = append(hostKeys, hk)
		}
	}

	return kex, ciphers, hostKeys
}

// FetchHostKey connects to the switch just far enough to capture its SSH host
// public key, then disconnects without authenticating. This exists because
// ssh-keyscan cannot negotiate with older FastIron SSH stacks (e.g. RomSShell
// 5.40 offers KEX algorithms modern OpenSSH refuses), while the x/crypto
// client used by this provider negotiates with them fine.
//
// The probe always offers the legacy algorithm lists: no credentials or data
// are sent during a key capture, and the captured key is verified out-of-band
// by the operator anyway, so there is nothing for a weak algorithm to weaken —
// while the widest offer lets the probe reach the oldest FastIron stacks
// (e.g. FastIron 08.0.30 on the ICX 6430) without extra flags.
func FetchHostKey(host string, port, timeoutSeconds int) (ssh.PublicKey, error) {
	if port == 0 {
		port = 22
	}
	if timeoutSeconds == 0 {
		timeoutSeconds = 15
	}

	var captured ssh.PublicKey
	config := &ssh.ClientConfig{
		User: "host-key-probe",
		HostKeyCallback: func(_ string, _ net.Addr, key ssh.PublicKey) error {
			captured = key
			// Abort the handshake — the key is all we came for.
			return fmt.Errorf("host key captured")
		},
		Timeout: time.Duration(timeoutSeconds) * time.Second,
	}
	config.KeyExchanges, config.Ciphers, config.HostKeyAlgorithms = legacyAlgorithms()

	client, err := ssh.Dial("tcp", fmt.Sprintf("%s:%d", host, port), config)
	if client != nil {
		_ = client.Close()
	}
	if captured != nil {
		return captured, nil
	}
	return nil, fmt.Errorf("fetch host key from %s: %w", host, err)
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

// ExecutePromptChanging runs one config-mode command whose success changes
// the CLI prompt — today only `hostname <name>`, since the prompt is
// "SSH@<hostname> Router#". The ordinary execute path waits for the prompt
// detected at connect time, which such a command makes obsolete, so it would
// wait out the full timeout and fail; and every command after it (including
// the `write memory` that saves the change) would fail the same way. This
// path instead sends the command and re-detects the prompt from whatever the
// switch prints next, then checks that output for a CLI error exactly like
// ExecuteInConfigMode does. If the command was rejected the prompt is simply
// re-detected unchanged.
func (c *Client) ExecutePromptChanging(command string) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if !c.inConfigMode {
		if _, err := c.execute("configure terminal"); err != nil {
			return fmt.Errorf("enter config mode: %w", err)
		}
		c.inConfigMode = true
	}

	if err := c.checkControlCharacters(command); err != nil {
		return err
	}
	if c.disconnected {
		return c.lostError()
	}
	if err := c.drainPendingIfTelnet(); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(c.stdin, "%s\n", command); err != nil {
		return fmt.Errorf("write command: %w", err)
	}
	output, err := c.readNewPrompt()
	if err != nil {
		// The switch may already run under the new name: the old prompt regex
		// is then useless and the shell is in an unknown mode. Mark the
		// session lost so later calls (WriteMemory included) fail fast with a
		// clear cause instead of each waiting out the timeout.
		c.markLost(ErrSessionLostAfterHostname)
		return fmt.Errorf("command %q: %w (re-detect prompt: %v)", command, ErrSessionLostAfterHostname, err)
	}
	if err := c.verifyEchoIfTelnet(sanitizeOutput(output), command); err != nil {
		return err
	}
	output = stripEchoedCommand(sanitizeOutput(output), command)
	if errMsg := detectCLIError(output); errMsg != "" {
		return fmt.Errorf("command %q: %s", command, strings.TrimSpace(errMsg))
	}
	return nil
}

// ExecuteExpectingDisconnect runs config-mode commands that are expected to
// cut this SSH session — changing the global management `ip address` the
// provider is connected through — and saves the configuration in the same
// breath. The commands, `end` and `write memory` are written to the shell in
// ONE write, without waiting for a prompt in between, so the switch has
// received the whole batch before it executes the first line; if the address
// change kills the connection, the `write memory` already sitting in its input
// buffer still has a chance to run. (Executing them one prompt at a time would
// guarantee the save is never sent.)
//
// Each command's output is then read in turn. A CLI error in any of them is
// returned as usual while the session is still alive. If the shell stops
// answering — timeout, EOF or transport error — the client is marked
// disconnected and ErrSessionSevered is returned (wrapped); from then on every
// method fails immediately with it. The caller cannot know whether the save
// ran in that case and must say so.
func (c *Client) ExecuteExpectingDisconnect(commands []string) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if len(commands) == 0 {
		return nil
	}
	for _, cmd := range commands {
		if err := c.checkControlCharacters(cmd); err != nil {
			return err
		}
	}
	if !c.inConfigMode {
		if _, err := c.execute("configure terminal"); err != nil {
			return fmt.Errorf("enter config mode: %w", err)
		}
		c.inConfigMode = true
	}
	if c.disconnected {
		return c.lostError()
	}

	if err := c.drainPendingIfTelnet(); err != nil {
		return err
	}
	batch := append(append([]string{}, commands...), "end", "write memory")
	if _, err := io.WriteString(c.stdin, strings.Join(batch, "\n")+"\n"); err != nil {
		return fmt.Errorf("write command batch: %w", err)
	}
	// Every line of the batch produces a prompt, so every one must be read
	// even after a CLI error — returning early would leave the output of the
	// remaining lines in the pipe to be mistaken for the next command's.
	var cliErr error
	for _, cmd := range batch {
		output, err := c.readUntilPrompt()
		if err != nil {
			c.markLost(ErrSessionSevered)
			return fmt.Errorf("after %q: %w (%v)", cmd, ErrSessionSevered, err)
		}
		if err := c.verifyEchoIfTelnet(sanitizeOutput(output), cmd); err != nil {
			return err
		}
		output = stripEchoedCommand(sanitizeOutput(output), cmd)
		if errMsg := detectCLIError(output); errMsg != "" && cliErr == nil {
			cliErr = fmt.Errorf("command %q: %s", cmd, strings.TrimSpace(errMsg))
		}
	}
	// The batch ended with `end`, so the shell is back in privileged EXEC mode
	// whether or not a command was rejected.
	c.inConfigMode = false
	return cliErr
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

// Close exits config mode and closes the underlying transport (SSH session
// and connection, or telnet socket). Closing the transport causes the reader
// goroutine to exit, so Close never deadlocks.
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
	if c.closeTransport != nil {
		if err := c.closeTransport(); err != nil {
			errs = append(errs, err.Error())
		}
		c.closeTransport = nil
	}

	if len(errs) > 0 {
		return fmt.Errorf("%s", strings.Join(errs, "; "))
	}
	return nil
}

// checkControlCharacters rejects commands containing control characters
// (including DEL) — defense-in-depth against CLI injection via embedded
// newlines, carriage returns, or other controls.
func (c *Client) checkControlCharacters(command string) error {
	if hasControlCharacters(command) {
		return fmt.Errorf("command contains control characters and was not sent")
	}
	return nil
}

// hasControlCharacters reports whether s contains a control character
// (0x00-0x1f) or DEL.
func hasControlCharacters(s string) bool {
	for _, b := range []byte(s) {
		if b < 0x20 || b == 0x7f {
			return true
		}
	}
	return false
}

// markLost records that the session can no longer be used, and why. From
// then on every command fails immediately with cause. Must be called with mu
// held.
func (c *Client) markLost(cause error) {
	c.disconnected = true
	c.lostErr = cause
}

// lostError is the error every call returns once the session was marked lost.
func (c *Client) lostError() error {
	if c.lostErr != nil {
		return c.lostErr
	}
	return ErrSessionSevered
}

// execute sends a command and reads output until the prompt. Must be called with mu held.
func (c *Client) execute(command string) (string, error) {
	if err := c.checkControlCharacters(command); err != nil {
		return "", err
	}
	if c.disconnected {
		return "", c.lostError()
	}
	if err := c.drainPendingIfTelnet(); err != nil {
		return "", err
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

	if err := c.verifyEchoIfTelnet(output, command); err != nil {
		return "", err
	}

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

// initialPrompt matches a CLI prompt of either mode (">" or "#") at the end
// of the buffer, before the exact prompt is known.
var initialPrompt = regexp.MustCompile(`[\w@\-]+(?:\s+\w+)?\s*[#>]\s*$`)

// Login prompts a telnet session may print before the CLI prompt: FastIron
// asks "User Name:" / "Password:" when local or AAA login is configured (older
// Foundry/FastIron releases "Please Enter Login Name:" / "Please Enter
// Password:") and prints nothing at all when it is not. Matched
// case-insensitively on the end of the current, still unterminated line, so
// any wording ending in "name:", "login:" or "password:" is answered, while a
// banner sentence that merely mentions a password (and is followed by a line
// break or more text) is not.
var (
	// loginPromptSettle is how long a candidate login prompt must stand
	// without further output before it is answered.
	loginPromptSettle = 150 * time.Millisecond

	usernamePrompt = regexp.MustCompile(`(?i)(?:name|login)\s*:\s*$`)
	passwordPrompt = regexp.MustCompile(`(?i)password\s*:\s*$`)
)

// readUntilPromptInitial reads until we see either > or # at the end of a
// line. Used before prompt detection is set up. With handleLogin set (telnet,
// where the transport has not authenticated anything) a "User Name:" or
// "Password:" line is answered with the configured username or password —
// each at most once, so a rejected login fails by timeout with the switch's
// output in the error instead of retrying forever — and reading continues
// until the CLI prompt appears.
func (c *Client) readUntilPromptInitial(handleLogin bool) (string, error) {
	var buf strings.Builder
	deadline := time.Now().Add(time.Duration(c.options.TimeoutSeconds) * time.Second)
	usernameSent, passwordSent := false, false
	// A byte read while checking that a login prompt had settled, to be
	// processed next. Only ever set when handleLogin is.
	var pushback *byte

	for {
		var b byte
		var err error
		if pushback != nil {
			b, pushback = *pushback, nil
		} else {
			b, err = c.readByteWithDeadline(deadline)
		}
		if err != nil {
			bufStr := buf.String()
			if errors.Is(err, errDeadlineExceeded) {
				return bufStr, fmt.Errorf("timeout waiting for initial prompt (buffer: %q)", bufStr)
			}
			return bufStr, fmt.Errorf("read: %w", err)
		}

		buf.WriteByte(b)
		content := buf.String()

		if initialPrompt.MatchString(content) {
			return content, nil
		}
		if !handleLogin {
			continue
		}

		lines := strings.Split(content, "\n")
		lastLine := strings.TrimRight(sanitizePromptLine(lines[len(lines)-1]), "\r ")
		wantUser := !usernameSent && usernamePrompt.MatchString(lastLine)
		wantPass := !passwordSent && passwordPrompt.MatchString(lastLine)
		if wantUser || wantPass {
			// The prompt patterns match a line suffix, so a banner line such
			// as "Last login: ..." matches mid-line. A real prompt waits for
			// input; a banner keeps coming. Answer only once the switch has
			// gone quiet.
			settle := time.Now().Add(loginPromptSettle)
			if settle.After(deadline) {
				settle = deadline
			}
			nb, nerr := c.readByteWithDeadline(settle)
			if nerr == nil {
				pushback = &nb
				continue
			}
			if !errors.Is(nerr, errDeadlineExceeded) {
				return content, fmt.Errorf("read: %w", nerr)
			}
		}
		switch {
		case wantUser:
			if _, err := fmt.Fprintf(c.stdin, "%s\n", c.options.Username); err != nil {
				return content, fmt.Errorf("send username: %w", err)
			}
			usernameSent = true
			buf.WriteByte('\n') // the answer is not echoed back; start a fresh line
		case wantPass:
			if _, err := fmt.Fprintf(c.stdin, "%s\n", c.options.Password); err != nil {
				return content, fmt.Errorf("send password: %w", err)
			}
			passwordSent = true
			buf.WriteByte('\n')
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
	_, err := c.readNewPrompt()
	return err
}

// readNewPrompt reads until the current line looks like a FastIron prompt of
// ANY hostname (anyPrompt), then installs that prompt as the one every later
// readUntilPrompt waits for. It returns everything read, prompt line included,
// so callers can inspect the output that preceded the prompt. Nothing is sent:
// the caller has already written whatever will make the switch print a prompt.
func (c *Client) readNewPrompt() (string, error) {
	var buf strings.Builder
	deadline := time.Now().Add(time.Duration(c.options.TimeoutSeconds) * time.Second)

	for {
		b, err := c.readByteWithDeadline(deadline)
		if err != nil {
			bufStr := buf.String()
			if errors.Is(err, errDeadlineExceeded) {
				return bufStr, fmt.Errorf("timeout detecting prompt (buffer: %q)", bufStr)
			}
			return bufStr, fmt.Errorf("read: %w", err)
		}

		buf.WriteByte(b)

		// Check the last line of the buffer for a prompt match. anyPrompt is
		// anchored, so terminal control sequences in front of the prompt (a
		// line-erase, cursor or colour code) must be stripped first or the
		// match never succeeds and detection times out.
		content := buf.String()
		lines := strings.Split(content, "\n")
		lastLine := strings.TrimRight(sanitizePromptLine(lines[len(lines)-1]), "\r ")
		if m := anyPrompt.FindStringSubmatch(lastLine); m != nil {
			// m[1] is the base portion, e.g., "SSH@ICX7250-24 Router"
			c.promptString = strings.TrimSpace(lastLine)
			// Build a regex that matches this exact base with any mode suffix.
			escaped := regexp.QuoteMeta(m[1])
			c.promptRegex = regexp.MustCompile(escaped + `\s*(?:\([^\)]*\))?[#>]\s*$`)
			return content, nil
		}
	}
}

// ansiRegex matches ANSI escape sequences.
var ansiRegex = regexp.MustCompile(`\x1b\[[0-9;]*[a-zA-Z]|\x1b\].*?\x07|\x1b[^[\]].?`)

// promptANSIRegex matches complete terminal control sequences precisely
// enough to strip them from a prompt line without eating prompt characters:
// CSI sequences including private modes ("\x1b[2K", "\x1b[?25h"), OSC
// sequences ending in BEL, charset designations ("\x1b(B") and two-byte
// escapes ("\x1b7", "\x1bM"). ansiRegex's catch-all `\x1b[^[\]].?` would
// also swallow the character after a two-byte escape — the "S" of "SSH@" —
// which is harmless in command output but would corrupt the detected prompt.
// An incomplete sequence is left alone, so byte-by-byte matching simply waits
// for the rest of it.
var promptANSIRegex = regexp.MustCompile(`\x1b\[[0-9;?]*[A-Za-z]|\x1b\][^\x07]*\x07|\x1b[()#][0-9A-Za-z]|\x1b[^\[\]()#]`)

// sanitizePromptLine strips terminal control sequences and carriage returns
// from a candidate prompt line before it is matched against anyPrompt.
func sanitizePromptLine(s string) string {
	s = promptANSIRegex.ReplaceAllString(s, "")
	return carriageReturnRegex.ReplaceAllString(s, "")
}

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
		// Lower-case form printed by the bulk `vlan-config` commands, e.g.
		// "error - IP routing, vrf, ... was configured on ports 1/1/1".
		"error -",
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

// drainQuietIfTelnet reads and discards everything the switch sends until it
// has been silent for telnetQuietWindow, bounded by the session timeout. Over
// telnet it runs at points where nothing further is expected, so whatever
// arrives — a second prompt answering a doubled line ending, a repeated
// banner — is stray and must not be mistaken for the next command's output.
// A no-op over SSH.
func (c *Client) drainQuietIfTelnet() error {
	if !c.telnet {
		return nil
	}
	deadline := time.Now().Add(time.Duration(c.options.TimeoutSeconds) * time.Second)
	for {
		quiet := time.Now().Add(telnetQuietWindow)
		if quiet.After(deadline) {
			quiet = deadline
		}
		_, err := c.readByteWithDeadline(quiet)
		switch {
		case err == nil:
			continue
		case errors.Is(err, errDeadlineExceeded):
			if time.Now().Before(deadline) {
				return nil // quiet for the whole window
			}
			return fmt.Errorf("telnet session never went quiet within the timeout")
		default:
			return fmt.Errorf("read: %w", err)
		}
	}
}

// resync re-establishes command/response alignment on a telnet session after
// start-up: one empty line, read up to its prompt, then a quiet drain. Any
// stray prompts the start-up dialogue provoked are consumed here rather than
// by the first real command.
func (c *Client) resync() error {
	if _, err := io.WriteString(c.stdin, "\n"); err != nil {
		return fmt.Errorf("telnet resync: write: %w", err)
	}
	if _, err := c.readUntilPrompt(); err != nil {
		return fmt.Errorf("telnet resync: %w", err)
	}
	if err := c.drainQuietIfTelnet(); err != nil {
		return fmt.Errorf("telnet resync: %w", err)
	}
	return nil
}

// drainPendingIfTelnet discards whatever a telnet switch has already sent
// before the next command is written: the client is strictly request/response
// and has read up to the previous prompt, so nothing pending can be the
// answer to the command about to be sent. Pending blank lines and prompts —
// the signature of a doubled Enter — are dropped silently. Anything else
// means the previous read ended early and its output was incomplete, so the
// session is marked out of sync. Blocks only when stray output is already
// arriving, until it stops (telnetQuietWindow). A no-op over SSH.
func (c *Client) drainPendingIfTelnet() error {
	if !c.telnet {
		return nil
	}
	var pending strings.Builder
	for done := false; !done; {
		select {
		case res, ok := <-c.byteCh:
			if !ok {
				return fmt.Errorf("read: %w", io.EOF)
			}
			if res.err != nil {
				return fmt.Errorf("read: %w", res.err)
			}
			pending.WriteByte(res.b)
		default:
			done = true
		}
	}
	if pending.Len() > 0 {
		// Something stray is arriving; let it finish (a prompt may be only
		// partly here) before judging it.
		deadline := time.Now().Add(time.Duration(c.options.TimeoutSeconds) * time.Second)
		for {
			quiet := time.Now().Add(telnetQuietWindow)
			if quiet.After(deadline) {
				quiet = deadline
			}
			b, err := c.readByteWithDeadline(quiet)
			if errors.Is(err, errDeadlineExceeded) {
				break
			}
			if err != nil {
				return fmt.Errorf("read: %w", err)
			}
			pending.WriteByte(b)
		}
	}
	for _, line := range strings.Split(sanitizeOutput(pending.String()), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || (c.promptRegex != nil && c.promptRegex.MatchString(line)) {
			continue
		}
		c.markLost(ErrTelnetOutOfSync)
		return fmt.Errorf("unexpected output %q before the next command: %w", truncate(line, 80), ErrTelnetOutOfSync)
	}
	return nil
}

// verifyEchoIfTelnet checks that a telnet command's (sanitized) output starts
// with the switch's echo of that command. The first command on the session
// decides whether the switch echoes at all; a switch that does not cannot be
// checked this way and relies on the drains alone. Only the command's first
// word is required on the echo line, so an echo the switch wraps or redraws
// for a narrow terminal still passes, while a shifted read — whose first line
// is empty or a bare prompt — does not. A no-op over SSH.
func (c *Client) verifyEchoIfTelnet(output, command string) error {
	if !c.telnet {
		return nil
	}
	first := strings.SplitN(output, "\n", 2)[0]
	echoed := strings.Contains(first, command)
	if fields := strings.Fields(command); !echoed && len(fields) > 0 {
		echoed = strings.Contains(first, fields[0])
	}
	switch c.telnetEcho {
	case echoUnknown:
		if echoed {
			c.telnetEcho = echoOn
		} else {
			c.telnetEcho = echoOff
		}
		return nil
	case echoOn:
		if echoed || strings.TrimSpace(command) == "" {
			return nil
		}
		c.markLost(ErrTelnetOutOfSync)
		return fmt.Errorf("command %q: output began with %q instead of its echo: %w",
			command, truncate(strings.TrimSpace(first), 80), ErrTelnetOutOfSync)
	}
	return nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
