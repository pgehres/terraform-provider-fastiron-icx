---
page_title: "icx_poe Resource - fastiron-icx"
description: |-
  Manages Power over Ethernet (PoE) settings on an ICX switch port.
---

# icx_poe (Resource)

Manages Power over Ethernet (inline power) settings for a single port on an ICX PoE switch.

The provider reads the admin state from `show inline power <port>` and the option from the port's running configuration, so a change made on the switch shows as a plan diff. The admin state comes from `show inline power` because the default differs by model: an ICX 7450 port with no ` inline power` line is powered, while the ICX 7150 fixtures carry an explicit ` inline power` line.

~> **One option per port.** On FastIron 09.0.10 a port holds at most one ` inline power` option, and a new `inline power <option>` replaces the previous one. `power_limit` and `priority` therefore cannot both be set, and neither can be set with `enabled = false`. To remove an option, the provider sends a plain `inline power`. It never sends `no inline power <option>`, because on 09.0.10 that turns PoE off on the port.

~> **Hardware requirement:** This resource is only meaningful on PoE-capable ports. Applying it to a non-PoE port will produce a CLI error on the switch.

## Example Usage

```terraform
# Enable PoE with a 15400 mW (15.4 W) power limit (802.3af)
resource "icx_poe" "ap_port" {
  port        = "1/1/5"
  enabled     = true
  power_limit = 15400
}

# Highest priority when the PoE budget runs short
resource "icx_poe" "uplink_switch" {
  port     = "1/1/1"
  priority = 1
}

# Disable PoE on a port
resource "icx_poe" "disabled_port" {
  port    = "1/1/12"
  enabled = false
}
```

## Argument Reference

### Required

- `port` (String) - Port identifier in `unit/module/port` format (e.g. `"1/1/1"`). Must be a PoE-capable port.

### Optional

- `enabled` (Boolean) - Enable inline power (PoE) on this port. Defaults to `true`.
- `power_limit` (Number) - Power limit in milliwatts (`inline power power-limit`). If not set, the switch default applies. Conflicts with `priority`; requires `enabled = true`.
- `priority` (Number) - PoE priority class for power management (`inline power priority`): `1` (highest) to `3` (lowest, the switch default). Conflicts with `power_limit`; requires `enabled = true`.

### Read-Only

- `id` (String) - Terraform resource identifier (port identifier).

## Import

Import by port identifier. The first read records the switch's current state:

```shell
terraform import icx_poe.ap_port 1/1/5
```
