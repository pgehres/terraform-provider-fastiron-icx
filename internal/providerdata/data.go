package providerdata

import "github.com/pgehres/terraform-provider-fastiron-icx/internal/sshclient"

// ProviderData holds the SSH client, shared with all resources and data sources.
type ProviderData struct {
	Client sshclient.CommandExecutor
}
