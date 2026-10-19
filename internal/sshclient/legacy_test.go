package sshclient

import (
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
)

func TestLegacyAlgorithms(t *testing.T) {
	kex, ciphers, hostKeys := legacyAlgorithms()

	contains := func(list []string, want string) bool {
		for _, v := range list {
			if v == want {
				return true
			}
		}
		return false
	}

	// Legacy additions needed by old FastIron stacks (e.g. 08.0.30 on ICX 6430)
	// must be offered.
	for _, want := range []string{
		"diffie-hellman-group14-sha1",
		"diffie-hellman-group1-sha1",
		"diffie-hellman-group-exchange-sha1",
	} {
		if !contains(kex, want) {
			t.Errorf("key exchanges missing %s: %v", want, kex)
		}
	}
	for _, want := range []string{"aes128-cbc", "3des-cbc"} {
		if !contains(ciphers, want) {
			t.Errorf("ciphers missing %s: %v", want, ciphers)
		}
	}
	for _, want := range []string{"ssh-rsa", "ssh-dss"} {
		if !contains(hostKeys, want) {
			t.Errorf("host key algorithms missing %s: %v", want, hostKeys)
		}
	}

	// RC4 must never be offered, even in legacy mode.
	for _, c := range ciphers {
		if strings.HasPrefix(c, "arcfour") {
			t.Errorf("legacy ciphers must not include RC4, got %s", c)
		}
	}

	// Modern algorithms must stay first in the preference order, so switches
	// that support them are unaffected by legacy mode.
	supported := ssh.SupportedAlgorithms()
	if len(kex) <= len(supported.KeyExchanges) || kex[0] != supported.KeyExchanges[0] {
		t.Errorf("legacy KEX list must extend, not replace, the secure defaults: %v", kex)
	}
	if ciphers[0] != supported.Ciphers[0] {
		t.Errorf("legacy cipher list must keep secure defaults first: %v", ciphers)
	}
	if hostKeys[0] != supported.HostKeys[0] {
		t.Errorf("legacy host key list must keep secure defaults first: %v", hostKeys)
	}

	// Every algorithm we offer must be one this x/crypto version implements —
	// an unknown name would fail the handshake setup at dial time.
	insecure := ssh.InsecureAlgorithms()
	implemented := map[string]bool{}
	for _, l := range [][]string{
		supported.KeyExchanges, supported.Ciphers, supported.HostKeys,
		insecure.KeyExchanges, insecure.Ciphers, insecure.HostKeys,
	} {
		for _, v := range l {
			implemented[v] = true
		}
	}
	for _, l := range [][]string{kex, ciphers, hostKeys} {
		for _, v := range l {
			if !implemented[v] {
				t.Errorf("offered algorithm %s is not implemented by x/crypto", v)
			}
		}
	}
}
