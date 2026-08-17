---
page_title: "icx_vlan Resource - fastiron-icx"
description: |-
  Manages a VLAN on an ICX switch. This resource controls VLAN existence and properties only — port membership is managed exclusively by icx_interface_ethernet.
---

# icx_vlan (Resource)

Manages a VLAN on an ICX switch.

~> **Important: No port membership here.** `icx_vlan` manages VLAN properties (name, STP, multicast, router-interface) **only**. It does NOT manage which ports are members of the VLAN. Port membership is managed exclusively by [`icx_interface_ethernet`](interface_ethernet.md). This separation prevents conflicting state that caused an outage during early development.

~> **tag_all_vlans drift caveat (v0.1.1+).** When you create a new VLAN and some ports have `tag_all_vlans = true`, the Create path automatically tags the new VLAN onto those ports by inspecting the running config. However, `Read` does not reconcile tagged VLAN membership when `tag_all_vlans = true`, so any drift that occurs after the initial apply (manual `no tagged ethe`, VLANs predating Terraform management, etc.) stays invisible to `terraform plan`. If auditable VLAN membership is required, use an explicit `tagged_vlans` list on `icx_interface_ethernet` instead. Requires **>= v0.1.1**; on v0.1.0 there is no create-time reconciliation at all.

## Example Usage

```terraform
resource "icx_vlan" "servers" {
  vlan_id = 100
  name    = "servers"
}

resource "icx_vlan" "management" {
  vlan_id          = 10
  name             = "management"
  router_interface = 10
  spanning_tree    = true
  stp_priority     = 4096
}

resource "icx_vlan" "multicast_vlan" {
  vlan_id           = 200
  name              = "multicast"
  multicast_passive = true
  multicast_version = 2
}
```

## Argument Reference

### Required

- `vlan_id` (Number) - VLAN ID (1–4094).

### Optional

- `name` (String) - VLAN name (1–31 chars; no spaces or quote characters). Once set, the name cannot be removed in place — FastIron has no CLI command to clear it — so clearing this attribute produces an error; destroy and recreate the VLAN instead.
- `router_interface` (Number) - VE interface number to associate with this VLAN (generates `router-interface ve N`). The VE interface configuration is managed separately by [`icx_interface_ve`](interface_ve.md).
- `spanning_tree` (Boolean) - Enable 802.1w spanning tree on this VLAN. Defaults to `false`.
- `stp_priority` (Number) - Spanning tree priority for this VLAN (e.g. `4096`). Only meaningful when `spanning_tree = true`.
- `multicast_passive` (Boolean) - Enable IGMP snooping in passive mode on this VLAN. Defaults to `false`.
- `multicast_version` (Number) - IGMP version (2 or 3). Only meaningful when `multicast_passive = true`.
- `raw_config` (List of String) - Additional raw CLI commands to execute within the VLAN context (`vlan N ... exit`). On destroy, each command is prefixed with `no`. See [raw_config escape hatch](#raw_config-escape-hatch) below.

### Read-Only

- `id` (String) - Terraform resource identifier (VLAN ID as string).

## raw_config Escape Hatch

The `raw_config` list sends commands verbatim inside the VLAN configuration context. On destroy, the provider prepends `no` to each command. This works correctly for leaf commands (`ip multicast boundary 10`) but **fails** for context-entering commands (`router ospf`, `interface ve N`). For those, use a dedicated `icx_raw_config` resource with explicit `destroy_commands`.

## VLAN 1 Behavior

VLAN 1 (the default VLAN) cannot be deleted. If you destroy an `icx_vlan` resource for VLAN 1, the provider resets it to defaults instead of issuing `no vlan 1`.

## Import

Import by VLAN ID:

```shell
terraform import icx_vlan.servers 100
```

~> **After import:** Run `terraform plan` to review any detected drift. If `raw_config` was set, it will show a diff because raw config lines are not read back from the switch.
