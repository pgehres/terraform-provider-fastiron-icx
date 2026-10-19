---
page_title: "icx_management_ip Resource - fastiron-icx"
description: |-
  Manages the global management IP address, default gateway and DNS servers of a switch-image ICX device. Singleton resource; switch image only.
---

# icx_management_ip (Resource)

Manages the global management addressing of a device running the FastIron **switch image**: `ip address A.B.C.D M.M.M.M`, `ip default-gateway G` and `ip dns server-address D [...]`.

This is a **singleton resource** — only one `icx_management_ip` may exist per provider instance. Its Terraform ID is always `"management"`.

~> **Switch image only.** The router image has no global `ip address`: it addresses management on a VE interface. The provider detects the image type at connect time, and planning this resource against a detected router image is rejected at plan time with a clear "Switch image required" error that points at [`icx_interface_ve`](interface_ve.md) (importing fails the same way). An undetectable image is never blocked. Destroy plans always pass. A resource already in state on a device that now runs the router image (after a switch→router reimage) is dropped from state on refresh so it can be rebuilt as a VE. See [Router Image vs. Switch Image](../index.md#router-image-vs-switch-image).

!> **The provider talks to the switch through the address this resource manages — and, from another subnet, through the default gateway the switch routes its replies by.** Four rules follow, and the provider enforces all of them:

1. **Changing `ip_address` or `default_gateway` can sever the SSH session.** An address change always does; a gateway change does whenever Terraform reaches the switch through a router, because the switch's replies follow the new gateway. Any plan that would send an `ip address` or `ip default-gateway` command — a change of either attribute, or a create whose values differ from what the device has — is **refused** at plan time unless `allow_management_ip_change = true` (with the flag, the plan carries a warning). With the flag set, the gateway and address commands are the very last of the apply and are written to the switch together, gateway first, followed by `end` and `write memory`, in a single write — so the save is already queued when the connection drops. (The gateway goes first so that it has already run when the address change certainly cuts the session; whether FastIron accepts a gateway outside the current subnet before the address moves is still to be confirmed on a live switch.) The provider treats a dropped session as expected and records the new values in state with a warning. DNS changes are not in the SSH path and are applied normally, without the flag. Afterwards:
   - point the provider block's `host` at the new address if it changed (and make sure the machine running Terraform can route to it);
   - run `terraform plan` — it should show no changes;
   - confirm the startup configuration holds the change (`show configuration | include ip`), and run `write memory` by hand if it does not — whether the queued save ran cannot be confirmed from the severed session;
   - run the change **on its own** (`terraform apply -target=icx_management_ip.this`): every command sent after a severed session fails fast with "SSH session severed" until the provider reconnects.
   The address is sent as a direct replacement (`ip address <new>`), never as `no ip address` followed by `ip address` — the second command would never be received. **Not yet validated on a live device**; whether FastIron accepts a direct replacement or requires removal first is one of the things to confirm on a lab switch before relying on this path.
2. **Destroying the resource sends no commands.** Removing the management address (or the gateway the provider may be reaching the switch through) would cut off remote access for good, so `terraform destroy` only forgets the resource; the device keeps its addressing. To actually remove an address, do it from the console.
3. **Omitted attributes are kept, not removed.** `default_gateway` and `dns_servers` are Optional+Computed: when left out of the configuration — or removed from it later, or set to `null` — the device's current values are adopted into state and never changed. Terraform plans an omitted Optional+Computed attribute as its prior value, so **a default gateway cannot be removed through this resource** once the device has one; the provider never sends `no ip default-gateway` (remove it from the console if you must). DNS servers can be cleared explicitly with `dns_servers = []`.
4. **A DHCP-learned address is refused.** If the device's address is `ip address A M dynamic`, plan, create, import and refresh fail with "Management address is DHCP-learned": the lease is not configuration, and this resource manages a static address only. Remove the resource (`terraform state rm` if it is in state), or configure a static address from the console first.

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

- `default_gateway` (String) - Default gateway (`ip default-gateway`). When omitted the device's current gateway is kept and recorded in state. A change is refused without `allow_management_ip_change` (see rule 1) and is sent as a direct replacement (FastIron keeps a single gateway), never through a gateway-less moment. It cannot be removed through this resource: removing the attribute (or setting it to `null`) keeps the current gateway.
- `dns_servers` (List of String) - DNS servers (`ip dns server-address`), in order, at most four, unique. When omitted the device's current servers are kept and recorded in state; `dns_servers = []` removes them all. Order is part of the setting (the first server is primary) and FastIron appends on add, so any difference removes the current servers and re-adds the whole list in one `ip dns server-address A B ...` command.
- `allow_management_ip_change` (Boolean) - Must be `true` for the provider to send an `ip address` or `ip default-gateway` command at all. Terraform-only; reads back as `false` after import. Defaults to `false`.

### Read-Only

- `id` (String) - Always `"management"`.

## Import

```shell
terraform import icx_management_ip.this management
```

After importing, run `terraform plan` and confirm it shows **no changes** before applying — a differing `ip_address` or `default_gateway` would be refused anyway (without the flag), but a differing `dns_servers` would be applied.

## Devices

| Device | Image | Addressing |
|---|---|---|
| ICX 7150-C10ZP (`08.0.95rT211`) | switch | global `ip address` + `ip default-gateway` — use this resource |
| ICX 6430-C12 (`08.0.30uT311`) | switch | global `ip address`, `ip default-gateway`, `ip dns server-address` — use this resource |
| ICX 7250-24, 7150-24, 7150-C12P (`08.0.95…T213`), ICX 6450-C12PD (`08.0.30uT313`) | router | VE interface — use [`icx_interface_ve`](interface_ve.md) |

Command generation is unit-tested against the real switch-image captures but has **not** been run against a live device yet. Validate the DNS path on a lab switch first, and treat the gateway- and address-change paths as experimental until they have been exercised once with console access at hand.

Never put `ip address`, `ip default-gateway` (or their `no` forms) in `raw_config` or [`icx_raw_config`](raw_config.md): they would bypass these safeguards, and the automatic `no` on destroy would remove the address. The provider rejects such entries at plan time.
