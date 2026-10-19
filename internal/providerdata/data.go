package providerdata

import (
	"github.com/pgehres/terraform-provider-fastiron-icx/internal/dialect"
	"github.com/pgehres/terraform-provider-fastiron-icx/internal/sshclient"
)

// ProviderData holds the SSH client, shared with all resources and data sources.
type ProviderData struct {
	Client sshclient.CommandExecutor
	// Username is the account the provider is authenticated as, so resources
	// can refuse operations that would sever the active session.
	Username string
	// Dialect is the device's firmware dialect (release + image type),
	// detected once at provider Configure time. Resources consult it — and
	// only it — for firmware- or image-dependent behaviour, so they can refuse
	// unsupported operations before any commands are sent and render
	// dialect-specific syntax. The zero value is an unknown, permissive dialect.
	Dialect dialect.Dialect
}
