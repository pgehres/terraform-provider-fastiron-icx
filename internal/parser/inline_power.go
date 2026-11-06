package parser

import (
	"regexp"
	"strconv"
	"strings"
)

// InlinePowerStatus is one port's row of `show inline power`.
type InlinePowerStatus struct {
	Port string
	// AdminOn is the Admin State column: PoE is enabled on the port.
	AdminOn bool
	// OperOn is the Oper State column: a powered device draws power now.
	OperOn bool
	// ConsumedMW and AllocatedMW are the Power(mWatts) columns.
	ConsumedMW  int64
	AllocatedMW int64
}

// inlinePowerRowRe matches the fixed leading columns of a `show inline power`
// row: port, admin state, oper state, consumed and allocated milliwatts. The
// later columns (PD type, PD class, priority, fault) can hold free text such
// as "n/a" or a class name, so they are not parsed.
var inlinePowerRowRe = regexp.MustCompile(`^\s*(\d+/\d+/\d+)\s+(On|Off)\s+(On|Off)\s+(\d+)\s+(\d+)\b`)

// ParseInlinePower parses the per-port rows of `show inline power` or
// `show inline power <port>` output (captured on an ICX 7450 with FastIron
// 09.0.10). Headers, separators and any other lines are skipped. The result
// is keyed by port.
func ParseInlinePower(output string) map[string]InlinePowerStatus {
	rows := make(map[string]InlinePowerStatus)
	for _, line := range strings.Split(output, "\n") {
		m := inlinePowerRowRe.FindStringSubmatch(strings.TrimRight(line, "\r"))
		if m == nil {
			continue
		}
		consumed, err1 := strconv.ParseInt(m[4], 10, 64)
		allocated, err2 := strconv.ParseInt(m[5], 10, 64)
		if err1 != nil || err2 != nil {
			continue
		}
		rows[m[1]] = InlinePowerStatus{
			Port:        m[1],
			AdminOn:     m[2] == "On",
			OperOn:      m[3] == "On",
			ConsumedMW:  consumed,
			AllocatedMW: allocated,
		}
	}
	return rows
}
