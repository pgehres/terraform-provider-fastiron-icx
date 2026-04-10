# Access port — untagged on mgmt VLAN.
resource "icx_interface_ethernet" "server1" {
  port      = "1/1/19"
  port_name = "server-0"

  untagged_vlan = icx_vlan.mgmt.vlan_id
  tagged_vlans = [
    icx_vlan.cloud.vlan_id,
    icx_vlan.k8s.vlan_id,
    icx_vlan.iot.vlan_id,
  ]
}

# Trunk port — tagged on ALL VLANs automatically.
resource "icx_interface_ethernet" "uplink_to_7250" {
  port      = "1/3/4"
  port_name = "7250-24 trunk"

  tag_all_vlans           = true
  spanning_tree_pt2pt_mac = true
}

# Port with raw config escape hatch.
resource "icx_interface_ethernet" "custom_port" {
  port      = "1/1/8"
  port_name = "patch-0"

  raw_config = [
    "rate-limit input fixed 100000",
  ]
}
