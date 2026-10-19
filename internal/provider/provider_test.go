package provider

import (
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/pgehres/terraform-provider-fastiron-icx/internal/sshclient"
)

func summaries(diags diag.Diagnostics, severity diag.Severity) []string {
	var out []string
	for _, d := range diags {
		if d.Severity() == severity {
			out = append(out, d.Summary())
		}
	}
	return out
}

func hasSummary(diags diag.Diagnostics, severity diag.Severity, substr string) bool {
	for _, s := range summaries(diags, severity) {
		if strings.Contains(s, substr) {
			return true
		}
	}
	return false
}

func sshSettings() connectionSettings {
	return connectionSettings{Host: "10.0.0.1", Username: "admin", Password: "pw", HostKey: "SHA256:abc", Timeout: 30}
}

// The default connection — SSH, credentials, pinned host key — resolves
// without a single diagnostic, so nothing changes for existing users.
func TestResolveConnectionDefaultsAreQuiet(t *testing.T) {
	opts, diags := resolveConnection(sshSettings())
	if len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}
	if opts.Transport != sshclient.TransportSSH || opts.Port != 0 || opts.AllowEmptyCredentials {
		t.Errorf("opts = %+v", opts)
	}
}

func TestResolveConnectionErrors(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*connectionSettings)
		want   string
	}{
		{"no host", func(s *connectionSettings) { s.Host = "" }, "Missing host"},
		{"no username", func(s *connectionSettings) { s.Username = "" }, "Missing username"},
		{"no password", func(s *connectionSettings) { s.Password = "" }, "Missing password"},
		{"no host key", func(s *connectionSettings) { s.HostKey = "" }, "Missing SSH host key"},
		{"unknown transport", func(s *connectionSettings) { s.Transport = "serial" }, "Invalid transport"},
		{"telnet without ack", func(s *connectionSettings) { s.Transport = "telnet" }, "requires insecure_transport_ack"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := sshSettings()
			tt.mutate(&s)
			_, diags := resolveConnection(s)
			if !hasSummary(diags, diag.SeverityError, tt.want) {
				t.Errorf("errors = %v, want one containing %q", summaries(diags, diag.SeverityError), tt.want)
			}
		})
	}
}

// Empty credentials are an error by default and a warning with the opt-in;
// the warning names what is empty.
func TestResolveConnectionEmptyCredentials(t *testing.T) {
	s := sshSettings()
	s.Username, s.Password, s.AllowEmptyCredentials = "", "", true
	opts, diags := resolveConnection(s)
	if diags.HasError() {
		t.Fatalf("unexpected errors: %v", summaries(diags, diag.SeverityError))
	}
	if !hasSummary(diags, diag.SeverityWarning, "empty credentials") {
		t.Errorf("warnings = %v, want the empty-credentials warning", summaries(diags, diag.SeverityWarning))
	}
	if !opts.AllowEmptyCredentials {
		t.Error("AllowEmptyCredentials must reach the client options")
	}
	var detail string
	for _, d := range diags {
		if d.Severity() == diag.SeverityWarning {
			detail = d.Detail()
		}
	}
	if !strings.Contains(detail, "empty username, password, enable_password") {
		t.Errorf("warning detail = %q, want it to name the empty fields", detail)
	}
}

// Telnet with the acknowledgement connects, warns about plaintext on every
// run, and warns about SSH-only settings it ignores.
func TestResolveConnectionTelnet(t *testing.T) {
	s := sshSettings()
	s.Transport, s.InsecureTransportAck = "telnet", true
	s.LegacySSHAlgorithms = true
	opts, diags := resolveConnection(s)
	if diags.HasError() {
		t.Fatalf("unexpected errors: %v", summaries(diags, diag.SeverityError))
	}
	warnings := summaries(diags, diag.SeverityWarning)
	if !hasSummary(diags, diag.SeverityWarning, "Plaintext telnet") || !hasSummary(diags, diag.SeverityWarning, "SSH settings ignored") {
		t.Errorf("warnings = %v", warnings)
	}
	if opts.Transport != sshclient.TransportTelnet || opts.Port != 0 {
		t.Errorf("opts = %+v, want telnet with the default port", opts)
	}

	// Telnet needs no host key at all.
	s = connectionSettings{Host: "10.0.0.1", Username: "admin", Password: "pw", Transport: "telnet", InsecureTransportAck: true}
	_, diags = resolveConnection(s)
	if diags.HasError() {
		t.Errorf("telnet without a host key must not error: %v", summaries(diags, diag.SeverityError))
	}
	if hasSummary(diags, diag.SeverityWarning, "SSH settings ignored") {
		t.Error("no SSH settings were given, so none should be reported as ignored")
	}
}

// The competition case: telnet, no credentials at all.
func TestResolveConnectionCompetitionSwitch(t *testing.T) {
	s := connectionSettings{Host: "10.80.0.2", Transport: "telnet", InsecureTransportAck: true, AllowEmptyCredentials: true}
	opts, diags := resolveConnection(s)
	if diags.HasError() {
		t.Fatalf("unexpected errors: %v", summaries(diags, diag.SeverityError))
	}
	if got := summaries(diags, diag.SeverityWarning); len(got) != 2 {
		t.Errorf("want exactly the empty-credentials and plaintext warnings, got %v", got)
	}
	if opts.Username != "" || opts.Password != "" || opts.EnablePassword != "" || !opts.AllowEmptyCredentials || opts.Transport != "telnet" {
		t.Errorf("opts = %+v", opts)
	}
}

func TestResolveConnectionAckOverSSHWarns(t *testing.T) {
	s := sshSettings()
	s.InsecureTransportAck = true
	_, diags := resolveConnection(s)
	if diags.HasError() || !hasSummary(diags, diag.SeverityWarning, "no effect over SSH") {
		t.Errorf("diags = %v", diags)
	}
}
