---
page_title: "icx_stack_unit Data Source - fastiron-icx"
description: |-
  Reads stack unit and module information from an ICX switch.
---

# icx_stack_unit (Data Source)

Reads stack unit and hardware module information from an ICX switch by parsing the running configuration.

Useful for discovering which modules are installed in which slot, and which ports are stack ports.

## Example Usage

```terraform
data "icx_stack_unit" "unit1" {
  unit_id = 1
}

output "modules" {
  value = data.icx_stack_unit.unit1.modules
}

output "stack_ports" {
  value = data.icx_stack_unit.unit1.stack_ports
}
```

## Argument Reference

### Optional

- `unit_id` (Number) - Stack unit ID to read. Defaults to `1`.

## Attributes Reference

- `id` (String) - Stack unit ID as string.
- `unit_id` (Number) - Stack unit ID.
- `modules` (List of Object) - List of modules installed in this stack unit. Each object has:
  - `id` (Number) - Module slot number.
  - `type` (String) - Module type string as reported in the running config.
- `stack_ports` (List of String) - List of stack port identifiers (e.g. `["1/2/1", "1/2/2"]`).
