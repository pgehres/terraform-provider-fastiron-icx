package sshclient

// CommandExecutor defines the interface for executing CLI commands on an ICX switch.
// Resources depend on this interface, enabling mock implementations for testing.
type CommandExecutor interface {
	// Execute sends a command and returns the output.
	Execute(command string) (string, error)

	// ExecuteInConfigMode enters config mode if needed and runs the commands.
	ExecuteInConfigMode(commands []string) error

	// ExecutePromptChanging runs one config-mode command that changes the
	// CLI prompt (`hostname`) and re-detects the prompt afterwards, so the
	// commands that follow — including WriteMemory — keep working. Sending
	// such a command through ExecuteInConfigMode instead hangs until the
	// read timeout and breaks the session for everything after it.
	ExecutePromptChanging(command string) error

	// ExecuteExpectingDisconnect runs config-mode commands that are expected
	// to cut this session (the switch image's global management `ip
	// address`), followed by `end` and `write memory`, all written to the
	// shell at once so the save is already queued when the address changes.
	// It returns an error wrapping ErrSessionSevered when the session was
	// lost; whether the save ran is then unknown.
	ExecuteExpectingDisconnect(commands []string) error

	// WriteMemory saves the running config to startup config.
	WriteMemory() error

	// GetRunningConfig returns the full running configuration.
	GetRunningConfig() (string, error)

	// Close closes the SSH session and connection.
	Close() error
}
