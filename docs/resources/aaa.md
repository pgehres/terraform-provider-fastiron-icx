---
page_title: "icx_aaa Resource - fastiron-icx"
description: |-
  Manages AAA authentication settings on an ICX switch. This is a singleton resource — only one may exist per provider instance.
---

# icx_aaa (Resource)

Manages AAA (Authentication, Authorization, Accounting) settings on an ICX switch.

This is a **singleton resource** — only one `icx_aaa` resource may exist per provider instance. Its Terraform ID is always `"aaa"`.

~> **Destroy can lock out SSH access.** Destroying this resource removes the configured `aaa authentication login` and `aaa authentication web-server` settings. If the switch requires AAA for SSH login and the fallback `local` method is removed, subsequent SSH connections (including the next Terraform run) will fail. Verify fallback authentication is intact before destroying.

## Example Usage

```terraform
resource "icx_aaa" "this" {
  login_auth         = "default local"
  web_server_auth    = "default local"
  enable_aaa_console = false
}
```

## Argument Reference

### Optional

- `login_auth` (String) - AAA authentication method list for SSH/Telnet login (e.g. `"default local"`).
- `web_server_auth` (String) - AAA authentication method list for the web server (e.g. `"default local"`).
- `enable_aaa_console` (Boolean) - Enable AAA authentication for console access. Defaults to `false`.

### Read-Only

- `id` (String) - Always `"aaa"`.

## Import

```shell
terraform import icx_aaa.this aaa
```
