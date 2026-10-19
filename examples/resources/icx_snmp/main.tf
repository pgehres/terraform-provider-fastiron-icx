# Community strings are write-only on the switch; keep them in variables
# marked sensitive. After import the first plan re-adds them (idempotent).
resource "icx_snmp" "this" {
  communities = [
    {
      string = var.snmp_ro_community
      mode   = "ro"
      acl    = "snmp-managers"
    },
    {
      string = var.snmp_rw_community
      mode   = "rw"
    },
  ]
  contact  = "Network Operations <noc@example.com>"
  location = "Rack 12, Row 3"
}

variable "snmp_ro_community" {
  type      = string
  sensitive = true
}

variable "snmp_rw_community" {
  type      = string
  sensitive = true
}
