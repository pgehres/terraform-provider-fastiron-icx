package sshclient

import (
	"fmt"
	"strings"
	"sync"
)

var _ CommandExecutor = &MockClient{}

// MockClient implements CommandExecutor for unit testing.
// It records commands sent and returns pre-configured responses.
type MockClient struct {
	mu               sync.Mutex
	Commands         []string
	Responses        map[string]string
	RunningConfig    string
	WriteMemoryCalls int
	ErrorOnCommand   map[string]error

	// PromptRedetects counts ExecutePromptChanging calls; PromptChangingCommands
	// records which commands went through that path (they also appear in
	// Commands, in sequence with everything else).
	PromptRedetects        int
	PromptChangingCommands []string
	// DisconnectBatches records each ExecuteExpectingDisconnect batch (the
	// caller's commands only; the mock appends "end" and "write memory" to
	// Commands like the real client sends them). Set SeverOnDisconnectBatch to
	// make the batch report a lost session, as the real switch would when the
	// management address changes under the connection.
	DisconnectBatches      [][]string
	SeverOnDisconnectBatch bool
	Disconnected           bool
}

// NewMockClient creates a MockClient with the given running config.
func NewMockClient(runningConfig string) *MockClient {
	return &MockClient{
		Responses:      make(map[string]string),
		ErrorOnCommand: make(map[string]error),
		RunningConfig:  runningConfig,
	}
}

func (m *MockClient) Execute(command string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.Commands = append(m.Commands, command)

	if err, ok := m.ErrorOnCommand[command]; ok {
		return "", err
	}
	if resp, ok := m.Responses[command]; ok {
		return resp, nil
	}
	return "", nil
}

func (m *MockClient) ExecuteInConfigMode(commands []string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	for _, cmd := range commands {
		m.Commands = append(m.Commands, cmd)

		if err, ok := m.ErrorOnCommand[cmd]; ok {
			return fmt.Errorf("command %q: %w", cmd, err)
		}
	}
	return nil
}

func (m *MockClient) ExecutePromptChanging(command string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.Disconnected {
		return ErrSessionSevered
	}
	m.Commands = append(m.Commands, command)
	m.PromptChangingCommands = append(m.PromptChangingCommands, command)
	m.PromptRedetects++
	if err, ok := m.ErrorOnCommand[command]; ok {
		return fmt.Errorf("command %q: %w", command, err)
	}
	return nil
}

func (m *MockClient) ExecuteExpectingDisconnect(commands []string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.Disconnected {
		return ErrSessionSevered
	}
	if len(commands) == 0 {
		return nil
	}
	m.DisconnectBatches = append(m.DisconnectBatches, append([]string{}, commands...))
	for _, cmd := range commands {
		m.Commands = append(m.Commands, cmd)
		if err, ok := m.ErrorOnCommand[cmd]; ok {
			return fmt.Errorf("command %q: %w", cmd, err)
		}
	}
	if m.SeverOnDisconnectBatch {
		m.Disconnected = true
		return fmt.Errorf("after %q: %w", commands[len(commands)-1], ErrSessionSevered)
	}
	m.Commands = append(m.Commands, "end", "write memory")
	m.WriteMemoryCalls++
	return nil
}

func (m *MockClient) WriteMemory() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.Disconnected {
		return ErrSessionSevered
	}
	m.WriteMemoryCalls++
	m.Commands = append(m.Commands, "write memory")
	return nil
}

func (m *MockClient) GetRunningConfig() (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.Commands = append(m.Commands, "show running-config")
	return m.RunningConfig, nil
}

func (m *MockClient) Close() error {
	return nil
}

// HasCommand returns true if the given command was executed.
func (m *MockClient) HasCommand(cmd string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()

	for _, c := range m.Commands {
		if c == cmd {
			return true
		}
	}
	return false
}

// HasCommandContaining returns true if any executed command contains the substring.
func (m *MockClient) HasCommandContaining(substr string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()

	for _, c := range m.Commands {
		if strings.Contains(c, substr) {
			return true
		}
	}
	return false
}

// Reset clears all recorded commands and call counts.
func (m *MockClient) Reset() {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.Commands = nil
	m.WriteMemoryCalls = 0
	m.PromptRedetects = 0
	m.PromptChangingCommands = nil
	m.DisconnectBatches = nil
}
