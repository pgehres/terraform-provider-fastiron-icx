---
page_title: "icx_user Resource - fastiron-icx"
description: |-
  Manages a local user account on an ICX switch.
---

# icx_user (Resource)

Manages a local user account on an ICX switch.

~> **Passwords are write-only.** FastIron stores password hashes in the running config; the provider cannot read back a plaintext password. When you import an existing user, the state will always show a password diff on the next plan. This is safe — the `username X password Y` command is idempotent. Accept the diff and apply to bring state in sync.

~> **Deleting the provider's own login account will not lock you out immediately** (the current SSH session continues), but subsequent Terraform runs will fail to connect. The provider does not currently prevent self-deletion. Ensure at least one valid admin account exists before destroying the account Terraform uses.

## Example Usage

```terraform
resource "icx_user" "admin" {
  username = "admin"
  password = var.admin_password
}

resource "icx_user" "readonly" {
  username = "monitor"
  password = var.monitor_password
}
```

## Argument Reference

### Required

- `username` (String) - The username for the local account.
- `password` (String, Sensitive) - The password for the user. This value is write-only — it cannot be read back from the switch.

### Read-Only

- `id` (String) - Terraform resource identifier (username).

## Import

Import by username:

```shell
terraform import icx_user.admin admin
```

After import, `terraform plan` will show a password diff because the stored hash cannot be converted back to plaintext. Apply the plan to reconcile state; the command is idempotent and does not change the password.
