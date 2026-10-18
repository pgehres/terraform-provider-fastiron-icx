package parser

import "regexp"

// ImageType identifies which FastIron firmware image a device is running.
// Every FastIron release ships as two binaries per platform: the switch image
// (SPS…, Layer 2 only) and the router image (SPR…, adds the Layer 3 plane).
type ImageType string

const (
	ImageTypeRouter  ImageType = "router"
	ImageTypeSwitch  ImageType = "switch"
	ImageTypeUnknown ImageType = "unknown"
)

// reImageCode matches the trailing T-code of a FastIron version string
// (e.g. "08.0.95rT211", "08.0.95qT213"). The final digit identifies the
// image: …1 = switch image (SPS), …3 = router image (SPR). Codes ending in
// any other digit (e.g. boot-monitor T215) are left to the structural
// fallback rather than guessed at.
var reImageCode = regexp.MustCompile(`T\d*([13])$`)

// ImageType derives the firmware image type of the device this configuration
// came from. The version string's T-code suffix is authoritative when it is
// recognized; otherwise Layer 3 configuration (VE interfaces, static routes,
// router-interface, global-stp) marks the router image, and a global
// ip address / ip default-gateway — which exist only on the switch image —
// marks the switch image. A config with neither signal is ImageTypeUnknown.
func (c *RunningConfig) ImageType() ImageType {
	if m := reImageCode.FindStringSubmatch(c.Global.Version); m != nil {
		if m[1] == "3" {
			return ImageTypeRouter
		}
		return ImageTypeSwitch
	}

	if len(c.VEInterfaces) > 0 || len(c.Global.StaticRoutes) > 0 || c.Global.GlobalSTP {
		return ImageTypeRouter
	}
	for i := range c.VLANs {
		if c.VLANs[i].RouterInterface != nil {
			return ImageTypeRouter
		}
	}

	if c.Global.IPAddress != "" || c.Global.DefaultGateway != "" {
		return ImageTypeSwitch
	}

	return ImageTypeUnknown
}
