---
page_title: "icx_raw_config Resource - fastiron-icx"
description: |-
  Manages arbitrary CLI configuration lines on an ICX switch. Use this as an escape hatch for features not covered by specific resources.
---

# icx_raw_config (Resource)

Manages arbitrary CLI configuration lines on an ICX switch. Use this as an escape hatch for switch features not yet covered by dedicated resources.

~> **Destroy behavior — auto-`no` is dangerous for context-entering commands.** If `destroy_commands` is not set, each command in `commands` is prefixed with `no` on destroy. This works correctly for leaf commands (`snmp-server community public ro`) but **will fail** for context-entering sequences like `router ospf` / `area 0 range 10.0.0.0/8`. For those, always set explicit `destroy_commands`.

~> **`icx_raw_config` does not import.** This resource has no `ImportState` implementation.

## Example Usage

### Simple leaf commands with auto-destroy

```terraform
resource "icx_raw_config" "snmp" {
  commands = [
    "snmp-server community public ro",
    "snmp-server location \"Server Room\"",
  ]

  expect_in_config = [
    "snmp-server community public ro",
  ]
}
```

### Context-entering commands with explicit destroy

```terraform
resource "icx_raw_config" "ospf" {
  commands = [
    "router ospf",
    "area 0",
    "exit",
    "interface ve 10",
    "ip ospf area 0",
    "exit",
  ]

  destroy_commands = [
    "no router ospf",
  ]

  expect_in_config = [
    "router ospf",
  ]
}
```

## Argument Reference

### Required

- `commands` (List of String) - CLI commands to execute in config mode on create and update. Commands are sent in order.

### Optional

- `destroy_commands` (List of String) - CLI commands to execute on destroy. If not specified, each command in `commands` is prefixed with `no`. **Always set this for multi-line or context-entering command sequences.**
- `expect_in_config` (List of String) - Lines expected to appear in the running config. If any listed line is missing from `show running-config` output, the resource is considered drifted and Terraform will re-apply it. If not set, drift cannot be detected and the provider trusts the state file.

### Read-Only

- `id` (String) - A deterministic identifier derived from the first command.

## Drift Detection

Without `expect_in_config`, this resource is effectively drift-blind — it applies commands on create/update but cannot verify they are still present. Set `expect_in_config` to at least one distinctive line from each command group to enable detection.

## write memory

The provider calls `write memory` after each create/update/destroy operation that sends commands to the switch.
