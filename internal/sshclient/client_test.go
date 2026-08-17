package sshclient

import (
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"io"
	"regexp"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

// generateTestKey returns an ssh.PublicKey and its authorized_keys line.
func generateTestKey(t *testing.T) (ssh.PublicKey, string) {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	sshPub, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatalf("convert key: %v", err)
	}
	return sshPub, string(ssh.MarshalAuthorizedKey(sshPub))
}

func TestBuildHostKeyCallbackInsecure(t *testing.T) {
	cb, err := buildHostKeyCallback(Options{InsecureSkipHostKeyVerify: true})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	key, _ := generateTestKey(t)
	if err := cb("switch:22", nil, key); err != nil {
		t.Errorf("insecure callback rejected key: %v", err)
	}
}

func TestBuildHostKeyCallbackMissing(t *testing.T) {
	if _, err := buildHostKeyCallback(Options{Host: "sw1"}); err == nil {
		t.Fatal("expected error when neither HostKey nor InsecureSkipHostKeyVerify is set")
	}
}

func TestBuildHostKeyCallbackFingerprint(t *testing.T) {
	key, _ := generateTestKey(t)
	otherKey, _ := generateTestKey(t)

	cb, err := buildHostKeyCallback(Options{HostKey: ssh.FingerprintSHA256(key)})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := cb("switch:22", nil, key); err != nil {
		t.Errorf("matching fingerprint rejected: %v", err)
	}
	if err := cb("switch:22", nil, otherKey); err == nil {
		t.Error("mismatched fingerprint accepted")
	}
}

func TestBuildHostKeyCallbackAuthorizedKey(t *testing.T) {
	key, line := generateTestKey(t)
	otherKey, _ := generateTestKey(t)

	for name, hostKey := range map[string]string{
		"bare":        line,
		"known_hosts": "10.0.0.1 " + line, // leading hostname field is stripped
	} {
		cb, err := buildHostKeyCallback(Options{HostKey: hostKey})
		if err != nil {
			t.Fatalf("%s: unexpected error: %v", name, err)
		}
		if err := cb("switch:22", nil, key); err != nil {
			t.Errorf("%s: matching key rejected: %v", name, err)
		}
		if err := cb("switch:22", nil, otherKey); err == nil {
			t.Errorf("%s: mismatched key accepted", name)
		}
	}
}

func TestBuildHostKeyCallbackUnparseable(t *testing.T) {
	if _, err := buildHostKeyCallback(Options{HostKey: "not a key"}); err == nil {
		t.Fatal("expected parse error for garbage host key")
	}
}

func TestBuildHostKeyCallbackRejectsMarkers(t *testing.T) {
	_, line := generateTestKey(t)
	for _, marker := range []string{"@cert-authority", "@revoked"} {
		hostKey := marker + " 10.0.0.1 " + line
		if _, err := buildHostKeyCallback(Options{HostKey: hostKey}); err == nil ||
			!strings.Contains(err.Error(), marker) {
			t.Errorf("%s line: expected marker rejection, got %v", marker, err)
		}
	}
}

func TestExecuteRejectsControlCharacters(t *testing.T) {
	c := &Client{}
	for _, cmd := range []string{"vlan 10\nexit", "vlan 10\rexit", "vlan\t10", "vlan 10\x7f"} {
		if _, err := c.execute(cmd); err == nil || !strings.Contains(err.Error(), "control characters") {
			t.Errorf("command %q: expected control-character rejection, got %v", cmd, err)
		}
	}
}

// TestReadUntilPromptTimesOut is the regression test for the read-hang bug:
// before the byte-pump refactor, a switch that went silent blocked ReadByte
// forever and the configured timeout never fired.
func TestReadUntilPromptTimesOut(t *testing.T) {
	c := &Client{
		byteCh:  make(chan byteResult), // never fed — simulates a silent switch
		options: Options{TimeoutSeconds: 1},
	}

	type result struct {
		out string
		err error
	}
	done := make(chan result, 1)
	go func() {
		out, err := c.readUntilPrompt()
		done <- result{out, err}
	}()

	select {
	case res := <-done:
		if res.err == nil || !strings.Contains(res.err.Error(), "timeout") {
			t.Errorf("expected timeout error, got %v", res.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("readUntilPrompt did not return — hang regression")
	}
}

// TestReadUntilPromptPartialOutputOnTimeout verifies bytes received before the
// switch went silent are returned alongside the timeout error.
func TestReadUntilPromptPartialOutputOnTimeout(t *testing.T) {
	ch := make(chan byteResult, 16)
	for _, b := range []byte("partial line") {
		ch <- byteResult{b: b}
	}
	c := &Client{byteCh: ch, options: Options{TimeoutSeconds: 1}}

	out, err := c.readUntilPrompt()
	if err == nil || !strings.Contains(err.Error(), "timeout") {
		t.Fatalf("expected timeout error, got %v", err)
	}
	if !strings.Contains(out, "partial line") {
		t.Errorf("partial output lost on timeout: %q", out)
	}
}

// TestReadUntilPromptSurfacesTransportError verifies non-EOF errors from the
// pump goroutine (e.g. connection reset) reach the caller instead of being
// collapsed into a generic EOF.
func TestReadUntilPromptSurfacesTransportError(t *testing.T) {
	ch := make(chan byteResult, 4)
	ch <- byteResult{b: 'x'}
	ch <- byteResult{err: errors.New("connection reset by peer")}
	c := &Client{byteCh: ch, options: Options{TimeoutSeconds: 5}}

	_, err := c.readUntilPrompt()
	if err == nil || !strings.Contains(err.Error(), "connection reset by peer") {
		t.Errorf("transport error not surfaced, got %v", err)
	}
}

// TestReadUntilPromptEOFOnClosedChannel verifies a closed pump channel (SSH
// pipe EOF / session closed) is reported as an error, not a silent success.
func TestReadUntilPromptEOFOnClosedChannel(t *testing.T) {
	ch := make(chan byteResult)
	close(ch)
	c := &Client{byteCh: ch, options: Options{TimeoutSeconds: 5}}

	_, err := c.readUntilPrompt()
	if err == nil || !errors.Is(err, io.EOF) {
		t.Errorf("expected EOF error on closed channel, got %v", err)
	}
}

// TestReadUntilPromptReturnsOnPrompt verifies the happy path: output followed
// by the detected prompt returns the output without the prompt line.
func TestReadUntilPromptReturnsOnPrompt(t *testing.T) {
	ch := make(chan byteResult, 64)
	for _, b := range []byte("vlan 10 name servers\nSSH@ICX7250-24 Router# ") {
		ch <- byteResult{b: b}
	}
	c := &Client{
		byteCh:      ch,
		options:     Options{TimeoutSeconds: 5},
		promptRegex: regexp.MustCompile(`SSH@ICX7250-24 Router\s*(?:\([^\)]*\))?[#>]\s*$`),
	}

	out, err := c.readUntilPrompt()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out, "vlan 10 name servers") || strings.Contains(out, "Router#") {
		t.Errorf("unexpected output: %q", out)
	}
}
