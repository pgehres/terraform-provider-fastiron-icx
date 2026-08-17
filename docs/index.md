---
page_title: "Provider: fastiron-icx"
description: |-
  The fastiron-icx provider manages Brocade/Ruckus ICX switches running FastIron firmware via SSH CLI.
---

# fastiron-icx Provider

The `fastiron-icx` provider manages Brocade/Ruckus ICX switches running FastIron firmware. It communicates with each switch over an SSH CLI session — no REST API or NETCONF is required or available on these firmware versions.

**Tested firmware:** FastIron 08.0.95

**Tested switch models:**
- ICX 7250-24 (2 modules, SFP+)
- ICX 7150-24 (3 modules including SFP+)
- ICX 7150-C12P (12-port PoE compact, optical-monitor)
- ICX 7150-C10ZP (10-port PoE compact, no global-stp, ISP VLANs)

## SSH and Host Key Verification

**Host key verification is required** unless you explicitly opt out. One of `host_key` or `insecure_skip_host_key_verify` must be set.

To obtain the switch's host key in `known_hosts` format:

```shell
ssh-keyscan <switch-hostname-or-ip>
```

Copy the relevant line (e.g. the `ssh-rsa` or `ecdsa-sha2-nistp256` entry) and set it as `host_key`.

## Example Usage

```terraform
terraform {
  required_providers {
    icx = {
      source  = "pgehres/fastiron-icx"
      version = "~> 0.1"
    }
  }
}

# Single switch
provider "icx" {
  host            = "192.168.1.1"
  username        = "admin"
  password        = var.switch_password
  enable_password = var.enable_password
  host_key        = "192.168.1.1 ssh-rsa AAAAB3NzaC1yc2EAAAA..."
}

resource "icx_vlan" "servers" {
  vlan_id = 100
  name    = "servers"
}
```

### Multi-Switch Usage with Provider Aliases

```terraform
provider "icx" {
  alias           = "core"
  host            = "10.0.0.1"
  username        = "admin"
  password        = var.core_password
  enable_password = var.enable_password
  host_key        = "10.0.0.1 ssh-rsa AAAA..."
}

provider "icx" {
  alias           = "access"
  host            = "10.0.0.2"
  username        = "admin"
  password        = var.access_password
  enable_password = var.enable_password
  host_key        = "10.0.0.2 ssh-rsa AAAA..."
}

resource "icx_vlan" "servers" {
  provider = icx.core
  vlan_id  = 100
  name     = "servers"
}

resource "icx_vlan" "servers_access" {
  provider = icx.access
  vlan_id  = 100
  name     = "servers"
}
```

## Provider Configuration Reference

~> **Note:** All attributes can be set via environment variables (see table below). Credentials set in the provider block take precedence over environment variables.

| Argument | Type | Required | Environment Variable | Description |
|---|---|---|---|---|
| `host` | string | yes | `FASTIRON_HOST` | Hostname or IP address of the switch. |
| `port` | number | no | `FASTIRON_PORT` | SSH port. Defaults to 22. |
| `username` | string | yes | `FASTIRON_USERNAME` | SSH username. |
| `password` | string | yes | `FASTIRON_PASSWORD` | SSH password. Sensitive. |
| `enable_password` | string | no | `FASTIRON_ENABLE_PASSWORD` | Enable mode password. Required if the switch prompts for one; safe to set even when the switch has no enable password configured. Sensitive. |
| `timeout` | number | no | — | SSH connection timeout in seconds. Defaults to 30. |
| `host_key` | string | see note | `FASTIRON_HOST_KEY` | The switch's SSH host public key in `known_hosts` format or `SHA256:<fingerprint>`. Obtain with `ssh-keyscan <host>`. One of `host_key` or `insecure_skip_host_key_verify` is required. |
| `insecure_skip_host_key_verify` | bool | see note | — | Disable SSH host key verification. Exposes connections to man-in-the-middle attacks. For lab use only. |

### Argument Reference

#### `host` (Required)

Hostname or IP address of the ICX switch. Can also be set with the `FASTIRON_HOST` environment variable.

#### `port` (Optional)

SSH port number. Defaults to `22`. Can also be set with the `FASTIRON_PORT` environment variable.

#### `username` (Required)

SSH username. Can also be set with the `FASTIRON_USERNAME` environment variable.

#### `password` (Required, Sensitive)

SSH password. Can also be set with the `FASTIRON_PASSWORD` environment variable.

#### `enable_password` (Optional, Sensitive)

Enable mode password. The provider enters enable mode only when the switch lands in user mode (a `>` prompt) after login; sessions that start in privileged EXEC (`#`) skip it entirely. If the switch has no enable password configured, the provider handles the "No password has been assigned yet" response gracefully — set this attribute anyway (to any value) to satisfy the configuration model, or leave it unset if the switch has no enable authentication. Can also be set with the `FASTIRON_ENABLE_PASSWORD` environment variable.

#### `timeout` (Optional)

SSH connection and command timeout in seconds. Defaults to `30`.

#### `host_key` (Optional)

The switch's SSH host public key used to verify the server's identity. Accepts either:
- A full `known_hosts`-format line (e.g. `192.168.1.1 ssh-rsa AAAAB3Nza...`)
- A SHA256 fingerprint (e.g. `SHA256:abc123...`)

Obtain the value with `ssh-keyscan <host>`. One of `host_key` or `insecure_skip_host_key_verify` must be set. Can also be set with the `FASTIRON_HOST_KEY` environment variable.

#### `insecure_skip_host_key_verify` (Optional)

When `true`, host key verification is disabled. This means an attacker performing a man-in-the-middle attack could intercept switch credentials and configuration commands. **Do not use in production.** One of `host_key` or `insecure_skip_host_key_verify` must be set.
