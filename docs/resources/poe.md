---
page_title: "icx_poe Resource - fastiron-icx"
description: |-
  Manages Power over Ethernet (PoE) settings on an ICX switch port.
---

# icx_poe (Resource)

Manages Power over Ethernet (inline power) settings for a single port on an ICX PoE switch.

~> **Read limitation:** PoE state is not currently read back from `show inline power` output. The provider preserves the last-known state between applies. If the PoE state is changed out-of-band, `terraform plan` will not detect it.

~> **Hardware requirement:** This resource is only meaningful on PoE-capable ports. Applying it to a non-PoE port will produce a CLI error on the switch.

## Example Usage

```terraform
# Enable PoE with a 15400 mW (15.4 W) power limit (802.3af)
resource "icx_poe" "ap_port" {
  port        = "1/1/5"
  enabled     = true
  power_limit = 15400
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
- `power_limit` (Number) - Power limit in milliwatts. If not set, the switch default applies.

### Read-Only

- `id` (String) - Terraform resource identifier (port identifier).

## Import

Import by port identifier:

```shell
terraform import icx_poe.ap_port 1/1/5
```
