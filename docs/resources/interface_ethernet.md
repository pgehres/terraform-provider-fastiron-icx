---
page_title: "icx_interface_ethernet Resource - fastiron-icx"
description: |-
  Manages an ethernet interface on an ICX switch, including VLAN membership. This is the sole resource that controls which VLANs a port belongs to.
---

# icx_interface_ethernet (Resource)

Manages an ethernet interface on an ICX switch, including port-level configuration and VLAN membership.

~> **This is the only resource that manages port VLAN membership.** `icx_vlan` does not have tagged or untagged port attributes. Every port that needs VLAN membership must be declared with this resource.

~> **NEVER use `terraform apply -replace` on a live trunk.** Replacing an `icx_interface_ethernet` resource destroys it first — the destroy path strips **all** VLAN membership from the port before the create path re-adds it. On a switch's own uplink or management trunk, this removes the management VLAN mid-apply, cuts the SSH session, and leaves the Create half unexecuted. The switch becomes reachable only via console. Instead of replacing: upgrade to >= v0.1.1 and let `icx_vlan.Create` reconcile tag-all ports, convert the resource to an explicit `tagged_vlans` list in-place (additive), or add the one membership by hand.

~> **Never manage the same port with two resources.** Two `icx_interface_ethernet` resources for the same port make each apply clobber the other — Terraform never converges and `plan` always shows pending changes. The provider rejects this at plan time when the two declarations differ (see [Duplicate port detection](#duplicate-port-detection)). Remove the duplicate with `terraform state rm` **before** deleting it from config, so the destroy does not strip membership the remaining resource still owns.

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

`tag_all_vlans = true` is a Terraform-only convenience that expands to all non-default VLANs on the switch at apply time. The flag itself cannot be read back from the switch, but the membership it promises can: `Read` checks the port against that expansion and reports a missing VLAN as a plan diff. See [tag_all_vlans semantics](#tag_all_vlans-semantics).

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
- `untagged_vlan` (Number) - VLAN ID for untagged traffic. The port is added as an `untagged ethe` member of this VLAN. On FastIron releases before 08.0.80 a port that also has tagged VLANs is instead configured with the interface-level `dual-mode` command — see [FastIron 08.0.30 `dual-mode` dialect](#fastiron-08030-dual-mode-dialect).
- `tagged_vlans` (Set of Number) - Set of VLAN IDs for tagged traffic. The port is added as a `tagged ethe` member of each listed VLAN. Mutually exclusive with `tag_all_vlans`. Do not list `untagged_vlan` here; on `dual-mode` firmware the provider adds that membership itself.
- `tag_all_vlans` (Boolean) - When `true`, tag all VLANs on the switch (excluding `untagged_vlan` and VLAN 1) on this port. Expanded to an explicit per-VLAN list at apply time, and checked against the switch on every refresh. Defaults to `false`.
- `raw_config` (List of String) - Additional raw CLI commands within the interface context (`interface ethernet N ... exit`). On destroy each command is prefixed with `no`. Use `icx_raw_config` with explicit `destroy_commands` for context-entering commands. Entries beginning with `hostname`, `ip address` or `ip default-gateway` (or their `no` forms) are rejected at plan time; use the dedicated attributes instead.

### Read-Only

- `id` (String) - Terraform resource identifier (port identifier).

## tag_all_vlans Semantics

`tag_all_vlans` is a Terraform-side abstraction. FastIron has no native "tag future VLANs automatically" primitive; the provider expands the option into an explicit per-VLAN `tagged ethe` list each time the resource is applied. Key behaviors:

- **At apply time:** the port is tagged on every VLAN that exists at that moment (excluding `untagged_vlan` and VLAN 1).
- **When a new VLAN is created:** `icx_vlan.Create` (>= v0.1.1) detects trunk-all ports and automatically tags the new VLAN onto them.
- **Read reconciles.** On every refresh the provider compares the port's actual tagged membership with the same expansion the apply path uses. When they match, state is left exactly as it was (`tagged_vlans` stays null), so a healthy trunk plans with no changes. When a VLAN is missing — a hand-run `no tagged ethe`, a VLAN that predates Terraform on that switch, an apply that died halfway — Read records what the switch actually has (`tag_all_vlans = false`, `tagged_vlans = <actual set>`) and emits a warning naming the missing VLANs:

  ```text
  Warning: tag_all_vlans trunk 1/3/2 has drifted

  Port 1/3/2 is declared tag_all_vlans = true, but the switch does not tag it in
  every VLAN. Missing from the port: VLAN 900, 901, 902. ...

  # icx_interface_ethernet.trunk will be updated in-place
    ~ tag_all_vlans = false -> true
    - tagged_vlans  = [20, 30, 40, 50, 70] -> null
  ```

  That plan reads as if membership were being removed; it is not. Applying it issues `tagged ethe` for exactly the missing VLANs and removes nothing: the expansion is every VLAN except VLAN 1 and the untagged VLAN, and a FastIron port cannot be both tagged and untagged in one VLAN, so the switch's tagged set is always a subset of the expansion. The only removal a `tag_all_vlans` trunk ever sees is when `untagged_vlan` itself is changed in configuration. If a switch ever reported a tagged VLAN outside the expansion, the warning would name it, because the apply would drop that membership.
- **On import:** `Read` sets `tag_all_vlans = false` because it cannot be inferred from the switch.

Use an explicit `tagged_vlans` list when the membership should be visible in state and reviewed VLAN by VLAN; the trade-off is that every VLAN must be listed, and omitting one removes it from the trunk.

## Duplicate port detection

Declaring one port in two `icx_interface_ethernet` resources on the same provider instance (the same switch) is rejected during `terraform plan`:

```text
Error: Port 1/2/1 is managed by more than one icx_interface_ethernet resource

Another icx_interface_ethernet resource using this provider instance also declares
port 1/2/1, with a different tagged_vlans, untagged_vlan. ...
```

The plugin protocol does not tell a resource its own address, so the check works by recording, per provider instance and per plan, the configuration each resource claims for its port, and erroring when a second claim on the same port is *definitely* different: at least one attribute known on both sides and unequal. That rule is what keeps Terraform's own re-plans of a single resource (a replacement is planned twice; values that are unknown until apply are planned again once known) from being reported. Its limits:

- Two resources with byte-identical configuration on one port are not detected. They also converge to the same device state and do not churn, but destroying one still strips the port, so remove it with `terraform state rm` first.
- Destroy plans are not claims, so the error does not fire while the redundant resource is being removed from configuration.
- The same port number on two switches (two provider aliases) is never a duplicate.

The fix is the same as before the check existed: keep one resource per port, and `terraform state rm` the redundant one before deleting it from configuration.

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

After import, `tag_all_vlans` will be `false` and `tagged_vlans` will reflect the port's actual tagged membership at import time. Setting `tag_all_vlans = true` on the imported resource then plans as an in-place update that tags whatever the expansion still lacks. On `dual-mode` firmware the dual-mode VLAN is imported as `untagged_vlan` and omitted from `tagged_vlans`.
