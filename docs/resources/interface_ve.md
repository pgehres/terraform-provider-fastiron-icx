---
page_title: "icx_interface_ve Resource - fastiron-icx"
description: |-
  Manages a virtual ethernet (VE) interface on an ICX switch. The VE must first be created by a VLAN's router_interface attribute.
---

# icx_interface_ve (Resource)

Manages a virtual ethernet (VE) interface on an ICX switch.

~> **Prerequisite:** The VE interface must be created by setting `router_interface` on the corresponding [`icx_vlan`](vlan.md) resource. `icx_interface_ve` only configures properties of an already-existing VE — it does not create the VE itself.

## Example Usage

```terraform
resource "icx_vlan" "management" {
  vlan_id          = 10
  name             = "management"
  router_interface = 10
}

resource "icx_interface_ve" "management" {
  depends_on = [icx_vlan.management]

  ve_id      = 10
  ip_address = "10.0.10.1/24"
}
```

## Argument Reference

### Required

- `ve_id` (Number) - VE interface number. Must match the `router_interface` value on the associated `icx_vlan`.

### Optional

- `ip_address` (String) - IP address in CIDR notation (e.g. `"10.0.1.1/24"`). The provider converts this to the `address mask` format required by FastIron CLI (`ip address 10.0.1.1 255.255.255.0`).
- `raw_config` (List of String) - Additional raw CLI commands within the VE interface context (`interface ve N ... exit`). On destroy each command is prefixed with `no`.

### Read-Only

- `id` (String) - Terraform resource identifier (VE ID as string).

## Import

Import by VE ID:

```shell
terraform import icx_interface_ve.management 10
```
