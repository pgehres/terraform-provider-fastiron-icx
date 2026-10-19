---
page_title: "icx_snmp Resource - fastiron-icx"
description: |-
  Manages SNMP community strings, contact and location on an ICX switch. This is a singleton resource — only one may exist per provider instance.
---

# icx_snmp (Resource)

Manages SNMP v1/v2c community strings (`snmp-server community <string> ro|rw [<acl>]`), `snmp-server contact` and `snmp-server location` on an ICX switch. The syntax is the same on every supported firmware train.

This is a **singleton resource** — only one `icx_snmp` resource may exist per provider instance. Its Terraform ID is always `"snmp"`.

~> **Community strings are write-only.** FastIron never prints a community string back: the running config shows it masked (`snmp-server community ..... ro`) or, with `enable password-display`, encrypted (`snmp-server community 1 $... ro`). The provider therefore keeps the configured strings in state and cannot detect a string that was changed on the switch by hand (it does notice when the device has no community at all, and then plans to re-add yours). Each `string` is marked **Sensitive**, so it never appears in plan output, and every community string is redacted from error diagnostics (a CLI rejection shows `(redacted)` in place of the string). The full running config in the [`icx_running_config`](../data-sources/running_config.md) data source is Sensitive for the same reason.

~> **Import shows a benign diff.** Because the strings cannot be read, `terraform import icx_snmp.this snmp` records `contact` and `location` but no communities; the next plan adds every community from your configuration and the apply re-sends `snmp-server community ...` commands that the switch already has. That is idempotent and safe — the same behaviour as [`icx_user`](user.md) passwords.

## Example Usage

```terraform
resource "icx_snmp" "this" {
  communities = [
    {
      string = var.snmp_ro_community
      mode   = "ro"
      acl    = "snmp-managers" # optional standard ACL name or number
    },
    {
      string = var.snmp_rw_community
      mode   = "rw"
    },
  ]
  contact  = "Network Operations <noc@example.com>"
  location = "Rack 12, Row 3"
}
```

## Argument Reference

### Optional

- `communities` (List of Object) - SNMP communities. Each object has:
  - `string` (String, Sensitive, Required) - The community string, 1-32 printable ASCII characters without spaces, quotes, backslash or `?`.
  - `mode` (String, Required) - `"ro"` (read-only) or `"rw"` (read-write).
  - `acl` (String, Optional) - A standard ACL name or number restricting which managers may use the community, appended to the command.

  Changing any field of a community removes the old entry (`no snmp-server community <string> <mode>`) and adds the new one.
- `contact` (String) - `snmp-server contact` text, 1-255 characters; spaces allowed, control characters, double quotes and `?` are not. Removed with `no snmp-server contact`.
- `location` (String) - `snmp-server location` text, same rules.

### Read-Only

- `id` (String) - Always `"snmp"`.

## Destroy

Destroying the resource removes every community it holds in state, the contact and the location. Other `snmp-server` lines (`host`, `view`, `group`, `user`, `enable traps`) are never touched; use [`icx_raw_config`](raw_config.md) for them.

## Import

```shell
terraform import icx_snmp.this snmp
```

~> Resource operations are unit-tested but have **not** yet been run against a live device. Confirm on a lab switch that `snmp-server contact`/`location` text with spaces round-trips unquoted (the 08.0.50/08.0.60 command references show `snmp-server location United States`), and how your release masks communities in `show running-config`.
