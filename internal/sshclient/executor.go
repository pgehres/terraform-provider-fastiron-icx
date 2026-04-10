package sshclient

// CommandExecutor defines the interface for executing CLI commands on an ICX switch.
// Resources depend on this interface, enabling mock implementations for testing.
type CommandExecutor interface {
	// Execute sends a command and returns the output.
	Execute(command string) (string, error)

	// ExecuteInConfigMode enters config mode if needed and runs the commands.
	ExecuteInConfigMode(commands []string) error

	// WriteMemory saves the running config to startup config.
	WriteMemory() error

	// GetRunningConfig returns the full running configuration.
	GetRunningConfig() (string, error)

	// Close closes the SSH session and connection.
	Close() error
}
