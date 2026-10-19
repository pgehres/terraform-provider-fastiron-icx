---
page_title: "icx_interface_ethernet Resource - fastiron-icx"
description: |-
  Manages an ethernet interface on an ICX switch, including VLAN membership. This is the sole resource that controls which VLANs a port belongs to.
---

# icx_interface_ethernet (Resource)

Manages an ethernet interface on an ICX switch, including port-level configuration and VLAN membership.

~> **This is the only resource that manages port VLAN membership.** `icx_vlan` does not have tagged or untagged port attributes. Every port that needs VLAN membership must be declared with this resource.

~> **NEVER use `terraform apply -replace` on a live trunk.** Replacing an `icx_interface_ethernet` resource destroys it first — the destroy path strips **all** VLAN membership from the port before the create path re-adds it. On a switch's own uplink or management trunk, this removes the management VLAN mid-apply, cuts the SSH session, and leaves the Create half unexecuted. The switch becomes reachable only via console. Instead of replacing: upgrade to >= v0.1.1 and let `icx_vlan.Create` reconcile tag-all ports, convert the resource to an explicit `tagged_vlans` list in-place (additive), or add the one membership by hand.

~> **`enabled = false` on the wrong port cuts you off.** Administratively disabling the port that carries the management VLAN to the Terraform host severs the provider's SSH session mid-apply — the same class of outage as `-replace` on a live trunk — and the switch is reachable only by console. The provider refuses, at plan time, to disable the port through which the switch reaches its management next hop (its `ip default-gateway` on the switch image, its default-route next hop on the router image, located with one read-only `show arp`). That covers the usual fleet case of Terraform on another subnet. It does **not** cover a Terraform host on the switch's own subnet, a next hop missing from the ARP table, or a device without a default route: in those cases the plan carries a warning instead of an error. Check `show arp` / `show mac-address` for the path to the Terraform host before disabling any uplink.

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
- `enabled` (Boolean) - Administrative state of the port. `false` sends `disable` (the running config then shows ` disable` under the interface); `true` sends `enable`. **When omitted, the port's current admin state is kept and recorded in state**, so adding this provider version to an existing fleet is a zero-diff change and a port someone disabled by hand stays disabled until you set the attribute explicitly. Destroying the resource re-enables a port it recorded as disabled (enabled is the default every other attribute is returned to). Disabling the switch's management-path port is refused at plan time — see the warning above.
- `untagged_vlan` (Number) - VLAN ID for untagged traffic. The port is added as an `untagged ethe` member of this VLAN. On FastIron releases before 08.0.80 a port that also has tagged VLANs is instead configured with the interface-level `dual-mode` command — see [FastIron 08.0.30 `dual-mode` dialect](#fastiron-08030-dual-mode-dialect).
- `tagged_vlans` (Set of Number) - Set of VLAN IDs for tagged traffic. The port is added as a `tagged ethe` member of each listed VLAN. Mutually exclusive with `tag_all_vlans`. Do not list `untagged_vlan` here; on `dual-mode` firmware the provider adds that membership itself.
- `tag_all_vlans` (Boolean) - When `true`, tag all VLANs on the switch (excluding `untagged_vlan` and VLAN 1) on this port. Expanded to an explicit per-VLAN list at apply time. Defaults to `false`.
- `raw_config` (List of String) - Additional raw CLI commands within the interface context (`interface ethernet N ... exit`). On destroy each command is prefixed with `no`. Use `icx_raw_config` with explicit `destroy_commands` for context-entering commands. Entries beginning with `hostname`, `ip address` or `ip default-gateway` (or their `no` forms) are rejected at plan time; use the dedicated attributes instead.

### Read-Only

- `id` (String) - Terraform resource identifier (port identifier).

## tag_all_vlans Semantics

`tag_all_vlans` is a Terraform-side abstraction. FastIron has no native "tag future VLANs automatically" primitive; the provider expands the option into an explicit per-VLAN `tagged ethe` list each time the resource is applied. Key behaviors:

- **At apply time:** the port is tagged on every VLAN that exists at that moment (excluding `untagged_vlan` and VLAN 1).
- **When a new VLAN is created:** `icx_vlan.Create` (>= v0.1.1) detects trunk-all ports and automatically tags the new VLAN onto them.
- **Read does not reconcile.** `Read` skips re-reading `tagged_vlans` when `tag_all_vlans = true`. Drift that occurs after the initial apply (e.g. a hand-run `no tagged ethe`) stays invisible to `terraform plan`.
- **On import:** `Read` sets `tag_all_vlans = false` because it cannot be inferred from the switch.

If auditable membership is needed — where drift surfaces as a plan diff — use an explicit `tagged_vlans` list instead.

### Bulk tagging on FastIron 08.0.50 and later

On firmware that has the interface-level `vlan-config` commands (introduced in FastIron 08.0.50, so every 08.0.9x ICX 7000-series device) the provider applies the expansion with one command instead of one `tagged ethe` per VLAN:

```text
interface ethernet 1/1/24
 vlan-config add all-tagged
 exit
```

What this does and does not change:

- **Same result as the per-VLAN form.** `vlan-config add all-tagged` is a one-shot, non-savable command: it tags the port in every VLAN that exists *at that moment*, and what gets saved by `write memory` is ordinary per-VLAN `tagged ethe` membership. It is **not** a "tag future VLANs automatically" setting — a VLAN created later is still tagged onto trunk-all ports by `icx_vlan.Create` exactly as before, and `Read`, import and destroy still work per VLAN.
- **When it is used.** Only for the VLANs a `tag_all_vlans = true` apply has to *add* — on create, on the transition from an explicit `tagged_vlans` list (including the repair `Read` plans for a drifted trunk) and when `untagged_vlan` changes. Removals, explicit `tagged_vlans` lists and the `dual-mode` dialect keep the per-VLAN commands. An apply with nothing to add sends nothing.
- **Verified against the device.** The command's documentation does not say whether it also tags the default VLAN 1 or the port's own `untagged_vlan`, both of which the expansion leaves out. After sending it the provider re-reads the running config and, if either happened, removes that tag again (`no tagged ethe`, restoring `untagged ethe` for the untagged VLAN). The fleet's running configs suggest the switch already skips both, in which case nothing more is sent; either way the bulk and per-VLAN paths converge on identical membership, and the unit tests prove that under every reading of the command's behaviour.
- **Firmware selection.** The capability is decided from the device's firmware version (see [Firmware dialects and capabilities](../index.md#firmware-dialects-and-capabilities)). Releases before 08.0.50 — the 08.0.30 ICX 6430/6450 — and devices whose version cannot be parsed always receive the per-VLAN form, so a command that might not exist is never sent.
- **Refusals.** The switch refuses the bulk command on a port that carries IP configuration (`error - IP routing, vrf, ip policy, route-only, rpf-mode, ip-mac or ip tcp mss was configured on ports ...`) and does not allow it on private-VLAN, MCT, GVRP, port-extender or flex-auth ports. The provider detects that error and fails the apply before `write memory`, with no membership changed; move or remove the conflicting configuration, or use an explicit `tagged_vlans` list for that port.

~> The bulk path is generated from the Ruckus FastIron 08.0.50–08.0.90 command references and unit-tested against the real 08.0.95 captures, but has **not** yet been run against a live device. The acceptance harness should confirm, on the scratch port, that after `tag_all_vlans = true` the port is tagged in every VLAN except 1 and its untagged VLAN, that its untagged membership is intact, that `show running-config` lists no tagged member under `vlan 1`, that re-running the command on an already fully-tagged port is accepted, and that the saved config survives a reload.

## FastIron 08.0.30 `dual-mode` dialect

FastIron releases **before 08.0.80** (the 08.0.30 train on the EOL ICX 6430/6450, and 08.0.5x/08.0.7x) express a trunk port's untagged VLAN differently from 08.0.9x. On those releases a port that is tagged in any VLAN cannot also be made `untagged` in VLAN context — the CLI rejects it. Instead the port is made a **tagged** member of the untagged VLAN as well, and the interface-level `dual-mode <vlan-id>` command selects which of its VLANs carries untagged frames ("only tagged ports can be configured as dual-mode ports"). A bare `dual-mode` with no argument means the default VLAN 1, and `no dual-mode` returns the port to tagged-only. Access ports (`untagged_vlan` with no tagged VLANs) use the ordinary VLAN-context `untagged ethe` form on every release. The command was deprecated in 08.0.80, where the VLAN-context form used by this resource on 08.0.9x became the only one.

The running config on such a device shows the two halves in two places:

```text
vlan 102 name management by port
 tagged ethe 1/2/1
!
interface ethernet 1/2/1
 dual-mode  102
```

The provider handles this transparently; **your configuration is the same on both dialects**:

```terraform
resource "icx_interface_ethernet" "uplink" {
  port          = "1/2/1"
  untagged_vlan = 102
  tagged_vlans  = [101, 107, 109, 110, 120, 200]
}
```

- **Dialect selection** is by the firmware version on the device's `ver` line: releases below 08.0.80 use `dual-mode`; 08.0.80 and later (including 08.0.95) use the VLAN-context form unchanged. A running config that already contains `dual-mode` lines is treated as the `dual-mode` dialect regardless of version. On firmware without the command, `dual-mode` is never sent.
- **Read** reports `untagged_vlan` from the interface's `dual-mode` line when present, and leaves that VLAN out of `tagged_vlans` even though the switch lists the port as tagged in it. Ports without a `dual-mode` line are read from VLAN context as on 08.0.9x. The configuration above therefore plans clean against the running config shown.
- **Apply** on `dual-mode` firmware tags the port in `tagged_vlans` **and** in `untagged_vlan`, then issues `dual-mode <untagged_vlan>` in interface context (bare `dual-mode` for VLAN 1). Changing `untagged_vlan` on an existing trunk re-issues `dual-mode` with the new VLAN directly — there is no intermediate `no dual-mode`, which would briefly drop the untagged VLAN and, on a management uplink, the provider's own SSH session — and only then removes the old VLAN's tagged membership. Removing `untagged_vlan` from a trunk sends `no dual-mode`. Converting an access port to a trunk (or back) necessarily passes through a moment where the port is in neither form; avoid doing that on the switch's management path.
- **Destroy** sends `no dual-mode` before stripping the port's VLAN memberships.
- **Drift:** the device's actual `dual-mode` line is the source of truth, so a port whose `dual-mode` was removed by hand is re-asserted on the next apply that changes the resource (a pure drift with no other change is visible to `plan` as an `untagged_vlan` diff, because Read reports the VLAN-context state).

~> Resource operations on 08.0.30 are generated from the FastIron 08.0.30–08.0.60 Layer 2 guides and command references and are covered by unit tests against real sanitized captures from an ICX 6430-C12 and an ICX 6450-C12PD, but they have not yet been exercised against a live 08.0.30 device. Validate on a non-production port first.

## Import

Import by port identifier:

```shell
terraform import icx_interface_ethernet.uplink 1/1/24
```

After import, `tag_all_vlans` will be `false`, `tagged_vlans` will reflect the port's actual tagged membership at import time, and `enabled` reflects the port's admin state. On `dual-mode` firmware the dual-mode VLAN is imported as `untagged_vlan` and omitted from `tagged_vlans`.
