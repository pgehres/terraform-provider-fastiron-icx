package parser

import (
	"regexp"
	"strconv"
)

// reFirmwareVersion matches the numeric prefix of a FastIron version string
// as it appears on the `ver` line of the running config, e.g. "08.0.30uT311"
// or "08.0.95rT211". Letter suffixes (patch letters and the T-code) are
// ignored for ordering purposes.
var reFirmwareVersion = regexp.MustCompile(`^(\d+)\.(\d+)\.(\d+)`)

// FirmwareVersion is the numeric part of a FastIron release number.
type FirmwareVersion struct {
	Major, Minor, Patch int
}

// ParseFirmwareVersion extracts the numeric major.minor.patch triple from a
// FastIron version string. ok is false when the string does not start with a
// recognizable release number.
func ParseFirmwareVersion(ver string) (v FirmwareVersion, ok bool) {
	m := reFirmwareVersion.FindStringSubmatch(ver)
	if m == nil {
		return FirmwareVersion{}, false
	}
	v.Major, _ = strconv.Atoi(m[1])
	v.Minor, _ = strconv.Atoi(m[2])
	v.Patch, _ = strconv.Atoi(m[3])
	return v, true
}

// Less reports whether v is an earlier release than o.
func (v FirmwareVersion) Less(o FirmwareVersion) bool {
	if v.Major != o.Major {
		return v.Major < o.Major
	}
	if v.Minor != o.Minor {
		return v.Minor < o.Minor
	}
	return v.Patch < o.Patch
}

// dualModeRemovedIn is the first FastIron release without the interface-level
// `dual-mode` command. The 08.0.80 release notes list `dual-mode [vlan-id]`
// as deprecated — from that release a port may simply be an untagged member
// of one VLAN and a tagged member of others via VLAN-context commands — and
// the 08.0.90 Layer 2 guide states the command is no longer supported.
var dualModeRemovedIn = FirmwareVersion{Major: 8, Minor: 0, Patch: 80}

// FirmwareHasDualMode reports whether the given FastIron release uses the
// `dual-mode` interface dialect for trunk ports that also carry an untagged
// VLAN (FastIron 08.0.30 through 08.0.7x, e.g. the ICX 6430/6450). It returns
// false for unparseable version strings so that the dialect is never emitted
// to firmware that is not positively known to accept it.
func FirmwareHasDualMode(ver string) bool {
	v, ok := ParseFirmwareVersion(ver)
	if !ok {
		return false
	}
	return v.Less(dualModeRemovedIn)
}

// UsesDualMode reports whether the device this configuration came from
// expresses trunk-untagged membership with the `dual-mode` interface command.
// The firmware version on the `ver` line is authoritative; as a fallback, a
// configuration that already contains `dual-mode` lines is by definition from
// firmware that has the command.
func (c *RunningConfig) UsesDualMode() bool {
	if FirmwareHasDualMode(c.Global.Version) {
		return true
	}
	for i := range c.EthernetInterfaces {
		if c.EthernetInterfaces[i].DualModeVLAN != nil {
			return true
		}
	}
	return false
}
