resource "icx_vlan" "mgmt" {
  vlan_id       = 200
  name          = "mgmt"
  spanning_tree = true
  stp_priority  = 4096

  multicast_passive = true
}

resource "icx_vlan" "cloud" {
  vlan_id       = 300
  name          = "cloud"
  spanning_tree = true
  stp_priority  = 4096
}

resource "icx_vlan" "k8s" {
  vlan_id       = 400
  name          = "k8s"
  spanning_tree = true
  stp_priority  = 4096
}

resource "icx_vlan" "iot" {
  vlan_id       = 800
  name          = "iot"
  spanning_tree = true
  stp_priority  = 4096
}
