package sshclient

import (
	"errors"
	"regexp"
	"strings"
	"testing"
	"time"
)

// recordingStdin stands in for the shell's stdin: every Write is kept
// separately so tests can assert what was sent and in how many writes.
type recordingStdin struct {
	writes []string
}

func (w *recordingStdin) Write(p []byte) (int, error) {
	w.writes = append(w.writes, string(p))
	return len(p), nil
}

func (w *recordingStdin) Close() error { return nil }

const oldPrompt = `SSH@ICX7250-24 Router\s*(?:\([^\)]*\))?[#>]\s*$`

// feed returns a channel pre-loaded with what the "switch" prints. closed
// closes it afterwards, which the byte pump does when the connection dies.
func feed(output string, closed bool) chan byteResult {
	ch := make(chan byteResult, len(output)+1)
	for _, b := range []byte(output) {
		ch <- byteResult{b: b}
	}
	if closed {
		close(ch)
	}
	return ch
}

func promptClient(ch chan byteResult, stdin *recordingStdin) *Client {
	return &Client{
		byteCh:       ch,
		stdin:        stdin,
		options:      Options{TimeoutSeconds: 1},
		promptRegex:  regexp.MustCompile(oldPrompt),
		promptString: "SSH@ICX7250-24 Router#",
		inConfigMode: true,
	}
}

// The hazard this path exists for: `hostname` changes the prompt, so the
// ordinary execute path keeps waiting for the old one until the timeout.
func TestExecuteInConfigModeHangsOnHostnameChange(t *testing.T) {
	stdin := &recordingStdin{}
	c := promptClient(feed("hostname lab-core\r\nSSH@lab-core Router(config)#", false), stdin)

	done := make(chan error, 1)
	go func() { done <- c.ExecuteInConfigMode([]string{"hostname lab-core"}) }()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "timeout") {
			t.Fatalf("expected a prompt timeout, got %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ExecuteInConfigMode did not return")
	}
}

// ExecutePromptChanging sends the command and adopts whatever prompt the
// switch prints next, so later commands match the new hostname.
func TestExecutePromptChangingRedetectsPrompt(t *testing.T) {
	stdin := &recordingStdin{}
	c := promptClient(feed("hostname lab-core\r\nSSH@lab-core Router(config)#", false), stdin)

	if err := c.ExecutePromptChanging("hostname lab-core"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := strings.Join(stdin.writes, ""); got != "hostname lab-core\n" {
		t.Errorf("sent %q, want the hostname command only", got)
	}
	if !c.promptRegex.MatchString("SSH@lab-core Router#") || !c.promptRegex.MatchString("SSH@lab-core Router(config-if-e1000-1/1/1)#") {
		t.Errorf("prompt regex %q does not match the new hostname's prompts", c.promptRegex)
	}
	if c.promptRegex.MatchString("SSH@ICX7250-24 Router#") {
		t.Errorf("prompt regex %q still matches the old prompt", c.promptRegex)
	}
	if !c.inConfigMode {
		t.Error("config mode must be preserved across a hostname change")
	}

	// A follow-up command (as WriteMemory would send) is read against the
	// new prompt and returns its output.
	c.byteCh = feed("write memory\r\nWrite startup-config done.\r\nSSH@lab-core Router#", false)
	out, err := c.execute("write memory")
	if err != nil {
		t.Fatalf("follow-up command failed: %v", err)
	}
	if !strings.Contains(out, "Write startup-config done.") {
		t.Errorf("follow-up output = %q", out)
	}
}

// Dotted hostnames are part of the prompt too.
func TestExecutePromptChangingDottedHostname(t *testing.T) {
	stdin := &recordingStdin{}
	c := promptClient(feed("hostname sw1.lab.example\r\nSSH@sw1.lab.example Router(config)#", false), stdin)

	if err := c.ExecutePromptChanging("hostname sw1.lab.example"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if c.promptString != "SSH@sw1.lab.example Router(config)#" {
		t.Errorf("promptString = %q", c.promptString)
	}
	if !c.promptRegex.MatchString("SSH@sw1.lab.example Router#") {
		t.Errorf("prompt regex %q does not match the dotted hostname", c.promptRegex)
	}
}

// A rejected hostname leaves the prompt as it was and surfaces the CLI error.
func TestExecutePromptChangingCLIError(t *testing.T) {
	stdin := &recordingStdin{}
	c := promptClient(feed("hostname \"bad name\"\r\nInvalid input -> \"bad name\"\r\nSSH@ICX7250-24 Router(config)#", false), stdin)

	err := c.ExecutePromptChanging(`hostname "bad name"`)
	if err == nil || !strings.Contains(err.Error(), "Invalid input") {
		t.Fatalf("expected the CLI error, got %v", err)
	}
	if !c.promptRegex.MatchString("SSH@ICX7250-24 Router#") {
		t.Errorf("prompt regex %q should still match the unchanged prompt", c.promptRegex)
	}
}

func TestExecutePromptChangingRejectsControlCharacters(t *testing.T) {
	stdin := &recordingStdin{}
	c := promptClient(feed("", false), stdin)
	if err := c.ExecutePromptChanging("hostname a\nno hostname"); err == nil || !strings.Contains(err.Error(), "control characters") {
		t.Fatalf("expected control-character rejection, got %v", err)
	}
	if len(stdin.writes) != 0 {
		t.Errorf("nothing must be sent, got %q", stdin.writes)
	}
}

// The whole batch — commands, end, write memory — goes out in a single write
// before any output is read, and each command's output is consumed in turn.
func TestExecuteExpectingDisconnectBatchesOneWrite(t *testing.T) {
	stdin := &recordingStdin{}
	output := "ip address 10.0.20.7 255.255.255.0\r\nSSH@ICX7250-24 Router(config)#" +
		"end\r\nSSH@ICX7250-24 Router#" +
		"write memory\r\nWrite startup-config done.\r\nSSH@ICX7250-24 Router#"
	c := promptClient(feed(output, false), stdin)

	if err := c.ExecuteExpectingDisconnect([]string{"ip address 10.0.20.7 255.255.255.0"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(stdin.writes) != 1 {
		t.Fatalf("expected exactly one write, got %d: %q", len(stdin.writes), stdin.writes)
	}
	if stdin.writes[0] != "ip address 10.0.20.7 255.255.255.0\nend\nwrite memory\n" {
		t.Errorf("batch = %q", stdin.writes[0])
	}
	if c.inConfigMode {
		t.Error("the batch ends with `end`, so the shell is back in EXEC mode")
	}
	if c.disconnected {
		t.Error("session survived; client must not be marked disconnected")
	}
}

// When the switch stops answering after the address change, the batch reports
// ErrSessionSevered and every later command fails fast instead of waiting
// out the read timeout.
func TestExecuteExpectingDisconnectSevered(t *testing.T) {
	stdin := &recordingStdin{}
	c := promptClient(feed("ip address 10.0.20.7 255.255.255.0\r\n", true), stdin)

	start := time.Now()
	err := c.ExecuteExpectingDisconnect([]string{"ip address 10.0.20.7 255.255.255.0"})
	if !errors.Is(err, ErrSessionSevered) {
		t.Fatalf("expected ErrSessionSevered, got %v", err)
	}
	if !c.disconnected {
		t.Error("client must be marked disconnected")
	}
	if len(stdin.writes) != 1 || !strings.HasSuffix(stdin.writes[0], "end\nwrite memory\n") {
		t.Errorf("save must have been queued in the same write, got %q", stdin.writes)
	}

	// Fast failure afterwards: no read, no timeout.
	for _, call := range []func() error{
		func() error { return c.WriteMemory() },
		func() error { return c.ExecuteInConfigMode([]string{"vlan 10"}) },
		func() error { _, err := c.Execute("show version"); return err },
		func() error { return c.ExecutePromptChanging("hostname x") },
		func() error { return c.ExecuteExpectingDisconnect([]string{"ip address 1.1.1.1 255.0.0.0"}) },
	} {
		if err := call(); !errors.Is(err, ErrSessionSevered) {
			t.Errorf("after severance: got %v, want ErrSessionSevered", err)
		}
	}
	if time.Since(start) > 3*time.Second {
		t.Error("severed client must fail fast, not wait for the read timeout")
	}
}

// A CLI error inside the batch is reported while the session is still up.
func TestExecuteExpectingDisconnectCLIError(t *testing.T) {
	stdin := &recordingStdin{}
	output := "ip address 10.0.20.7 255.255.255.0\r\nError - duplicate ip address\r\nSSH@ICX7250-24 Router(config)#"
	c := promptClient(feed(output, false), stdin)

	err := c.ExecuteExpectingDisconnect([]string{"ip address 10.0.20.7 255.255.255.0"})
	if err == nil || !strings.Contains(err.Error(), "duplicate ip address") || errors.Is(err, ErrSessionSevered) {
		t.Fatalf("expected the CLI error only, got %v", err)
	}
	if c.disconnected {
		t.Error("a CLI error is not a severed session")
	}
}

func TestExecuteExpectingDisconnectEmptyBatch(t *testing.T) {
	stdin := &recordingStdin{}
	c := promptClient(feed("", false), stdin)
	if err := c.ExecuteExpectingDisconnect(nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(stdin.writes) != 0 {
		t.Errorf("empty batch must send nothing, got %q", stdin.writes)
	}
}

// detectPrompt still works for the prompts seen on the fleet and tolerates
// a `>` user-mode prompt.
func TestReadNewPromptShapes(t *testing.T) {
	tests := []struct {
		printed string
		base    string
	}{
		{"\r\nSSH@ICX7250-24 Router#", "SSH@ICX7250-24 Router"},
		{"\r\nSSH@ICX7150-C10ZP Switch(config)#", "SSH@ICX7150-C10ZP Switch"},
		{"\r\nSSH@lab-icx-6430 Switch>", "SSH@lab-icx-6430 Switch"},
		{"\r\nSSH@sw1.lab Router(config-vlan-10)#", "SSH@sw1.lab Router"},
	}
	for _, tt := range tests {
		c := promptClient(feed(tt.printed, false), &recordingStdin{})
		if _, err := c.readNewPrompt(); err != nil {
			t.Errorf("%q: %v", tt.printed, err)
			continue
		}
		want := regexp.QuoteMeta(tt.base) + `\s*(?:\([^\)]*\))?[#>]\s*$`
		if c.promptRegex.String() != want {
			t.Errorf("%q: regex = %q, want %q", tt.printed, c.promptRegex, want)
		}
	}
}
