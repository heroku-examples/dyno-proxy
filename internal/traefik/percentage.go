package traefik

import (
	"fmt"
	"strings"
)

// UUIDPercentagePatterns maps percentage values to regex patterns that match
// the last two hex characters of a UUID. Used for percentage-based routing via
// x-request-id header matching in percentage routes. Ported verbatim from
// shared.go:20.
var UUIDPercentagePatterns = map[int]string{
	1:  `0[0-2]$`,
	5:  `0[0-9a-c]$`,
	10: `(0[0-9a-f]|1[0-9])$`,
	15: `(0[0-9a-f]|1[0-9a-f]|2[0-6])$`,
	20: `([0-2][0-9a-f]|3[0-3])$`,
	25: `[0-3][0-9a-f]$`,
	30: `([0-3][0-9a-f]|4[0-9a-c])$`,
	33: `([0-4][0-9a-f]|5[0-4])$`,
	35: `([0-4][0-9a-f]|5[0-9])$`,
	40: `([0-5][0-9a-f]|6[0-6])$`,
	45: `([0-6][0-9a-f]|7[0-3])$`,
	50: `[0-7][0-9a-f]$`,
	55: `([0-7][0-9a-f]|8[0-9a-c])$`,
	60: `([0-8][0-9a-f]|9[0-9])$`,
	65: `([0-9][0-9a-f]|a[0-6])$`,
	66: `([0-9][0-9a-f]|a[0-8])$`,
	70: `([0-9a][0-9a-f]|b[0-3])$`,
	75: `[0-9a-b][0-9a-f]$`,
	80: `([0-9a-b][0-9a-f]|c[0-9a-c])$`,
	85: `([0-9a-c][0-9a-f]|d[0-9])$`,
	90: `([0-9a-d][0-9a-f]|e[0-6])$`,
	95: `([0-9a-e][0-9a-f]|f[0-3])$`,
	99: `([0-9a-e][0-9a-f]|f[0-9a-c])$`,
}

// getUUIDPercentagePattern returns the regex pattern for a given percentage. If
// an exact match doesn't exist, it returns the pattern for the closest available
// percentage that is greater than or equal to the requested value. Returns empty
// string if percentage is <= 0 or >= 100. Ported from shared.go:50.
func getUUIDPercentagePattern(percentage int) string {
	if percentage <= 0 || percentage >= 100 {
		return ""
	}
	if pattern, exists := UUIDPercentagePatterns[percentage]; exists {
		return pattern
	}
	closestPercentage := 101
	for p := range UUIDPercentagePatterns {
		if p >= percentage && p < closestPercentage {
			closestPercentage = p
		}
	}
	if closestPercentage <= 100 {
		return UUIDPercentagePatterns[closestPercentage]
	}
	return ""
}

// getUUIDPercentageRangePattern returns a regex pattern matching UUIDs in the
// percentage range [startPct, endPct). Used for cumulative percentage routing
// when multiple tests share the same path/method matchers. Ported from
// shared.go:83.
func getUUIDPercentageRangePattern(startPct, endPct int) string {
	if startPct < 0 || endPct > 100 || startPct >= endPct {
		return ""
	}
	startHex := (startPct * 256) / 100
	endHex := ((endPct * 256) / 100) - 1
	if endHex < startHex {
		endHex = startHex
	}
	return buildHexRangePattern(startHex, endHex)
}

// buildHexRangePattern generates a regex pattern matching hex values in
// [start, end]. Ported from shared.go:103.
func buildHexRangePattern(start, end int) string {
	if start < 0 || end > 255 || start > end {
		return ""
	}
	if start == end {
		return fmt.Sprintf("%02x$", start)
	}

	startHi := start / 16
	startLo := start % 16
	endHi := end / 16
	endLo := end % 16

	if startHi == endHi {
		if startLo == 0 && endLo == 15 {
			return fmt.Sprintf("%x[0-9a-f]$", startHi)
		}
		return fmt.Sprintf("%x[%x-%x]$", startHi, startLo, endLo)
	}

	var parts []string

	if startLo == 0 {
		parts = append(parts, fmt.Sprintf("%x[0-9a-f]", startHi))
	} else if startLo <= 9 {
		parts = append(parts, fmt.Sprintf("%x[%x-9a-f]", startHi, startLo))
	} else {
		parts = append(parts, fmt.Sprintf("%x[%x-f]", startHi, startLo))
	}

	if endHi-startHi > 1 {
		if endHi-startHi == 2 {
			parts = append(parts, fmt.Sprintf("%x[0-9a-f]", startHi+1))
		} else {
			midStart := startHi + 1
			midEnd := endHi - 1
			if midStart <= 9 && midEnd <= 9 {
				parts = append(parts, fmt.Sprintf("[%d-%d][0-9a-f]", midStart, midEnd))
			} else if midStart >= 10 {
				parts = append(parts, fmt.Sprintf("[%x-%x][0-9a-f]", midStart, midEnd))
			} else {
				parts = append(parts, fmt.Sprintf("[%d-9][0-9a-f]", midStart))
				if midEnd >= 10 {
					parts = append(parts, fmt.Sprintf("[a-%x][0-9a-f]", midEnd))
				}
			}
		}
	}

	if endLo == 15 {
		parts = append(parts, fmt.Sprintf("%x[0-9a-f]", endHi))
	} else if endLo >= 10 {
		parts = append(parts, fmt.Sprintf("%x[0-9a-%x]", endHi, endLo))
	} else {
		parts = append(parts, fmt.Sprintf("%x[0-%x]", endHi, endLo))
	}

	return fmt.Sprintf("(%s)$", strings.Join(parts, "|"))
}
