package resource

import (
	"fmt"
	"strings"
	"testing"
)

func TestRedactPassword(t *testing.T) {
	tests := []struct {
		name     string
		errMsg   string
		password string
	}{
		{
			name:     "plain password",
			errMsg:   `command "username admin password hunter2": Invalid input`,
			password: "hunter2",
		},
		{
			name:     "password with quote is escaped by %q",
			errMsg:   fmt.Sprintf("command %q: Invalid input", `username admin password hun"ter`),
			password: `hun"ter`,
		},
		{
			name:     "password with backslash is escaped by %q",
			errMsg:   fmt.Sprintf("command %q: Invalid input", `username admin password hun\ter`),
			password: `hun\ter`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := redactPassword(tt.errMsg, tt.password)
			if strings.Contains(got, tt.password) {
				t.Errorf("raw password survived redaction: %s", got)
			}
			if !strings.Contains(got, "(redacted)") {
				t.Errorf("no redaction marker in output: %s", got)
			}
		})
	}

	// Empty password must not cause a mass replacement.
	if got := redactPassword("some error", ""); got != "some error" {
		t.Errorf("empty password altered message: %q", got)
	}
}
