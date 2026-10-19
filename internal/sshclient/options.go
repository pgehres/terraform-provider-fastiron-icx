package sshclient

// Transport names accepted by Options.Transport and Connect.
const (
	TransportSSH    = "ssh"
	TransportTelnet = "telnet"
)

// Options holds the configuration for connecting to an ICX switch.
type Options struct {
	Host string
	// Port is the TCP port; 0 means the transport's default (22 for SSH, 23
	// for telnet).
	Port           int
	Username       string
	Password       string
	EnablePassword string
	TimeoutSeconds int

	// Transport selects the interactive transport: TransportSSH (the default
	// when empty) or TransportTelnet. Telnet is plaintext — credentials and
	// every command cross the wire unencrypted and the switch's identity is
	// never verified. It exists for factory-fresh or competition switches
	// that expose nothing else; the provider requires an explicit
	// acknowledgement before using it.
	Transport string

	// AllowEmptyCredentials permits connecting with an empty username,
	// password and/or enable password, as a factory-fresh switch with no
	// local users accepts. Over SSH it also offers keyboard-interactive
	// authentication answered with the (empty) password; the x/crypto client
	// always tries the "none" method first regardless. Without it, a user-mode
	// prompt with no enable password configured is an error, as before.
	AllowEmptyCredentials bool

	// HostKey is the switch's SSH host public key in authorized_keys / known_hosts
	// format (e.g. "ssh-rsa AAAA...") or a SHA256 fingerprint (e.g. "SHA256:...").
	// If a known_hosts-style line with a leading hostname field is provided, the
	// hostname field is stripped automatically.  Obtain the key with ssh-keyscan,
	// or with FetchHostKey / `debug-ssh -print-host-key` on older FastIron whose
	// SSH stack ssh-keyscan cannot negotiate with.
	HostKey string

	// InsecureSkipHostKeyVerify disables SSH host key verification entirely.
	// WARNING: this permits man-in-the-middle interception; use only on isolated
	// lab networks where MITM is not a concern.
	InsecureSkipHostKeyVerify bool

	// LegacySSHAlgorithms additionally offers the SHA-1/CBC-era SSH algorithms
	// that x/crypto still implements but excludes by default
	// (diffie-hellman-group14-sha1, diffie-hellman-group1-sha1,
	// diffie-hellman-group-exchange-sha1 key exchange; aes128-cbc and 3des-cbc
	// ciphers; ssh-rsa and ssh-dss host key signatures). Old FastIron SSH
	// stacks — e.g. FastIron 08.0.30 on the EOL ICX 6430 — support nothing
	// newer. Modern algorithms stay first in the preference order, so switches
	// that support them are unaffected. WARNING: these algorithms are
	// cryptographically weak; enable only for EOL devices on trusted networks.
	LegacySSHAlgorithms bool
}
