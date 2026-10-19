---
page_title: "icx_ntp Resource - fastiron-icx"
description: |-
  Manages NTP servers and system clock settings (time zone, summer time) on an ICX switch. This is a singleton resource — only one may exist per provider instance.
---

# icx_ntp (Resource)

Manages the `ntp` block (`server <address>`, `disable serve`) and the global clock settings (`clock timezone`, `clock summer-time`) of an ICX switch.

This is a **singleton resource** — only one `icx_ntp` resource may exist per provider instance. Its Terraform ID is always `"ntp"`.

The same syntax is used on every supported firmware train: the `ntp` block form appears verbatim in the FastIron 08.0.30 capture of an ICX 6430 and in the 08.0.6x–08.0.9x management guides. The one dialect difference is the set of time-zone regions: `europe` and `australia` were added in FastIron 08.0.50, so on older firmware (08.0.30 on the ICX 6430/6450) a `timezone` using them is rejected at plan time with **Not supported by this firmware**; `gmt` and `us` work everywhere. See [Firmware dialects and capabilities](../index.md#firmware-dialects-and-capabilities).

## Example Usage

```terraform
resource "icx_ntp" "this" {
  servers     = ["216.239.35.0", "216.239.35.4"]
  serve       = false # client only: `disable serve`
  timezone    = "us pacific"
  summer_time = true
}
```

Against the ICX 6430 reference capture, which holds exactly that configuration, this plans with no changes.

## Argument Reference

### Optional

- `servers` (List of String) - NTP servers, each sent as `server <address>` inside the `ntp` block, in order. IPv4 addresses (FastIron 08.0.x documents only IP addresses here; a DNS name passes the provider's validation but may be rejected by the switch). Omitted or empty means no servers. Changes are applied as `no server <old>` for removed entries and `server <new>` for added ones, inside one `ntp` block.
- `serve` (Boolean) - Whether the switch also serves time to downstream NTP clients. `false` sends `disable serve`; `true` sends `no disable serve`. Defaults to `true`, the device default.
- `timezone` (String) - System clock time zone as `"<region> <zone>"` for `clock timezone`: region `australia`, `europe`, `gmt` or `us`, and a zone such as `pacific`, `east-indiana`, `cet` or `gmt+05:30`. Sent lower-cased. The device prints the zone capitalised (`clock timezone us Pacific`); the provider compares case-insensitively and keeps your spelling, so `"us pacific"` plans clean against it. Omitted means the device default (GMT); removing the attribute sends `no clock timezone <old>`. `europe`/`australia` require FastIron 08.0.50 or later.
- `summer_time` (Boolean) - Enable daylight saving time with the zone's default dates (`clock summer-time`). Defaults to `false`. A `clock summer-time` line that carries explicit `zone`/`start`/`end`/`offset` arguments on the device is read back as `true`; those arguments are not managed.

### Read-Only

- `id` (String) - Always `"ntp"`.

## Destroy

Destroying the resource returns every managed setting to its default: `no clock summer-time`, `no clock timezone <zone>`, `no server <address>` for each server and `no disable serve`. Other lines in the `ntp` block (`peer`, `authenticate`, `source-interface`, server options such as `minpoll`) are never touched by this resource.

## Import

```shell
terraform import icx_ntp.this ntp
```

~> Resource operations are unit-tested against the real sanitized captures but have **not** yet been run against a live device. In particular, confirm on a lab switch that `clock timezone` applies without an interactive confirmation prompt on your release (the 08.0.50 command reference shows a `y/n` prompt for `clock summer-time` with explicit dates), and that `no disable serve` is the accepted way to re-enable serving.
