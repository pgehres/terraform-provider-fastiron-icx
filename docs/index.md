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
- ICX 7150-C10ZP (10-port PoE compact, switch image, ISP VLANs)

## Router Image vs. Switch Image

Every FastIron release ships as two binaries per platform: the **switch image** (`SPS…`, version strings ending in codes like `T211`) and the **router image** (`SPR…`, codes like `T213`). The hardware is identical; the router image adds the Layer 3 plane. This distinction — not the switch model or series — determines which provider features work on a given device:

| Capability | Switch image | Router image |
|---|---|---|
| VLANs, port membership, port names, PoE | ✓ | ✓ |
| `icx_user`, `icx_aaa`, users/SSH management | ✓ | ✓ |
| `icx_ntp`, `icx_snmp`, `icx_logging`, port `enabled` | ✓ | ✓ |
| Management IP | One global `ip address` + `ip default-gateway` — [`icx_management_ip`](resources/management_ip.md) | VE interfaces + `ip route` — [`icx_interface_ve`](resources/interface_ve.md) |
| `icx_management_ip` | ✓ | ✗ (no global `ip address`) |
| `icx_interface_ve` / `router_interface` on `icx_vlan` | ✗ (commands do not exist) | ✓ |
| `global_stp` on `icx_system` | ✗ | ✓ |
| Inter-VLAN routing, static routes | ✗ | ✓ |

Identify the image from `show version` or the `ver` line at the top of the running config (e.g. `08.0.95rT211` = switch image, `08.0.95mT213` = router image).

-> **The provider detects the image type.** At connect time the provider reads the running config once and derives the image type, first from the version string's trailing T-code (`…T211` = switch image, `…T213` = router image) and, when that code is unrecognized, from structural signals (VE interfaces, `ip route`, a VLAN `router-interface`, or `global-stp` mean router image; a global `ip address`/`ip default-gateway` means switch image). Declaring a router-image-only resource or attribute against a detected switch-image device is then rejected at **plan time** with a clear **Router image required** error (and `icx_management_ip` against a detected router image with **Switch image required**), so `terraform plan` fails before any resource in the run is applied — instead of a mid-apply `Invalid input` CLI error after other changes have already landed. (The same check is repeated at apply time, before any commands are sent, for values that are only known during apply.) Devices whose image cannot be determined are never blocked; misconfigurations there degrade to the switch's own apply-time CLI error. The detected type is exposed as `image_type` (alongside `firmware_version`) on the [`icx_stack_unit`](data-sources/stack_unit.md) data source.

-> **Reimaging changes your Terraform config.** Moving a switch between images requires flashing the other binary and rebooting, and the management addressing does not translate: a switch-image device's global `ip address` must be rebuilt as an `icx_vlan` `router_interface` + `icx_interface_ve` (or vice versa). Plan this as a deliberate config migration: the provider does not rewrite your configuration. After a router→switch reimage, stale router-image state is cleared without errors — `icx_interface_ve` resources drop out of state on refresh (or destroy cleanly), and removing `router_interface`/`global_stp = true` from config applies without sending the non-existent `no` commands — but declaring the switch-image equivalents is up to you.

**Experimental models (read path verified on real hardware; resource operations not yet validated live):**
- ICX 6430-C12 (EOL, FastIron 08.0.30 switch image — L2 only, non-stackable) and ICX 6450-C12PD (08.0.30 router image). SSH connectivity and config parsing are verified against live devices — their SSH stacks offer only legacy key exchange, so `legacy_ssh_algorithms = true` is required (see below) — and the parser test fixtures are sanitized real captures (which, notably, do use the `stack unit` wrapper despite the models being non-stackable; tolerance for bare top-level `module` lines is retained and tested separately as robustness). The 08.0.30 trunk-untagged dialect — `dual-mode <vlan>` on the interface, which pre-08.0.80 FastIron requires in place of a VLAN-context `untagged` entry on a tagged port — is understood by `icx_interface_ethernet`: Read reports the dual-mode VLAN as `untagged_vlan`, and apply emits `dual-mode` only on firmware below 08.0.80 (see [the resource's dialect section](resources/interface_ethernet.md#fastiron-08030-dual-mode-dialect)). That command generation is unit-tested against the real captures but has **not** been run against a live 08.0.30 device yet, and the 6430's `no ip dhcp-client enable` DHCP-client spelling is likewise unit-tested only (see [Firmware dialects and capabilities](#firmware-dialects-and-capabilities)). Expect rough edges beyond read-only use, and validate on a non-production port first.

~> **Migration (ICX 6430 / FastIron 08.0.30 switch image): one-time `dhcp_client_disable` diff.** Earlier provider versions did not recognize the 6430's `no ip dhcp-client enable` line and read the DHCP client as enabled (`dhcp_client_disable = false`). Starting with this version it is reported correctly as disabled. If your `icx_system` for such a device leaves `dhcp_client_disable` unset (or `false`), the next `plan` shows `dhcp_client_disable: true -> false`, and applying it would send `ip dhcp-client enable` to a switch that uses a static management IP. **Set `dhcp_client_disable = true` to match before applying.** 08.0.9x devices and the 08.0.30 router image (ICX 6450) are unaffected.

## Firmware dialects and capabilities

Beyond the router/switch image split, FastIron's CLI differs between firmware trains. The provider detects the device's **dialect** — firmware release plus image type — once at connect time from the running config's `ver` line, and every resource consults that one capability table instead of carrying its own version checks. Where a feature is missing the provider refuses at **plan time**, before any command is sent. Where the syntax differs, it emits the spelling the device's firmware uses.

| Capability | 08.0.9x router | 08.0.9x switch | 08.0.30 router | 08.0.30 switch |
|---|---|---|---|---|
| `icx_interface_ve` | ✓ | ✗ | ✓ | ✗ |
| `router_interface` on `icx_vlan` | ✓ | ✗ | ✓ | ✗ |
| `global_stp` on `icx_system` | ✓ | ✗ | ✓ | ✗ |
| `icx_management_ip` (global `ip address` / `ip default-gateway` / `ip dns server-address`) | ✗ | ✓ | ✗ | ✓ |
| `hostname` on `icx_system` | ✓ | ✓ | ✓ | ✓ |
| `enabled` on `icx_interface_ethernet` (`disable`/`enable`) | ✓ | ✓ | ✓ | ✓ |
| `icx_ntp` (`ntp` block, `clock summer-time`) | ✓ | ✓ | ✓ | ✓ |
| `timezone` regions `gmt` / `us` on `icx_ntp` | ✓ | ✓ | ✓ | ✓ |
| `timezone` regions `europe` / `australia` on `icx_ntp` | ✓ | ✓ | ✗ | ✗ |
| `icx_snmp`, `icx_logging` | ✓ | ✓ | ✓ | ✓ |
| `manager_registrar` / `manager_disable` / `manager_port_list` on `icx_system` | ✓ | ✓ | ✗ | ✗ |
| `optical_monitor` / `optical_monitor_non_ruckus` on `icx_system` | ✓ | ✓ | ✓¹ | ✓¹ |
| Trunk port with an untagged VLAN | VLAN-context `untagged ethe` | VLAN-context `untagged ethe` | `dual-mode <vlan>` on the interface | `dual-mode <vlan>` on the interface |
| `dhcp_client_disable = true` | `ip dhcp-client disable` | `ip dhcp-client disable` | `ip dhcp-client disable` | `no ip dhcp-client enable` |

Reference devices: 08.0.9x router = ICX 7250-24 (`08.0.95mT213`), ICX 7150-24 and 7150-C12P (`08.0.95qT213`). 08.0.9x switch = ICX 7150-C10ZP (`08.0.95rT211`). 08.0.30 router = ICX 6450-C12PD (`08.0.30uT313`). 08.0.30 switch = ICX 6430-C12 (`08.0.30uT311`).

- **Version boundaries.** The "08.0.30" columns apply to every release **before 08.0.80**. That is the release that deprecated `dual-mode`, and SmartZone documents it as the minimum release for the `manager` commands. 08.0.80 and later, 09.x included, use the 08.0.9x column. The one earlier boundary is 08.0.50, which added the `europe` and `australia` regions to `clock timezone`. The DHCP-client spelling is the one difference that depends on both axes: the 08.0.30 router image already uses the 08.0.9x form.
- **Unknown never blocks.** If the version string cannot be parsed, every version-dependent feature is allowed and the 08.0.9x syntax is used. If the image cannot be detected, every image-dependent feature is allowed. A wrong guess then surfaces as the switch's own CLI error, never as a provider refusal. Existing `dual-mode` lines in the running config also count as proof of the 08.0.30 trunk syntax.
- **Hardware is out of scope.** PoE (`icx_poe`, `inline power`), optic support, and port layout depend on the chassis and modules. The firmware dialect does not determine them, so they are not capabilities and remain apply-time concerns for the switch.
- ¹ `optical-monitor` exists on every known firmware train. Whether a particular platform or optic reports DOM data is a hardware question, so the provider does not gate it. The per-interface `optical_monitor` on `icx_interface_ethernet` is not routed through the capability table at all.

-> **Migration note:** on FastIron 08.0.30 switch-image devices (ICX 6430) the DHCP client is reported correctly as disabled starting with this version. Set `dhcp_client_disable = true` to match before applying; see the [migration warning above](#router-image-vs-switch-image) for why. 08.0.9x and 08.0.30 router-image devices need no change.

Universal settings — `hostname` on `icx_system`, the switch-image management addressing where it applies, port admin state (`enabled` on `icx_interface_ethernet`) and the [`icx_ntp`](resources/ntp.md), [`icx_snmp`](resources/snmp.md) and [`icx_logging`](resources/logging.md) singletons — are available on every supported dialect; all are unit-tested against the six captures (the 6430 capture is the reference for the `ntp`/`clock` syntax) but have not yet been applied to any live device, on any firmware.

A capability error names the attribute and the reason. For example, `manager_registrar = true` against an ICX 6430 fails `terraform plan` with: **Not supported by this firmware**: "The manager_registrar attribute of icx_system is not available on FastIron 08.0.30 (firmware 08.0.30uT311); the manager commands require FastIron 08.0.80 or later." Router-image features keep the **Router image required** error described above.

## SSH and Host Key Verification

**Host key verification is required** unless you explicitly opt out. One of `host_key` or `insecure_skip_host_key_verify` must be set.

To obtain the switch's host key in `known_hosts` format:

```shell
ssh-keyscan <switch-hostname-or-ip>
```

Copy the relevant line (e.g. the `ssh-rsa` or `ecdsa-sha2-nistp256` entry) and set it as `host_key`.

~> **If `ssh-keyscan` prints only banner comments and no keys**, the switch's SSH stack (e.g. RomSShell 5.40 on FastIron 08.0.x) offers key-exchange algorithms too old for modern OpenSSH to negotiate. The provider's own SSH client handles these switches fine — use the debug tool from the provider repository instead, which prints the key in both accepted formats:

```shell
FASTIRON_HOST=<switch> go run ./cmd/debug-ssh -print-host-key
```

### Legacy SSH Algorithms (EOL devices)

Very old FastIron releases — e.g. **FastIron 08.0.30 on the EOL ICX 6430** — only support SSH algorithms (SHA-1 key exchange, CBC ciphers, `ssh-rsa` host key signatures) that modern SSH clients, including the Go `x/crypto` library this provider uses, disable by default. If the provider fails to connect with a key-exchange or cipher negotiation error, set:

```terraform
provider "icx" {
  # ...
  legacy_ssh_algorithms = true
}
```

This *extends* the offered algorithm lists with `diffie-hellman-group14-sha1`, `diffie-hellman-group1-sha1`, `diffie-hellman-group-exchange-sha1`, `aes128-cbc`, `3des-cbc`, `ssh-rsa`, and `ssh-dss`. Modern algorithms remain first in the preference order, so a switch that supports anything newer still negotiates it — but these legacy algorithms are cryptographically weak, so enable this only for EOL devices on trusted networks. RC4 is never offered.

The `debug-ssh -print-host-key` host-key capture always offers the legacy algorithms (nothing secret is exchanged during a key capture), so it works against these devices without extra flags.

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
| `host_key` | string | see note | `FASTIRON_HOST_KEY` | The switch's SSH host public key in `known_hosts` format or `SHA256:<fingerprint>`. Obtain with `ssh-keyscan <host>` (see the host-key note above if it prints nothing). One of `host_key` or `insecure_skip_host_key_verify` is required. |
| `insecure_skip_host_key_verify` | bool | see note | — | Disable SSH host key verification. Exposes connections to man-in-the-middle attacks. For lab use only. |
| `legacy_ssh_algorithms` | bool | no | — | Additionally offer weak legacy SSH algorithms required by very old FastIron SSH stacks (e.g. 08.0.30 on the EOL ICX 6430). Defaults to `false`. |

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

Obtain the value with `ssh-keyscan <host>`, or with the debug tool described in [SSH and Host Key Verification](#ssh-and-host-key-verification) if `ssh-keyscan` prints nothing. One of `host_key` or `insecure_skip_host_key_verify` must be set. Can also be set with the `FASTIRON_HOST_KEY` environment variable.

#### `insecure_skip_host_key_verify` (Optional)

When `true`, host key verification is disabled. This means an attacker performing a man-in-the-middle attack could intercept switch credentials and configuration commands. **Do not use in production.** One of `host_key` or `insecure_skip_host_key_verify` must be set.

#### `legacy_ssh_algorithms` (Optional)

When `true`, the provider additionally offers legacy SSH algorithms — `diffie-hellman-group14-sha1`, `diffie-hellman-group1-sha1`, and `diffie-hellman-group-exchange-sha1` key exchange; `aes128-cbc` and `3des-cbc` ciphers; `ssh-rsa` and `ssh-dss` host key signatures — needed by very old FastIron SSH stacks such as FastIron 08.0.30 on the EOL ICX 6430. Modern algorithms remain preferred, so switches that support them are unaffected. **These algorithms are cryptographically weak** (SHA-1 key exchange, CBC-mode ciphers, SHA-1 signatures); enable this only for EOL devices on trusted networks. RC4 is never offered. Defaults to `false`. See [Legacy SSH Algorithms](#legacy-ssh-algorithms-eol-devices).
