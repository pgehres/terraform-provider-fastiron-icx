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
	mu              sync.Mutex
	Commands        []string
	Responses       map[string]string
	RunningConfig   string
	WriteMemoryCalls int
	ErrorOnCommand  map[string]error
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

func (m *MockClient) WriteMemory() error {
	m.mu.Lock()
	defer m.mu.Unlock()

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
}
