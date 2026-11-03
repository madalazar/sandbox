package model

import (
	"fmt"
	"math/big"
	"strings"
)

// represents a contiguous range of cache ways
type WayInterval struct {
	Start  int64
	Length int64
}

// returns the one-past-the-end index of the interval
func (w WayInterval) End() int64 {
	return w.Start + w.Length
}

// reports whether w and other have any ways in common
func (w WayInterval) Overlaps(other WayInterval) bool {
	return w.Start < other.End() && other.Start < w.End()
}

// ParseContiguousWayMask parses a hex or decimal bitmask string into a contiguous WayInterval
// within [0, maxWays). Returns an error if the mask is invalid, empty/zero, out of bounds,
// or has non-contiguous set bits (e.g. 0xF00F).
func ParseContiguousWayMask(mask string, maxWays int64) (WayInterval, error) {
	if maxWays <= 0 {
		return WayInterval{}, fmt.Errorf("maxWays must be > 0, got %d", maxWays)
	}

	trimmed := strings.TrimSpace(mask)
	if trimmed == "" {
		return WayInterval{}, fmt.Errorf("cache mask is empty")
	}

	parsed := new(big.Int)
	if _, ok := parsed.SetString(trimmed, 0); !ok || parsed.Sign() <= 0 {
		return WayInterval{}, fmt.Errorf("invalid cache mask %q", mask)
	}

	if int64(parsed.BitLen()) > maxWays {
		return WayInterval{}, fmt.Errorf("cache mask %q exceeds max ways %d", mask, maxWays)
	}

	start := int64(-1)
	length := int64(0)
	gapAfterStart := false

	for bit := range maxWays {
		if parsed.Bit(int(bit)) == 1 {
			if gapAfterStart {
				return WayInterval{}, fmt.Errorf("cache mask %q is not a contiguous way interval", mask)
			}
			if start == -1 {
				start = bit
			}
			length++
		} else if start != -1 {
			gapAfterStart = true
		}
	}

	if length == 0 {
		return WayInterval{}, fmt.Errorf("cache mask %q has no ways set", mask)
	}

	return WayInterval{Start: start, Length: length}, nil
}

// FreeWayIntervals scans a boolean bitmap of used ways and returns all contiguous free (false) intervals.
func FreeWayIntervals(used []bool) []WayInterval {
	intervals := make([]WayInterval, 0)
	start := int64(-1)

	for i := int64(0); i < int64(len(used)); i++ {
		if !used[i] {
			if start == -1 {
				start = i
			}
			continue
		}

		if start != -1 {
			intervals = append(intervals, WayInterval{Start: start, Length: i - start})
			start = -1
		}
	}

	if start != -1 {
		intervals = append(intervals, WayInterval{Start: start, Length: int64(len(used)) - start})
	}

	return intervals
}

// identifies one reserved slot in the device-wide class-of-service pool.
// the pool is shared hardware; only the spelling is runtime-specific:
// - pqos uses "COS" + index
// - resctrl a control-group name
// both are strings wherever they are applied, so one opaque type covers both
type ClosId string

// means no slot is held. "0" is COS0, a real class that is never
// allocated, so the two can no longer be conflated
const ClassUnset ClosId = ""

// reports whether a class slot is currently reserved
func (c ClosId) Held() bool { return c != ClassUnset }

// returns the string representation of the class identifier
func (c ClosId) String() string { return string(c) }

// the device-wide hardware limit on classes of service
// resctrl exposes num_closids per level; the usable count is the minimum
// across the levels present
// pqos and rdt draw from this same pool
type ClosPool struct {
	NumClos  int // from the topology artifact
	Reserved int // slots the agent does not own, e.g. resctrl's default group
}

// returns the number of assignable classes of service.
func (p ClosPool) Usable() int {
	if p.NumClos <= p.Reserved {
		return 0
	}
	return p.NumClos - p.Reserved
}

// device inventory the ledger needs to answer "what is free"
// Both fields come from the topology artifact
type CacheCapacity struct {
	Ways     map[string]int64
	ClosPool ClosPool
}

// represents a planned cache assignment for one component on one cache domain
type CacheAssignment struct {
	Owner    OwnerRef
	Level    string
	CacheId  string
	SizeKiB  int64
	Interval WayInterval
	Mask     string
	Clos     ClosId
}

// represents what cache planning decided for one component
type CachePlan struct {
	Component         ComponentRef
	L3CacheAssignment *CacheAssignment
}

// reports whether the plan contains any cache allocations
func (p CachePlan) HasCache() bool {
	return p.L3CacheAssignment != nil
}
