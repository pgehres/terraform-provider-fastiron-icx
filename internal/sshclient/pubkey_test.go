package sshclient

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"testing"

	"golang.org/x/crypto/ssh"
)

// newTestKey returns a fresh ed25519 key as OpenSSH PEM (optionally
// encrypted) and its public half.
func newTestKey(t *testing.T, passphrase string) (string, ssh.PublicKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	var block *pem.Block
	if passphrase == "" {
		block, err = ssh.MarshalPrivateKey(priv, "test")
	} else {
		block, err = ssh.MarshalPrivateKeyWithPassphrase(priv, "test", []byte(passphrase))
	}
	if err != nil {
		t.Fatal(err)
	}
	sshPub, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(block)), sshPub
}

func methodNames(t *testing.T, opts Options) []string {
	t.Helper()
	methods, err := authMethods(opts)
	if err != nil {
		t.Fatalf("authMethods: %v", err)
	}
	// ssh.AuthMethod hides its method name; the concrete types tell them apart.
	var names []string
	for _, m := range methods {
		names = append(names, fmt.Sprintf("%T", m))
	}
	return names
}

func TestAuthMethodsPrivateKey(t *testing.T) {
	key, _ := newTestKey(t, "")

	if got := methodNames(t, Options{PrivateKey: key}); len(got) != 1 || !strings.Contains(got[0], "publicKeyCallback") {
		t.Errorf("key only: want public key auth only, got %v", got)
	}
	got := methodNames(t, Options{PrivateKey: key, Password: "pw"})
	if len(got) != 2 || !strings.Contains(got[0], "publicKeyCallback") || !strings.Contains(got[1], "passwordCallback") {
		t.Errorf("key and password: want public key then password, got %v", got)
	}
}

func TestAuthMethodsBadKey(t *testing.T) {
	_, err := authMethods(Options{PrivateKey: "not a key"})
	if err == nil {
		t.Fatal("want an error for an unparseable key")
	}
	if strings.Contains(err.Error(), "not a key") {
		t.Errorf("error must not echo the key: %v", err)
	}
}

func TestParsePrivateKeyPassphrase(t *testing.T) {
	key, _ := newTestKey(t, "s3cret")
	if _, err := ParsePrivateKey(key, ""); err == nil || !strings.Contains(err.Error(), "passphrase") {
		t.Errorf("encrypted key without passphrase: got %v", err)
	}
	if _, err := ParsePrivateKey(key, "wrong"); err == nil {
		t.Error("wrong passphrase must fail")
	}
	if _, err := ParsePrivateKey(key, "s3cret"); err != nil {
		t.Errorf("right passphrase: %v", err)
	}
}

// sshFakeServer is an SSH server on loopback that accepts only one public key
// and connects each shell to a fakeSwitch, so NewClient runs end to end.
type sshFakeServer struct {
	addr     string
	mu       sync.Mutex
	methods  []string // auth methods the client tried
	password bool     // a password attempt arrived
}

func startSSHFakeServer(t *testing.T, allowed ssh.PublicKey, sw *fakeSwitch) *sshFakeServer {
	t.Helper()
	_, hostPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	hostSigner, err := ssh.NewSignerFromKey(hostPriv)
	if err != nil {
		t.Fatal(err)
	}
	srv := &sshFakeServer{}
	cfg := &ssh.ServerConfig{
		PublicKeyCallback: func(_ ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
			srv.mu.Lock()
			srv.methods = append(srv.methods, "publickey")
			srv.mu.Unlock()
			if bytes.Equal(key.Marshal(), allowed.Marshal()) {
				return &ssh.Permissions{}, nil
			}
			return nil, errors.New("key not allowed")
		},
		PasswordCallback: func(_ ssh.ConnMetadata, _ []byte) (*ssh.Permissions, error) {
			srv.mu.Lock()
			srv.methods = append(srv.methods, "password")
			srv.password = true
			srv.mu.Unlock()
			return nil, errors.New("password not allowed")
		},
	}
	cfg.AddHostKey(hostSigner)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	srv.addr = ln.Addr().String()

	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		_, chans, reqs, err := ssh.NewServerConn(conn, cfg)
		if err != nil {
			_ = conn.Close()
			return
		}
		go ssh.DiscardRequests(reqs)
		for nc := range chans {
			if nc.ChannelType() != "session" {
				_ = nc.Reject(ssh.UnknownChannelType, "session only")
				continue
			}
			ch, chReqs, err := nc.Accept()
			if err != nil {
				return
			}
			go func() {
				for r := range chReqs {
					ok := r.Type == "pty-req" || r.Type == "shell"
					_ = r.Reply(ok, nil)
					if r.Type == "shell" {
						clientEnd, switchEnd := net.Pipe()
						sw.conn = switchEnd
						go sw.run()
						go func() { _, _ = io.Copy(ch, clientEnd) }()
						go func() { _, _ = io.Copy(clientEnd, ch) }()
					}
				}
			}()
		}
	}()
	return srv
}

func (s *sshFakeServer) hostPort(t *testing.T) (string, int) {
	t.Helper()
	host, port, err := net.SplitHostPort(s.addr)
	if err != nil {
		t.Fatal(err)
	}
	p, err := strconv.Atoi(port)
	if err != nil {
		t.Fatal(err)
	}
	return host, p
}

func TestNewClientPublicKeyLogin(t *testing.T) {
	key, pub := newTestKey(t, "")
	sw := newFakeSwitch(t)
	sw.hostname = "SSH@ICX7450-48P Switch"
	srv := startSSHFakeServer(t, pub, sw)
	host, port := srv.hostPort(t)

	c, err := NewClient(Options{
		Host: host, Port: port, Username: "iac", PrivateKey: key,
		InsecureSkipHostKeyVerify: true, TimeoutSeconds: 3,
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	defer func() { _ = c.Close() }()

	out, err := c.GetRunningConfig()
	if err != nil {
		t.Fatalf("GetRunningConfig: %v", err)
	}
	if !strings.Contains(out, "ver 08.0.95") {
		t.Errorf("running config not read through the key-authenticated session: %q", out)
	}
	srv.mu.Lock()
	defer srv.mu.Unlock()
	if srv.password {
		t.Errorf("no password was configured, so none may be offered; methods tried: %v", srv.methods)
	}
}

func TestNewClientWrongKeyRejected(t *testing.T) {
	key, _ := newTestKey(t, "")
	_, otherPub := newTestKey(t, "")
	sw := newFakeSwitch(t)
	srv := startSSHFakeServer(t, otherPub, sw)
	host, port := srv.hostPort(t)

	_, err := NewClient(Options{
		Host: host, Port: port, Username: "iac", PrivateKey: key,
		InsecureSkipHostKeyVerify: true, TimeoutSeconds: 3,
	})
	if err == nil || !strings.Contains(err.Error(), "unable to authenticate") {
		t.Fatalf("want an authentication failure, got %v", err)
	}
}
