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

### icx_management_ip

Manages the global management IP, default gateway and DNS servers of a **switch-image** device (singleton; router-image devices use `icx_interface_ve`). Changing `ip_address` severs the provider's own SSH session and is refused unless `allow_management_ip_change = true`; destroying the resource sends no commands. See [docs/resources/management_ip.md](docs/resources/management_ip.md) before changing an address.

```hcl
resource "icx_management_ip" "this" {
  ip_address      = "10.0.20.5/24"
  default_gateway = "10.0.20.1"
  dns_servers     = ["10.0.20.1"]
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

### icx_ntp

Singleton: NTP servers, client-only mode, time zone and summer time.

```hcl
resource "icx_ntp" "this" {
  servers     = ["216.239.35.0", "216.239.35.4"]
  serve       = false
  timezone    = "us pacific"
  summer_time = true
}
```

### icx_snmp

Singleton: SNMP community strings (write-only, Sensitive, redacted from errors), contact and location.

```hcl
resource "icx_snmp" "this" {
  communities = [{ string = var.snmp_ro, mode = "ro", acl = "snmp-managers" }]
  contact     = "Network Operations"
  location    = "Rack 12"
}
```

### icx_logging

Singleton: syslog hosts, local buffer size, disabled buffered levels, facility.

```hcl
resource "icx_logging" "this" {
  hosts            = ["10.0.20.50"]
  buffered_entries = 1000
  disabled_levels  = ["debugging"]
  facility         = "local7"
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
terraform import icx_management_ip.this management
terraform import icx_user.admin admin
terraform import icx_system.this system
terraform import icx_aaa.this aaa
terraform import icx_ntp.this ntp
terraform import icx_snmp.this snmp
terraform import icx_logging.this logging
```

**Important**: After importing, run `terraform plan` and verify **zero changes** before applying. User passwords will always show a diff (write-only) — this is expected and safe.

## Building from Source

```bash
make build    # Build the binary
make install  # Install to local Terraform plugin directory
make test     # Run unit tests
make testacc  # Run acceptance tests against a live lab switch (see below)
```

## Acceptance Tests

`internal/acctest` runs the provider's resources against a **live lab switch** through [terraform-plugin-testing](https://github.com/hashicorp/terraform-plugin-testing). Every test skips unless `TF_ACC` and the connection variables are set, so `go test ./...` never needs a switch. Each test snapshots the running configuration first, checks that the scratch resources it will use are free, creates / updates / destroys through real `terraform` runs (a step fails if its post-apply plan is not empty), verifies every step on the device over its own SSH session, and finally requires the running configuration to be **identical to the pre-test snapshot**.

```bash
FASTIRON_HOST=192.168.2.240 \
FASTIRON_USERNAME=pgehres \
FASTIRON_PASSWORD='...' \
FASTIRON_ENABLE_PASSWORD='...' \
FASTIRON_HOST_KEY='SHA256:...' \
FASTIRON_LEGACY_SSH=true \
TF_ACC_SCRATCH_PORT=1/1/11 \
make testacc
```

| Variable | Required | Meaning |
|---|---|---|
| `FASTIRON_HOST`, `FASTIRON_USERNAME`, `FASTIRON_PASSWORD` | yes | The provider's own connection variables (`FASTIRON_PORT` optional). |
| `FASTIRON_ENABLE_PASSWORD` | if the switch prompts for it | Enable password. |
| `FASTIRON_HOST_KEY` | yes | Host key or `SHA256:` fingerprint; the harness never skips verification. Capture it with `go run ./cmd/debug-ssh -print-host-key`. |
| `FASTIRON_LEGACY_SSH` | no | `true` for FastIron 08.0.30 devices (ICX 6430/6450) — sets `legacy_ssh_algorithms`. |
| `TF_ACC_SCRATCH_PORT` | for the interface test | The **one** port the tests may configure, e.g. `1/1/11`. Must be unconfigured, in no VLAN and have no link. |
| `TF_ACC_SCRATCH_VLAN`, `TF_ACC_SCRATCH_VLAN_2` | no | VLAN IDs that must not exist on the device (defaults 3999 and 3998). |
| `TF_ACC_ALLOW_TRUNK_RECONCILE` | no | See below. |

Narrow the run with `make testacc TESTARGS='-run TestAccVLAN'`. A `terraform` binary on `PATH` is used (or `TF_ACC_TERRAFORM_PATH`); otherwise the framework downloads one. `TF_LOG=DEBUG` shows the CLI exchange.

**Pre-flight checklist**

1. The scratch port has nothing connected and shows `Down` in `show interfaces brief`, has no `interface ethernet` stanza and is in no VLAN besides the implicit default. The harness checks all of this and fails rather than run otherwise.
2. The scratch VLAN IDs do not exist on the device (never VLAN 1, never the management VLAN).
3. No local user `tfacc-test` exists, and the login user is not `tfacc-test`.
4. The switch allows at least two concurrent SSH sessions (the provider's and the harness's).
5. You have console access, in case the hostname test is interrupted (it restores the original name in cleanup even on failure, but a dead network would stop it).

**What each test touches and leaves behind**

| Test | Does | Never |
|---|---|---|
| `TestAccVLAN` | Creates the scratch VLAN with a name and spanning tree, renames it, imports it, deletes it. | Touches an existing VLAN. |
| `TestAccInterfaceEthernet` | Puts the scratch port through access → tagged-only → trunk-with-untagged → access using both scratch VLANs, then destroys everything. On FastIron below 08.0.80 the trunk-with-untagged step exercises the `dual-mode` dialect; the log says which form was seen. | Configures any other port. |
| `TestAccUser` | Creates `tfacc-test`, changes its password, imports it, deletes it. | Touches the login user. |
| `TestAccSystemHostname` | Sets the hostname to `<original>-tfacc` through `icx_system` and back, exercising SSH prompt re-detection live; the resource is then *forgotten* with a `removed` block (Terraform ≥ 1.7), not destroyed, because destroying `icx_system` would reset every setting it owns. Every other `icx_system` attribute is rendered with the device's current value, so the commands sent for them are idempotent re-issues. If the device runs its factory-default name the provider cannot restore it (it never sends `no hostname`), so the harness does that itself. | Leaves a changed hostname behind: `t.Cleanup` restores it even when the test fails. |
| `TestAccManagementIP` | Switch-image devices only (router image skips). Appends `192.0.2.53` to the device's DNS servers through `icx_management_ip` and restores the original list. | Sends `ip address` or `ip default-gateway`. Changing the address or gateway — including whether FastIron accepts an off-subnet gateway — is a **manual** lab test with console access. |

**Trunk-all ports.** `icx_vlan.Create` tags a new VLAN onto every port it recognises as a "tag all VLANs" trunk (CLAUDE.md gotcha #8). On a device with such trunks the VLAN-creating tests would therefore configure ports other than the scratch port, so they **skip** and name the ports. Set `TF_ACC_ALLOW_TRUNK_RECONCILE=true` to allow it: the scratch VLAN's membership is removed again with the VLAN and nothing about an existing VLAN on those trunks changes.

Every provider operation ends in `write memory`, so a full run writes the startup configuration a couple of dozen times. Tests run one at a time (one switch, one config session); do not run the package with `-parallel` against the same device.

## License

Apache License 2.0 — see [LICENSE](LICENSE).
