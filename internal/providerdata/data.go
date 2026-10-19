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
	// NativeBulkTagging is the provider's experimental, default-off
	// native_bulk_tagging setting: whether icx_interface_ethernet may spell a
	// tag_all_vlans apply as `vlan-config add all-tagged` (tagged-only,
	// non-management trunks on FastIron 08.0.50+ only). When false every
	// command stream is the per-VLAN expansion.
	NativeBulkTagging bool
	// Ports records the ethernet ports claimed by icx_interface_ethernet
	// resources during the current operation, so a port declared by two
	// resources is rejected at plan time. See PortRegistry.
	Ports *PortRegistry
}
