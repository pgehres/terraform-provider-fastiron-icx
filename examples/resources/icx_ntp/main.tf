# NTP client-only with the US Pacific zone and daylight saving. Against the
# ICX 6430 reference capture this plans with no changes.
resource "icx_ntp" "this" {
  servers     = ["216.239.35.0", "216.239.35.4"]
  serve       = false
  timezone    = "us pacific"
  summer_time = true
}
