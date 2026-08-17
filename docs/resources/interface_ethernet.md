---
page_title: "icx_interface_ethernet Resource - fastiron-icx"
description: |-
  Manages an ethernet interface on an ICX switch, including VLAN membership. This is the sole resource that controls which VLANs a port belongs to.
---

# icx_interface_ethernet (Resource)

Manages an ethernet interface on an ICX switch, including port-level configuration and VLAN membership.

~> **This is the only resource that manages port VLAN membership.** `icx_vlan` does not have tagged or untagged port attributes. Every port that needs VLAN membership must be declared with this resource.

~> **NEVER use `terraform apply -replace` on a live trunk.** Replacing an `icx_interface_ethernet` resource destroys it first — the destroy path strips **all** VLAN membership from the port before the create path re-adds it. On a switch's own uplink or management trunk, this removes the management VLAN mid-apply, cuts the SSH session, and leaves the Create half unexecuted. The switch becomes reachable only via console. Instead of replacing: upgrade to >= v0.1.1 and let `icx_vlan.Create` reconcile tag-all ports, convert the resource to an explicit `tagged_vlans` list in-place (additive), or add the one membership by hand.

~> **Never manage the same port with two resources.** Two `icx_interface_ethernet` resources for the same port make each apply clobber the other — Terraform never converges and `plan` always shows pending changes. Remove the duplicate with `terraform state rm` **before** deleting it from config, so the destroy does not strip membership the remaining resource still owns.

## Example Usage

### Access port (untagged)

```terraform
resource "icx_interface_ethernet" "access_port" {
  port          = "1/1/5"
  port_name     = "workstation-desk-5"
  untagged_vlan = 100
}
```

### Explicit trunk (tagged list)

Using an explicit `tagged_vlans` list is the safest approach: Read populates it from the device, so drift surfaces as a normal plan diff. The trade-off is that every VLAN must be listed — omitting one removes it from the trunk.

```terraform
resource "icx_interface_ethernet" "uplink" {
  port         = "1/1/24"
  port_name    = "uplink-to-core"
  tagged_vlans = [10, 100, 200, 300]
}
```

### tag_all_vlans trunk

`tag_all_vlans = true` is a Terraform-only convenience that expands to all non-default VLANs on the switch at apply time. It cannot be read back from the switch; `Read` preserves whatever the state contains. See [tag_all_vlans semantics](#tag_all_vlans-semantics) for caveats.

```terraform
resource "icx_interface_ethernet" "trunk_all" {
  port          = "1/1/24"
  tag_all_vlans = true
  untagged_vlan = 1
}
```

## Argument Reference

### Required

- `port` (String) - Port identifier in `unit/module/port` format (e.g. `"1/1/15"`).

### Optional

- `port_name` (String) - Descriptive port label. Names containing spaces are automatically quoted when sent to the switch.
- `spanning_tree_pt2pt_mac` (Boolean) - Enable `spanning-tree 802-1w admin-pt2pt-mac` on this interface. Defaults to `false`.
- `optical_monitor` (Boolean) - Enable optical monitoring on this interface. Set to `false` to send `no optical-monitor`. Defaults to `true`.
- `untagged_vlan` (Number) - VLAN ID for untagged traffic. The port is added as an `untagged ethe` member of this VLAN.
- `tagged_vlans` (Set of Number) - Set of VLAN IDs for tagged traffic. The port is added as a `tagged ethe` member of each listed VLAN. Mutually exclusive with `tag_all_vlans`.
- `tag_all_vlans` (Boolean) - When `true`, tag all VLANs on the switch (excluding `untagged_vlan` and VLAN 1) on this port. Expanded to an explicit per-VLAN list at apply time. Defaults to `false`.
- `raw_config` (List of String) - Additional raw CLI commands within the interface context (`interface ethernet N ... exit`). On destroy each command is prefixed with `no`. Use `icx_raw_config` with explicit `destroy_commands` for context-entering commands.

### Read-Only

- `id` (String) - Terraform resource identifier (port identifier).

## tag_all_vlans Semantics

`tag_all_vlans` is a Terraform-side abstraction. FastIron has no native "tag future VLANs automatically" primitive; the provider expands the option into an explicit per-VLAN `tagged ethe` list each time the resource is applied. Key behaviors:

- **At apply time:** the port is tagged on every VLAN that exists at that moment (excluding `untagged_vlan` and VLAN 1).
- **When a new VLAN is created:** `icx_vlan.Create` (>= v0.1.1) detects trunk-all ports and automatically tags the new VLAN onto them.
- **Read does not reconcile.** `Read` skips re-reading `tagged_vlans` when `tag_all_vlans = true`. Drift that occurs after the initial apply (e.g. a hand-run `no tagged ethe`) stays invisible to `terraform plan`.
- **On import:** `Read` sets `tag_all_vlans = false` because it cannot be inferred from the switch.

If auditable membership is needed — where drift surfaces as a plan diff — use an explicit `tagged_vlans` list instead.

## Import

Import by port identifier:

```shell
terraform import icx_interface_ethernet.uplink 1/1/24
```

After import, `tag_all_vlans` will be `false` and `tagged_vlans` will reflect the port's actual tagged membership at import time.
