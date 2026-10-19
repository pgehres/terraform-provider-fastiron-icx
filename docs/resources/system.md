---
page_title: "icx_system Resource - fastiron-icx"
description: |-
  Manages global system settings on an ICX switch. This is a singleton resource — only one may exist per provider instance.
---

# icx_system (Resource)

Manages global system settings on an ICX switch.

This is a **singleton resource** — only one `icx_system` resource may exist per provider instance. Its Terraform ID is always `"system"`.

~> **Switch-image note:** `global-stp` is a FastIron **router image** command. On a device running the switch image (`SPS…`/`T211`-style versions — e.g. an ICX 7150-C10ZP shipped with the L2 image), setting `global_stp = true` is rejected at plan time with a clear "Router image required" error (if the image type cannot be detected, it instead fails at apply time with the switch's CLI error); keep it `false` (the default) there. See [Router Image vs. Switch Image](../index.md#router-image-vs-switch-image).

## Example Usage

```terraform
resource "icx_system" "this" {
  global_stp               = true
  telnet_server            = false
  dhcp_client_disable      = true
  optical_monitor          = true
  optical_monitor_non_ruckus = false
}
```

## Argument Reference

### Optional

- `global_stp` (Boolean) - Enable global 802.1w spanning tree. Defaults to `false`. Router image only — see the switch-image note above; on a switch-image device (any model) this command does not exist, and setting `true` against a detected switch image is rejected at plan time (an undetected image degrades to an apply-time CLI error).
- `telnet_server` (Boolean) - Enable the telnet server. Set to `false` to disable (sends `no telnet server`). Defaults to `true`.
- `dhcp_client_disable` (Boolean) - Disable the DHCP client globally. Defaults to `false`. Rendered as `ip dhcp-client disable` on most firmware, and as `no ip dhcp-client enable` on the FastIron 08.0.30 switch image (ICX 6430); both spellings are read back as `true` (see [Firmware dialects and capabilities](../index.md#firmware-dialects-and-capabilities)). **Migration:** on FastIron 08.0.30 switch-image devices the DHCP client is reported correctly as disabled starting with this version (earlier versions read it as `false`). Set `dhcp_client_disable = true` to match before applying; otherwise the one-time plan diff `true -> false` would send `ip dhcp-client enable` to a device that uses a static management IP.
- `optical_monitor` (Boolean) - Enable optical monitoring globally. Defaults to `false`.
- `optical_monitor_non_ruckus` (Boolean) - Enable optical monitoring for non-Ruckus optics. Defaults to `false`.
- `manager_registrar` (Boolean) - Enable the manager registrar. Defaults to `false`. The `manager_*` attributes require FastIron 08.0.80 or later; setting them on an older release (e.g. 08.0.30 on the ICX 6430/6450) is rejected at plan time.
- `manager_disable` (Boolean) - Disable the manager. Defaults to `false`.
- `manager_port_list` (String) - Manager port list string (e.g. `"987"`).

### Read-Only

- `id` (String) - Always `"system"`.

## Import

```shell
terraform import icx_system.this system
```
