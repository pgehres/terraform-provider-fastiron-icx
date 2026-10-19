# Switch-image devices only (e.g. ICX 7150-C10ZP, ICX 6430). Router-image
# devices address management on a VE interface: see icx_interface_ve.
#
# With values matching the device this plans and applies with zero commands.
# Changing ip_address severs the provider's SSH session and is refused unless
# allow_management_ip_change = true; read docs/resources/management_ip.md first.
resource "icx_management_ip" "this" {
  ip_address      = "10.0.20.5/24"
  default_gateway = "10.0.20.1"
  dns_servers     = ["10.0.20.1"]
}
