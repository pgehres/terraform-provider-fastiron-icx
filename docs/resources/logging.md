---
page_title: "icx_logging Resource - fastiron-icx"
description: |-
  Manages syslog hosts, the local log buffer and the syslog facility on an ICX switch. This is a singleton resource — only one may exist per provider instance.
---

# icx_logging (Resource)

Manages the global `logging` settings of an ICX switch: syslog servers (`logging host`), the local buffer size (`logging buffered <n>`), per-severity buffered logging (`no logging buffered <level>`) and the syslog facility (`logging facility`). The syntax is the same on every supported firmware train.

This is a **singleton resource** — only one `icx_logging` resource may exist per provider instance. Its Terraform ID is always `"logging"`.

FastIron does not have a single "minimum level" knob for the buffer: every severity is logged by default (informational and debugging excepted on some releases, per the command reference) and individual levels are switched off one at a time with `no logging buffered <level>`. `disabled_levels` models exactly that; the running config shows one `no logging buffered <level>` line per entry.

## Example Usage

```terraform
resource "icx_logging" "this" {
  hosts            = ["10.0.20.50"]
  buffered_entries = 1000
  disabled_levels  = ["debugging", "informational"]
  facility         = "local7"
}
```

## Argument Reference

### Optional

- `hosts` (List of String) - Syslog servers, each sent as `logging host <ipv4>`, in order. IPv4 addresses only (an IPv6 `logging host ipv6 ...` line on the device is ignored). Omitted or empty means none. A host configured on the device with a custom `udp-port` is read back by address only.
- `buffered_entries` (Number) - Size of the local syslog buffer, 1-1000 (`logging buffered <n>`). Omitted means the device default of 50; removing the attribute sends `no logging buffered <old>`. FastIron applies a buffer-size change only after `write memory` (which the provider does) **and a reload**; the running config, and therefore `plan`, reflects the configured value immediately.
- `disabled_levels` (Set of String) - Severities whose buffered logging is turned off with `no logging buffered <level>`: `alerts`, `critical`, `debugging`, `emergencies`, `errors`, `informational`, `notifications`, `warnings`. Removing a level from the set re-enables it (`logging buffered <level>`). Omitted or empty leaves all levels at the device default.
- `facility` (String) - Syslog facility for messages sent to the hosts (`logging facility <name>`): `auth`, `cron`, `daemon`, `kern`, `local0`-`local7`, `lpr`, `mail`, `news`, `sys9`-`sys14`, `syslog`, `user`, `uucp`. Omitted means the device default (`user`); removing the attribute sends `no logging facility`.

### Read-Only

- `id` (String) - Always `"logging"`.

## Destroy

Destroying the resource returns the managed settings to the device defaults: `no logging host` for each host, `no logging buffered <n>`, `logging buffered <level>` for each disabled level and `no logging facility`. Other logging lines (`logging console`, `logging on`, `logging persistence`, `logging enable rfc5424`) are never touched.

## Import

```shell
terraform import icx_logging.this logging
```

~> Resource operations are unit-tested but have **not** yet been run against a live device. Confirm on a lab switch which severities your release logs by default (the 08.0.60 command reference says informational and debugging are off by default, in which case `logging buffered debugging` would appear in the running config as an *enabled* level; the provider reads such lines but does not manage them) and that `no logging facility` is accepted.
