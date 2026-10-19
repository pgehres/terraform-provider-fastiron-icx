package providerdata

import (
	"github.com/pgehres/terraform-provider-fastiron-icx/internal/parser"
	"github.com/pgehres/terraform-provider-fastiron-icx/internal/sshclient"
)

// ProviderData holds the SSH client, shared with all resources and data sources.
type ProviderData struct {
	Client sshclient.CommandExecutor
	// Username is the account the provider is authenticated as, so resources
	// can refuse operations that would sever the active session.
	Username string
	// FirmwareVersion and ImageType are read once at provider Configure time
	// so resources can refuse router-image-only operations on switch-image
	// devices before any commands are sent.
	FirmwareVersion string
	ImageType       parser.ImageType
}
