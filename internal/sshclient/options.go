package sshclient

// Options holds the configuration for connecting to an ICX switch via SSH.
type Options struct {
	Host           string
	Port           int
	Username       string
	Password       string
	EnablePassword string
	TimeoutSeconds int

	// HostKey is the switch's SSH host public key in authorized_keys / known_hosts
	// format (e.g. "ssh-rsa AAAA...") or a SHA256 fingerprint (e.g. "SHA256:...").
	// If a known_hosts-style line with a leading hostname field is provided, the
	// hostname field is stripped automatically.  Obtain the key with ssh-keyscan.
	HostKey string

	// InsecureSkipHostKeyVerify disables SSH host key verification entirely.
	// WARNING: this permits man-in-the-middle interception; use only on isolated
	// lab networks where MITM is not a concern.
	InsecureSkipHostKeyVerify bool
}
