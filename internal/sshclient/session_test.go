package sshclient

import (
	"fmt"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeSwitch is a scripted FastIron CLI at the far end of a net.Pipe. It
// speaks just enough of the dialogue the session layer depends on — login
// prompts, user/privileged mode and `enable`, the empty-line prompt probe,
// skip-page-display, show running-config, configure terminal and write
// memory — echoing each command the way a real terminal session does. A
// reader goroutine drains everything the client sends (commands as lines,
// telnet negotiation as triples) so the client can never block on a pipe
// write while the fake is itself blocked writing.
type fakeSwitch struct {
	t        *testing.T
	conn     net.Conn
	hostname string // prompt base, e.g. "telnet@ICX7150-C12P Router"

	telnet bool // send telnet option negotiation before the first prompt

	loginUser, loginPass string // "" = no login prompts
	userMode             bool   // land at ">" so the client must `enable`
	enablePrompts        bool   // `enable` asks "Password:" (answer in enablePass)
	enablePass           string
	runningConfig        string

	lines chan string

	mu           sync.Mutex
	received     []string
	negotiations [][]byte
	mode         string // "", "(config)", "(config-vlan-10)" ...
	privileged   bool
}

func newFakeSwitch(t *testing.T) *fakeSwitch {
	return &fakeSwitch{
		t:             t,
		hostname:      "telnet@ICX7150-C12P Router",
		runningConfig: "ver 08.0.95qT213\n!\nvlan 1 name DEFAULT-VLAN by port\n!\nend",
		lines:         make(chan string, 64),
	}
}

func (f *fakeSwitch) sent() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string{}, f.received...)
}

func (f *fakeSwitch) negotiated() [][]byte {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([][]byte{}, f.negotiations...)
}

// run plays the script until the connection closes.
func (f *fakeSwitch) run() {
	go f.readLoop()

	if f.telnet {
		f.write(string([]byte{
			telnetIAC, telnetDO, 24, // DO TERMINAL-TYPE
			telnetIAC, telnetWILL, 1, // WILL ECHO
			telnetIAC, telnetWILL, 3, // WILL SUPPRESS-GO-AHEAD
			telnetIAC, 241, // NOP
		}))
	}
	if f.loginUser != "" {
		f.write("\r\nUser Name:")
		if got := f.next(); got != f.loginUser {
			f.write("\r\nLogin failed\r\nUser Name:")
			return // a rejected login re-prompts; the client must not answer again
		}
		f.write("\r\nPassword:")
		if got := f.next(); got != f.loginPass {
			f.write("\r\nLogin failed\r\nUser Name:")
			return
		}
	}
	f.privileged = !f.userMode
	f.write("\r\n" + f.prompt())

	for cmd := range f.lines {
		f.mu.Lock()
		f.received = append(f.received, cmd)
		f.mu.Unlock()
		f.handle(cmd)
	}
}

func (f *fakeSwitch) handle(cmd string) {
	echo := cmd + "\r\n"
	switch {
	case cmd == "":
		f.write("\r\n" + f.prompt())
	case cmd == "enable" && !f.privileged:
		if f.enablePrompts {
			f.write(echo + "Password:")
			if got := f.next(); got != f.enablePass {
				f.write("\r\nError - Incorrect password\r\n" + f.prompt())
				return
			}
			f.privileged = true
			f.write("\r\n" + f.prompt())
			return
		}
		f.privileged = true
		f.write(echo + "No password has been assigned yet...\r\n" + f.prompt())
	case cmd == "show version":
		f.write(echo + "  SW: Version 08.0.95qT213 Copyright (c) Ruckus Networks\r\n" + f.prompt())
	case cmd == "skip-page-display":
		f.write(echo + "Disable page display mode\r\n" + f.prompt())
	case cmd == "show running-config":
		f.write(echo + "Current configuration:\r\n!\r\n" + strings.ReplaceAll(f.runningConfig, "\n", "\r\n") + "\r\n" + f.prompt())
	case cmd == "configure terminal":
		f.mode = "(config)"
		f.write(echo + f.prompt())
	case cmd == "end":
		f.mode = ""
		f.write(echo + f.prompt())
	case cmd == "write memory":
		f.write(echo + "Write startup-config done.\r\n" + f.prompt())
	case strings.HasPrefix(cmd, "vlan ") && f.mode != "":
		f.mode = "(config-vlan-" + strings.TrimPrefix(cmd, "vlan ") + ")"
		f.write(echo + f.prompt())
	case strings.HasPrefix(cmd, "bogus"):
		f.write(echo + "Invalid input -> " + cmd + "\r\nType ? for a list\r\n" + f.prompt())
	default:
		f.write(echo + f.prompt())
	}
}

func (f *fakeSwitch) prompt() string {
	if !f.privileged {
		return f.hostname + ">"
	}
	return f.hostname + f.mode + "#"
}

func (f *fakeSwitch) write(s string) {
	_, _ = f.conn.Write([]byte(s)) // an error means the client closed the connection
}

// next returns the next line the client sent, or fails the test after 5s.
func (f *fakeSwitch) next() string {
	select {
	case l := <-f.lines:
		return l
	case <-time.After(5 * time.Second):
		f.t.Error("fake switch: timed out waiting for the client")
		return ""
	}
}

// readLoop splits the client's bytes into CR/LF-terminated lines and telnet
// negotiation triples.
func (f *fakeSwitch) readLoop() {
	defer close(f.lines)
	buf := make([]byte, 1)
	var line []byte
	var iac []byte
	for {
		if _, err := f.conn.Read(buf); err != nil {
			return
		}
		b := buf[0]
		switch {
		case len(iac) > 0:
			iac = append(iac, b)
			if len(iac) == 3 {
				f.mu.Lock()
				f.negotiations = append(f.negotiations, iac)
				f.mu.Unlock()
				iac = nil
			}
		case b == telnetIAC:
			iac = []byte{b}
		case b == '\n':
			f.lines <- strings.TrimRight(string(line), "\r")
			line = nil
		default:
			line = append(line, b)
		}
	}
}

// connectFake wires a fakeSwitch to the client side of a pipe and opens the
// session over the requested transport layer.
func connectFake(t *testing.T, f *fakeSwitch, opts Options) (*Client, error) {
	t.Helper()
	clientConn, serverConn := net.Pipe()
	f.conn = serverConn
	go f.run()
	if opts.TimeoutSeconds == 0 {
		opts.TimeoutSeconds = 2
	}
	if f.telnet {
		return newTelnetSession(clientConn, opts)
	}
	// The SSH transport authenticates before the shell opens, so it uses the
	// session layer directly with no login handling.
	c := newSessionClient(clientConn, clientConn, clientConn.Close, opts)
	initial, err := c.readUntilPromptInitial(false)
	if err != nil {
		_ = c.Close()
		return nil, err
	}
	if err := c.start(initial); err != nil {
		_ = c.Close()
		return nil, err
	}
	return c, nil
}

func contains(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

// A factory-fresh competition switch: telnet, no login, lands at ">",
// `enable` needs no password. The whole provider dialogue must work.
func TestTelnetSessionNoCredentials(t *testing.T) {
	f := newFakeSwitch(t)
	f.telnet, f.userMode = true, true

	c, err := connectFake(t, f, Options{Transport: TransportTelnet, AllowEmptyCredentials: true})
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer func() { _ = c.Close() }()

	if c.promptString != "telnet@ICX7150-C12P Router#" {
		t.Errorf("promptString = %q", c.promptString)
	}
	cfg, err := c.GetRunningConfig()
	if err != nil {
		t.Fatalf("GetRunningConfig: %v", err)
	}
	if !strings.Contains(cfg, "vlan 1 name DEFAULT-VLAN by port") || strings.Contains(cfg, "Router#") {
		t.Errorf("running config = %q", cfg)
	}
	if err := c.ExecuteInConfigMode([]string{"vlan 10", "name servers"}); err != nil {
		t.Fatalf("ExecuteInConfigMode: %v", err)
	}
	if err := c.WriteMemory(); err != nil {
		t.Fatalf("WriteMemory: %v", err)
	}
	if err := c.ExecuteInConfigMode([]string{"bogus command"}); err == nil || !strings.Contains(err.Error(), "Invalid input") {
		t.Errorf("CLI error not detected over telnet: %v", err)
	}

	want := []string{"enable", "", "skip-page-display", "show running-config", "configure terminal", "vlan 10", "name servers", "end", "write memory", "configure terminal", "bogus command"}
	if got := f.sent(); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("switch received %q\nwant %q", got, want)
	}
	wantNeg := [][]byte{{telnetIAC, telnetWONT, 24}, {telnetIAC, telnetDONT, 1}, {telnetIAC, telnetDONT, 3}}
	if got := f.negotiated(); fmt.Sprint(got) != fmt.Sprint(wantNeg) {
		t.Errorf("negotiation replies = % x, want % x", got, wantNeg)
	}
}

// A switch with local users prompts "User Name:" / "Password:" over telnet.
func TestTelnetSessionLoginPrompts(t *testing.T) {
	f := newFakeSwitch(t)
	f.telnet = true
	f.loginUser, f.loginPass = "admin", "s3cret"

	c, err := connectFake(t, f, Options{Transport: TransportTelnet, Username: "admin", Password: "s3cret"})
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer func() { _ = c.Close() }()
	out, err := c.Execute("show version")
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.HasPrefix(out, "SW: Version 08.0.95qT213") || strings.Contains(out, "Router#") {
		t.Errorf("show version output = %q, want the echo and prompt stripped", out)
	}
	// The login answers are consumed by the fake's login script, so the
	// first recorded command is the prompt probe.
	if got := f.sent(); len(got) == 0 || got[0] != "" || !contains(got, "show version") {
		t.Errorf("switch received %q", got)
	}
}

// A rejected login re-prompts; the client answers each prompt once and then
// fails by timeout with the switch's output, instead of retrying forever.
func TestTelnetSessionLoginRejected(t *testing.T) {
	f := newFakeSwitch(t)
	f.telnet = true
	f.loginUser, f.loginPass = "admin", "right"

	start := time.Now()
	_, err := connectFake(t, f, Options{Transport: TransportTelnet, Username: "admin", Password: "wrong", TimeoutSeconds: 1})
	if err == nil || !strings.Contains(err.Error(), "timeout") || !strings.Contains(err.Error(), "Login failed") {
		t.Fatalf("expected a timeout carrying the switch's output, got %v", err)
	}
	if time.Since(start) > 4*time.Second {
		t.Error("login failure must be bounded by the timeout")
	}
}

// The switch prompts for an enable password even though none is set, and
// accepts an empty line.
func TestSessionEnablePromptEmptyPassword(t *testing.T) {
	f := newFakeSwitch(t)
	f.telnet, f.userMode, f.enablePrompts = true, true, true

	c, err := connectFake(t, f, Options{Transport: TransportTelnet, AllowEmptyCredentials: true})
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer func() { _ = c.Close() }()
	if !strings.HasSuffix(c.promptString, "Router#") {
		t.Errorf("promptString = %q, want privileged mode", c.promptString)
	}
}

// Without the opt-in, a user-mode prompt and no enable password is still an
// error — nothing about the default behaviour changes.
func TestSessionUserModeNeedsEnablePasswordWithoutOptIn(t *testing.T) {
	f := newFakeSwitch(t)
	f.telnet, f.userMode = true, true

	_, err := connectFake(t, f, Options{Transport: TransportTelnet})
	if err == nil || !strings.Contains(err.Error(), "requires enable password") {
		t.Fatalf("expected the enable-password error, got %v", err)
	}
}

// The same session layer over a plain pipe, as the SSH transport uses it:
// a switch that lands in privileged mode straight away.
func TestSessionLayerOverPlainPipe(t *testing.T) {
	f := newFakeSwitch(t)
	f.hostname = "SSH@ICX7250-24 Router"

	c, err := connectFake(t, f, Options{Username: "pgehres", Password: "x"})
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	if c.promptString != "SSH@ICX7250-24 Router#" {
		t.Errorf("promptString = %q", c.promptString)
	}
	if err := c.ExecuteInConfigMode([]string{"vlan 20"}); err != nil {
		t.Fatalf("ExecuteInConfigMode: %v", err)
	}
	if err := c.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	// Close leaves config mode first, then closes the transport.
	if got := f.sent(); !contains(got, "end") || got[0] != "" || contains(got, "enable") {
		t.Errorf("switch received %q", got)
	}
	// The pump sees EOF once the pipe is closed, so a later call fails fast.
	if _, err := c.Execute("show version"); err == nil {
		t.Error("Execute after Close must fail")
	}
}

func TestConnectUnknownTransport(t *testing.T) {
	if _, err := Connect(Options{Transport: "serial"}); err == nil || !strings.Contains(err.Error(), "unknown transport") {
		t.Fatalf("got %v", err)
	}
}

func TestAuthMethodsGatedOnOptIn(t *testing.T) {
	if n := len(authMethods(Options{Password: "x"})); n != 1 {
		t.Errorf("default must offer password auth only, got %d methods", n)
	}
	if n := len(authMethods(Options{AllowEmptyCredentials: true})); n != 2 {
		t.Errorf("opt-in must add keyboard-interactive, got %d methods", n)
	}
}
