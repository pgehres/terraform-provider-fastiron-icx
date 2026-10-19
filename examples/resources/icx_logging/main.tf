# Remote syslog with a larger local buffer, debug/informational kept out of
# the buffer, and the local7 facility.
resource "icx_logging" "this" {
  hosts            = ["10.0.20.50"]
  buffered_entries = 1000
  disabled_levels  = ["debugging", "informational"]
  facility         = "local7"
}
