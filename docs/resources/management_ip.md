---
page_title: "icx_management_ip Resource - fastiron-icx"
description: |-
  Manages the global management IP address, default gateway and DNS servers of a switch-image ICX device. Singleton resource; switch image only.
---

# icx_management_ip (Resource)

Manages the global management addressing of a device running the FastIron **switch image**: `ip address A.B.C.D M.M.M.M`, `ip default-gateway G` and `ip dns server-address D [...]`.

This is a **singleton resource** — only one `icx_management_ip` may exist per provider instance. Its Terraform ID is always `"management"`.

~> **Switch image only.** The router image has no global `ip address`: it addresses management on a VE interface. The provider detects the image type at connect time, and planning this resource against a detected router image is rejected at plan time with a clear "Switch image required" error that points at [`icx_interface_ve`](interface_ve.md) (importing fails the same way). An undetectable image is never blocked. Destroy plans always pass. A resource already in state on a device that now runs the router image (after a switch→router reimage) is dropped from state on refresh so it can be rebuilt as a VE. See [Router Image vs. Switch Image](../index.md#router-image-vs-switch-image).

!> **The provider talks to the switch through the address this resource manages.** Three rules follow, and the provider enforces all of them:

1. **Changing `ip_address` severs the SSH session.** Any plan that would send an `ip address` command — a change of `ip_address`, or a create whose `ip_address` differs from what the device has — is **refused** unless `allow_management_ip_change = true`. With the flag set, the new address is the very last command of the apply and is written to the switch together with `end` and `write memory` in a single write, so the save is already queued when the connection drops. The provider treats the dropped session as expected and records the new address in state with a warning. Afterwards:
   - point the provider block's `host` at the new address (and fix the route or gateway you reach it through);
   - run `terraform plan` — it should show no changes;
   - confirm the startup configuration holds the new address (`show configuration | include ip address`), and run `write memory` by hand if it does not — whether the queued save ran cannot be confirmed from the severed session;
   - run the change **on its own** (`terraform apply -target=icx_management_ip.this`): every command sent after the address change fails fast with "SSH session severed" until the provider reconnects.
   The address is sent as a direct replacement (`ip address <new>`), never as `no ip address` followed by `ip address` — the second command would never be received. **Not yet validated on a live device**; whether FastIron accepts a direct replacement or requires removal first is one of the things to confirm on a lab switch before relying on this path.
2. **Destroying the resource sends no commands.** Removing the management address (or the gateway the provider may be reaching the switch through) would cut off remote access for good, so `terraform destroy` only forgets the resource; the device keeps its addressing. To actually remove an address, do it from the console.
3. **Omitted attributes are kept, not removed.** `default_gateway` and `dns_servers` are Optional+Computed: when left out of the configuration, the device's current values are adopted into state and never changed. Only a gateway or DNS list that was previously set *through this resource* is removed when the attribute is later set to `null`.

## Example Usage

```terraform
# Adopt an existing switch-image device: with values matching the device this
# plans and applies with zero commands.
resource "icx_management_ip" "this" {
  ip_address      = "10.0.20.5/24"
  default_gateway = "10.0.20.1"
  dns_servers     = ["10.0.20.1"]
}
```

```terraform
# Deliberately re-address the switch. Apply with -target, then move the
# provider's host to 10.0.20.7 and verify the startup config.
resource "icx_management_ip" "this" {
  ip_address                 = "10.0.20.7/24"
  default_gateway            = "10.0.20.1"
  allow_management_ip_change = true
}
```

## Argument Reference

### Required

- `ip_address` (String) - Management IP address in CIDR notation (e.g. `"10.0.20.5/24"`), sent as `ip address 10.0.20.5 255.255.255.0`. Changing it is refused without `allow_management_ip_change`; see the warning above.

### Optional

- `default_gateway` (String) - Default gateway (`ip default-gateway`). When omitted the device's current gateway is kept and recorded in state. A change is sent as a direct replacement (FastIron keeps a single gateway), never through a gateway-less moment. Setting it to `null` after it was set here sends `no ip default-gateway <old>` — which can itself cut off a provider connecting from another subnet.
- `dns_servers` (List of String) - DNS servers (`ip dns server-address`), in order, at most four, unique. When omitted the device's current servers are kept and recorded in state. Order is part of the setting (the first server is primary) and FastIron appends on add, so any difference removes the current servers and re-adds the whole list in one `ip dns server-address A B ...` command.
- `allow_management_ip_change` (Boolean) - Must be `true` for the provider to send an `ip address` command at all. Terraform-only; reads back as `false` after import. Defaults to `false`.

### Read-Only

- `id` (String) - Always `"management"`.

## Import

```shell
terraform import icx_management_ip.this management
```

After importing, run `terraform plan` and confirm it shows **no changes** before applying — a differing `ip_address` would be refused anyway, but a differing `default_gateway` or `dns_servers` would be applied.

## Devices

| Device | Image | Addressing |
|---|---|---|
| ICX 7150-C10ZP (`08.0.95rT211`) | switch | global `ip address` + `ip default-gateway` — use this resource |
| ICX 6430-C12 (`08.0.30uT311`) | switch | global `ip address`, `ip default-gateway`, `ip dns server-address` — use this resource |
| ICX 7250-24, 7150-24, 7150-C12P (`08.0.95…T213`), ICX 6450-C12PD (`08.0.30uT313`) | router | VE interface — use [`icx_interface_ve`](interface_ve.md) |

Command generation is unit-tested against the real switch-image captures but has **not** been run against a live device yet. Validate the gateway/DNS path on a lab switch first, and treat the address-change path as experimental until it has been exercised once with console access at hand.
