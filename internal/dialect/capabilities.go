package dialect

import "fmt"

// Capability names one firmware- or image-dependent behaviour.
type Capability string

const (
	// CapVEInterfaces: `interface ve N`. Router image only.
	CapVEInterfaces Capability = "ve_interfaces"
	// CapRouterInterface: `router-interface ve N` in VLAN context. Router image only.
	CapRouterInterface Capability = "router_interface"
	// CapGlobalSTP: the global `global-stp` command. Router image only — the
	// switch image runs STP per VLAN and has no such command.
	CapGlobalSTP Capability = "global_stp"
	// CapGlobalManagementIP: the switch image's single global `ip address`,
	// `ip default-gateway` and `ip dns server-address` management addressing
	// (icx_management_ip). Switch image only — the router image addresses
	// management on a VE interface (CapVEInterfaces) and has no global
	// `ip address`.
	CapGlobalManagementIP Capability = "global_management_ip"
	// CapManager: the SmartZone/Unleashed `manager ...` commands
	// (registrar, disable, port-list). Absent on FastIron 08.0.30 (ICX
	// 6430/6450); SmartZone documents 08.0.80 as the minimum release for ICX
	// management.
	CapManager Capability = "manager"
	// CapOpticalMonitor: the global `optical-monitor` commands behind
	// icx_system's optical_monitor and optical_monitor_non_ruckus attributes.
	// (Per-interface `optical-monitor` on icx_interface_ethernet is not
	// gated.) Present in every known dialect; whether a given optic or
	// platform reports DOM data is a hardware question left to the switch.
	CapOpticalMonitor Capability = "optical_monitor"

	// CapClockTimezoneRegions: the `europe` and `australia` region keywords of
	// `clock timezone` (icx_ntp's timezone attribute). FastIron 08.0.50 added
	// them; earlier releases (08.0.30 on the ICX 6430/6450) accept only the
	// `gmt` and `us` regions. The command itself exists everywhere.
	CapClockTimezoneRegions Capability = "clock_timezone_regions"

	// CapDualMode is a syntax form, not a feature: trunk ports that also carry
	// an untagged VLAN are expressed with `dual-mode <vlan>` under the
	// interface (FastIron 08.0.30 through 08.0.7x) instead of a VLAN-context
	// `untagged ethe` alongside `tagged ethe` (08.0.80+). The 08.0.80 release
	// notes deprecate `dual-mode`; the 08.0.90 L2 guide says it is gone.
	CapDualMode Capability = "dual_mode"
	// CapLegacyDHCPClient is a syntax form: the DHCP client is controlled with
	// `[no] ip dhcp-client enable` (enabled by default) instead of
	// `[no] ip dhcp-client disable`. Observed on the 08.0.30 switch image (ICX
	// 6430); the 08.0.30 router image (ICX 6450) already uses the 08.0.9x form.
	// Use DHCPClientDisable rather than branching on this directly.
	CapLegacyDHCPClient Capability = "legacy_dhcp_client"
	// CapVlanConfigAllTagged is a syntax form: the interface-context
	// `vlan-config add all-tagged` command tags a port in every VLAN on the
	// device in one operation (introduced in FastIron 08.0.50 per the command
	// reference; present on the 08.0.9x ICX 7000s). Without it, tag_all_vlans
	// is spelled out as one VLAN-context `tagged ethe` per VLAN. It is a
	// one-shot, non-savable command whose effect is ordinary per-VLAN tagged
	// membership — it does not subscribe the port to VLANs created later.
	// Unknown firmware falls back to the per-VLAN spelling, which exists
	// everywhere, so a command that might not exist is never sent. Having the
	// capability is necessary but not sufficient: icx_interface_ethernet only
	// uses the command when the provider opts in (native_bulk_tagging,
	// experimental, default off) and the port is a tagged-only,
	// non-management trunk.
	CapVlanConfigAllTagged Capability = "vlan_config_all_tagged"
)

var (
	// v08050 added the europe/australia regions to `clock timezone` and
	// introduced the interface-context `vlan-config` commands.
	v08050 = Version{Major: 8, Minor: 0, Patch: 50}
	// v08080 is the first release with the 08.0.9x-style VLAN/L2 dialect
	// (dual-mode deprecated) and the minimum SmartZone-manageable release.
	v08080 = Version{Major: 8, Minor: 0, Patch: 80}
)

// kind distinguishes feature capabilities (permissive when unknown) from
// syntax forms (08.0.9x spelling when unknown). Both are encoded in the
// predicate itself; kind is used for documentation and tests.
type kind int

const (
	feature kind = iota
	form
)

// rule is one row of the capability table.
type rule struct {
	kind kind
	// has decides the capability. Feature predicates must return true when
	// the axis they depend on is unknown; form predicates must return false.
	has func(d Dialect) bool
	// title is the diagnostic summary when a feature is unavailable.
	title string
	// why explains, as a clause following the feature name, why the feature
	// is unavailable on d. Only consulted when has(d) is false.
	why func(d Dialect) string
	// docs points the user at the relevant documentation section.
	docs string
}

const (
	docsRouterImage = `See the "Router Image vs. Switch Image" section of the provider documentation ` +
		`(docs/index.md#router-image-vs-switch-image).`
	docsDialects = `See the "Firmware dialects and capabilities" section of the provider documentation ` +
		`(docs/index.md#firmware-dialects-and-capabilities).`
)

var routerImageRule = rule{
	kind:  feature,
	has:   func(d Dialect) bool { return !d.switchImage() },
	title: "Router image required",
	why: func(d Dialect) string {
		return fmt.Sprintf("is only available on the FastIron router image, but this device runs "+
			"the switch image (firmware %s)", d.Firmware())
	},
	docs: docsRouterImage,
}

var switchImageRule = rule{
	kind:  feature,
	has:   func(d Dialect) bool { return !d.routerImage() },
	title: "Switch image required",
	why: func(d Dialect) string {
		return fmt.Sprintf("is only available on the FastIron switch image, which addresses the device "+
			"with one global `ip address`; this device runs the router image (firmware %s), where the "+
			"management address lives on a VE interface — use icx_interface_ve (with router_interface "+
			"on icx_vlan) instead", d.Firmware())
	},
	docs: docsRouterImage,
}

// table is the capability matrix. To support a new model or firmware, add or
// adjust rows here — never add version/image checks in resources.
var table = map[Capability]rule{
	CapVEInterfaces:       routerImageRule,
	CapRouterInterface:    routerImageRule,
	CapGlobalSTP:          routerImageRule,
	CapGlobalManagementIP: switchImageRule,
	CapManager: {
		kind:  feature,
		has:   func(d Dialect) bool { return !d.before(v08080) },
		title: "Not supported by this firmware",
		why: func(d Dialect) string {
			return fmt.Sprintf("is not available on FastIron %s (firmware %s); the manager commands "+
				"require FastIron 08.0.80 or later", d.Version.Family(), d.Firmware())
		},
		docs: docsDialects,
	},
	CapOpticalMonitor: {
		kind: feature,
		has:  func(Dialect) bool { return true },
	},
	CapClockTimezoneRegions: {
		kind:  feature,
		has:   func(d Dialect) bool { return !d.before(v08050) },
		title: "Not supported by this firmware",
		why: func(d Dialect) string {
			return fmt.Sprintf("is not available on FastIron %s (firmware %s); the europe and australia "+
				"time-zone regions require FastIron 08.0.50 or later, and earlier releases accept only "+
				"the gmt and us regions", d.Version.Family(), d.Firmware())
		},
		docs: docsDialects,
	},
	CapDualMode: {
		kind: form,
		has:  func(d Dialect) bool { return d.dualModeSeen || d.before(v08080) },
	},
	CapLegacyDHCPClient: {
		kind: form,
		has:  func(d Dialect) bool { return d.before(v08080) && d.switchImage() },
	},
	CapVlanConfigAllTagged: {
		kind: form,
		has:  func(d Dialect) bool { return d.Known && !d.Version.Less(v08050) },
	},
}

// Capabilities lists every capability in display order.
func Capabilities() []Capability {
	return []Capability{
		CapVEInterfaces, CapRouterInterface, CapGlobalSTP, CapGlobalManagementIP,
		CapManager, CapOpticalMonitor, CapClockTimezoneRegions, CapDualMode, CapLegacyDHCPClient,
		CapVlanConfigAllTagged,
	}
}

// IsSyntaxForm reports whether c selects between two spellings rather than
// gating a feature.
func IsSyntaxForm(c Capability) bool { return table[c].kind == form }

// Has reports whether d has capability c. An unregistered capability is
// reported as present, consistent with the never-block rule.
func (d Dialect) Has(c Capability) bool {
	r, ok := table[c]
	if !ok {
		return true
	}
	return r.has(d)
}

// Why returns a clause explaining why d lacks c, suitable for following the
// feature name: "<feature> " + Why(c). It returns "" when d has c.
func (d Dialect) Why(c Capability) string {
	r, ok := table[c]
	if !ok || r.has(d) || r.why == nil {
		return ""
	}
	return r.why(d)
}

// Title returns the diagnostic summary for a missing c.
func Title(c Capability) string {
	if t := table[c].title; t != "" {
		return t
	}
	return "Not supported by this firmware"
}

// Docs returns the documentation pointer for a missing c.
func Docs(c Capability) string {
	if s := table[c].docs; s != "" {
		return s
	}
	return docsDialects
}
