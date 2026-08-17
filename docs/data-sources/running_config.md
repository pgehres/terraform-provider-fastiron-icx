---
page_title: "icx_running_config Data Source - fastiron-icx"
description: |-
  Reads the full running configuration from an ICX switch.
---

# icx_running_config (Data Source)

Reads the full running configuration from an ICX switch by executing `show running-config`.

The output is marked sensitive because it contains password hashes and potentially SNMP community strings.

## Example Usage

```terraform
data "icx_running_config" "this" {}

output "switch_config" {
  value     = data.icx_running_config.this.config
  sensitive = true
}
```

## Argument Reference

This data source has no configurable arguments.

## Attributes Reference

- `id` (String) - Always `"running-config"`.
- `config` (String, Sensitive) - The full running configuration text as returned by `show running-config`. Contains password hashes, SNMP community strings, and other sensitive data.
