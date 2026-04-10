# terraform-provider-fastiron-icx

Terraform provider for managing Brocade/Ruckus ICX switches running FastIron firmware via SSH CLI.

Built for environments where the REST API is unavailable (e.g., FastIron 08.0.95). All communication happens over an interactive SSH shell session, parsing CLI output.

## Supported Hardware

- ICX 7250 series
- ICX 7150 series (including compact C12P and C10ZP models)
- FastIron firmware 08.0.95 (other versions may work but are untested)

## Quick Start

```hcl
terraform {
  required_providers {
    icx = {
      source  = "pgehres/fastiron-icx"
      version = "~> 0.1"
    }
  }
}

provider "icx" {
  host            = "10.0.1.1"
  username        = var.switch_username
  password        = var.switch_password
  enable_password = var.switch_password
}

resource "icx_vlan" "servers" {
  vlan_id       = 100
  name          = "servers"
  spanning_tree = true
  stp_priority  = 4096
}

resource "icx_interface_ethernet" "server1" {
  port          = "1/1/1"
  port_name     = "web-server-1"
  untagged_vlan = icx_vlan.servers.vlan_id
}
```

## Provider Configuration

| Attribute | Description | Required | Env Var |
|---|---|---|---|
| `host` | Switch IP or hostname | Yes | `FASTIRON_HOST` |
| `port` | SSH port (default: 22) | No | `FASTIRON_PORT` |
| `username` | SSH username | Yes | `FASTIRON_USERNAME` |
| `password` | SSH password | Yes | `FASTIRON_PASSWORD` |
| `enable_password` | Enable mode password | No | `FASTIRON_ENABLE_PASSWORD` |
| `timeout` | SSH timeout in seconds (default: 30) | No | |

### Multi-Switch Setup

Use provider aliases to manage multiple switches:

```hcl
provider "icx" {
  alias    = "core"
  host     = "10.0.1.1"
  username = var.username
  password = var.password
}

provider "icx" {
  alias    = "access"
  host     = "10.0.1.2"
  username = var.username
  password = var.password
}

resource "icx_vlan" "mgmt_core" {
  provider = icx.core
  vlan_id  = 102
  name     = "mgmt"
}
```

## Resources

### icx_vlan

Manages VLAN existence and properties. Port membership is managed on `icx_interface_ethernet`.

```hcl
resource "icx_vlan" "mgmt" {
  vlan_id           = 102
  name              = "mgmt"
  spanning_tree     = true
  stp_priority      = 4096
  router_interface  = 1
  multicast_passive = true
}
```

### icx_interface_ethernet

Manages ethernet interface configuration and VLAN membership.

```hcl
# Access port
resource "icx_interface_ethernet" "server1" {
  port          = "1/1/1"
  port_name     = "web-server"
  untagged_vlan = 102
  tagged_vlans  = [110, 120, 200]
}

# Trunk port — automatically tagged on all VLANs
resource "icx_interface_ethernet" "uplink" {
  port                    = "1/2/7"
  port_name               = "core trunk"
  spanning_tree_pt2pt_mac = true
  tag_all_vlans           = true
}
```

| Attribute | Description |
|---|---|
| `port` | Port identifier (`unit/module/port`) |
| `port_name` | Descriptive name (auto-quoted if contains spaces) |
| `untagged_vlan` | VLAN ID for untagged traffic |
| `tagged_vlans` | Set of VLAN IDs for tagged traffic |
| `tag_all_vlans` | Tag all VLANs on the switch (excluding `untagged_vlan` and VLAN 1) |
| `spanning_tree_pt2pt_mac` | Enable 802.1w admin-pt2pt-mac |
| `optical_monitor` | Enable optical monitoring (default: true) |
| `raw_config` | Additional CLI commands within the interface context |

### icx_interface_ve

Manages virtual ethernet interfaces.

```hcl
resource "icx_interface_ve" "mgmt" {
  ve_id      = 1
  ip_address = "10.0.1.1/24"
  depends_on = [icx_vlan.mgmt]  # VE requires router-interface on a VLAN first
}
```

### icx_user

Manages local user accounts. Passwords are write-only (cannot be read from the switch).

```hcl
resource "icx_user" "admin" {
  username = "admin"
  password = var.admin_password
}
```

### icx_aaa

Manages AAA authentication settings (singleton resource).

```hcl
resource "icx_aaa" "this" {
  web_server_auth    = "default local"
  login_auth         = "default local"
  enable_aaa_console = true
}
```

### icx_system

Manages global system settings (singleton resource).

```hcl
resource "icx_system" "this" {
  global_stp                 = true
  telnet_server              = false
  dhcp_client_disable        = true
  optical_monitor            = true
  optical_monitor_non_ruckus = true
  manager_disable            = true
  manager_port_list          = "987"
}
```

### icx_poe

Manages per-port Power over Ethernet settings.

```hcl
resource "icx_poe" "camera" {
  port        = "1/1/5"
  enabled     = true
  power_limit = 30000  # milliwatts
}
```

### icx_raw_config

Escape hatch for arbitrary CLI commands not covered by specific resources.

```hcl
resource "icx_raw_config" "snmp" {
  commands = [
    "snmp-server community public ro",
  ]
  destroy_commands = [
    "no snmp-server community public ro",
  ]
  expect_in_config = [
    "snmp-server community public ro",
  ]
}
```

## Data Sources

### icx_running_config

```hcl
data "icx_running_config" "this" {}
output "config" { value = data.icx_running_config.this.config }
```

### icx_stack_unit

```hcl
data "icx_stack_unit" "this" {
  unit_id = 1
}
```

## Importing Existing Resources

```bash
terraform import icx_vlan.mgmt 102
terraform import icx_interface_ethernet.server1 1/1/1
terraform import icx_interface_ve.mgmt 1
terraform import icx_user.admin admin
terraform import icx_system.this system
terraform import icx_aaa.this aaa
```

**Important**: After importing, run `terraform plan` and verify **zero changes** before applying. User passwords will always show a diff (write-only) — this is expected and safe.

## Building from Source

```bash
make build    # Build the binary
make install  # Install to local Terraform plugin directory
make test     # Run unit tests
make testacc  # Run acceptance tests (requires TF_ACC=1 and a live switch)
```

## License

Apache License 2.0 — see [LICENSE](LICENSE).
