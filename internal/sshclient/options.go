package sshclient

// Options holds the configuration for connecting to an ICX switch via SSH.
type Options struct {
	Host           string
	Port           int
	Username       string
	Password       string
	EnablePassword string
	TimeoutSeconds int
}
