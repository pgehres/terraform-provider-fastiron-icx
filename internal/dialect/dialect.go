// Package dialect is the single place that knows how FastIron behaviour
// differs by firmware release and image type.
//
// A Dialect is detected once per provider instance from the `ver` line of the
// running config and the image type the parser derives from it. Resources ask
// it two kinds of question, both answered from the declarative table in
// capabilities.go rather than from if-chains scattered through resources:
//
//   - Feature capabilities (Has(CapVEInterfaces), Has(CapManager), ...): can
//     this device accept these commands at all? A false answer becomes a
//     plan-time error via Why.
//   - Syntax forms (Has(CapDualMode), Has(CapLegacyDHCPClient)): which of two
//     spellings does this firmware use? Resources do not branch on these
//     directly; they call the named rendering methods (DHCPClientDisable,
//     DualMode) which do.
//
// The rule for anything the provider cannot identify is "never block": an
// unparseable version makes every version-based feature capability true, and
// an undetected image makes every image-based one true. Syntax forms fall back
// to the 08.0.9x spelling, which is the current firmware family and the one
// the provider was written against.
//
// Hardware-dependent behaviour (PoE, optic support, port counts) is
// deliberately not modelled here: a Dialect knows the firmware and image, not
// the chassis or the module in a given slot, so a "capability" like inline
// power could only be guessed. Those remain per-port, apply-time concerns
// backstopped by the switch's own CLI error (see internal/resource/poe.go).
package dialect

import (
	"fmt"
	"regexp"
	"strconv"

	"github.com/pgehres/terraform-provider-fastiron-icx/internal/parser"
)

// reVersion matches the numeric major.minor.patch prefix of a FastIron release
// string as it appears on the `ver` line of the running config. Anything after
// the triple (a patch-letter build such as "m" and a T-code such as "T213") is
// ignored here: image detection from the T-code is the parser's job
// (parser.RunningConfig.ImageType), and no capability depends on the build.
var reVersion = regexp.MustCompile(`^(\d+)\.(\d+)\.(\d+)`)

// Version is a parsed FastIron release number, e.g. "08.0.95mT213" is
// {8, 0, 95}.
type Version struct {
	Major, Minor, Patch int
	// Raw is the device's version string, used verbatim in diagnostics. Detect
	// records it even when it cannot be parsed, so error text always shows
	// what the device reported.
	Raw string
}

// ParseVersion parses a FastIron release string. ok is false when the string
// does not start with a recognizable major.minor.patch triple; any trailing
// text (build letter, T-code, or something unfamiliar) is ignored.
func ParseVersion(s string) (v Version, ok bool) {
	m := reVersion.FindStringSubmatch(s)
	if m == nil {
		return Version{}, false
	}
	v.Major, _ = strconv.Atoi(m[1])
	v.Minor, _ = strconv.Atoi(m[2])
	v.Patch, _ = strconv.Atoi(m[3])
	v.Raw = s
	return v, true
}

// Less reports whether v is an earlier release than o, comparing only the
// numeric triple.
func (v Version) Less(o Version) bool {
	if v.Major != o.Major {
		return v.Major < o.Major
	}
	if v.Minor != o.Minor {
		return v.Minor < o.Minor
	}
	return v.Patch < o.Patch
}

// Family returns the release family as printed in documentation, e.g. "08.0.95".
func (v Version) Family() string {
	return fmt.Sprintf("%02d.%d.%d", v.Major, v.Minor, v.Patch)
}

// Dialect is the firmware dialect of one device.
type Dialect struct {
	Version Version
	Image   parser.ImageType
	// Known is true when Version was parsed from the device's version string.
	// Image is tracked separately: ImageTypeUnknown (or empty) means the image
	// could not be detected, independently of Known.
	Known bool

	// dualModeSeen records that the device's configuration already contains
	// `dual-mode` interface lines — proof that the firmware has the command
	// even when the version string could not be parsed.
	dualModeSeen bool
}

// Detect builds the Dialect for a version string (the `ver` line of the
// running config) and image type. It never fails: an unparseable version
// yields a Dialect with Known = false, which is permissive. The raw string is
// kept either way so diagnostics show the device's actual version.
func Detect(versionString string, image parser.ImageType) Dialect {
	v, ok := ParseVersion(versionString)
	v.Raw = versionString
	if image == "" {
		image = parser.ImageTypeUnknown
	}
	return Dialect{Version: v, Image: image, Known: ok}
}

// FromConfig detects the Dialect of the device a running config came from.
func FromConfig(c *parser.RunningConfig) Dialect {
	return Dialect{}.Observe(c)
}

// Observe refines d with evidence from a freshly read running config: when d
// has no parsed version (or no detected image) the config's own `ver` line and
// structure fill them in, and `dual-mode` lines in the config prove CapDualMode.
// A Dialect that is already Known keeps its version and image, so the
// Configure-time detection stays authoritative.
func (d Dialect) Observe(c *parser.RunningConfig) Dialect {
	if c == nil {
		return d
	}
	if !d.Known {
		if v, ok := ParseVersion(c.Global.Version); ok {
			d.Version, d.Known = v, true
		} else if d.Version.Raw == "" {
			d.Version.Raw = c.Global.Version
		}
	}
	if d.Image == "" || d.Image == parser.ImageTypeUnknown {
		d.Image = c.ImageType()
	}
	if c.HasDualModeLines() {
		d.dualModeSeen = true
	}
	return d
}

// Firmware returns the device's version string for diagnostics (verbatim, even
// when it could not be parsed), or "unknown" when the device reported none.
func (d Dialect) Firmware() string {
	if d.Version.Raw != "" {
		return d.Version.Raw
	}
	return "unknown"
}

// switchImage is true only when the device is positively identified as
// running the switch image.
func (d Dialect) switchImage() bool { return d.Image == parser.ImageTypeSwitch }

// routerImage is true only when the device is positively identified as
// running the router image.
func (d Dialect) routerImage() bool { return d.Image == parser.ImageTypeRouter }

// before reports whether the device is positively known to run a release
// earlier than v. Unknown versions are never "before" anything.
func (d Dialect) before(v Version) bool { return d.Known && d.Version.Less(v) }
