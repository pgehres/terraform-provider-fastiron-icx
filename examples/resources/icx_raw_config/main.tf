# Example: manage SNMP settings not covered by a specific resource.
resource "icx_raw_config" "snmp" {
  commands = [
    "snmp-server community public ro",
    "snmp-server host 10.0.0.5 version v2c public",
  ]

  destroy_commands = [
    "no snmp-server community public ro",
    "no snmp-server host 10.0.0.5 version v2c public",
  ]

  expect_in_config = [
    "snmp-server community public ro",
  ]
}
