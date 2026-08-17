package sshclient

import (
	"crypto/ed25519"
	"crypto/rand"
	"strings"
	"testing"

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
