// Package skip is the parts of what zfs-exporter reports that can be left out, by the names the settings give them. A part is either a level, the
// pools, vdevs or disks, or a kind of reading; a reading is on the page only when neither its level nor its kind is left out. What is left out is
// not asked for either, where that saves a question.
package skip

import (
	"fmt"
	"slices"
	"strings"
)

// Part is one part of what is reported, by the name the settings give it.
type Part string

// The levels: each pool as a whole, its top-level vdevs, and its disks.
const (
	Pools Part = "pools"
	Vdevs Part = "vdevs"
	Disks Part = "disks"
)

// The kinds of reading.
const (
	// Poll is how the asking goes: how often each round runs, how long it took, when it last read everything, how often it failed, and how the
	// programs it runs are going. Nothing is asked for it, so leaving it out saves only the page.
	Poll Part = "poll"
	// Latency is how long requests took, as histograms: all of them for each pool and vdev, the time at the disk for each disk, and all of them for
	// each disk too when every disk timing is asked for.
	Latency Part = "latency"
	// RequestSizes is how big requests were, as histograms, for each pool and vdev, and each disk when every disk timing is asked for.
	RequestSizes Part = "request-sizes"
	// Queues is how many requests each pool has with its disks and waiting.
	Queues Part = "queues"
	// Operations is how many reads and writes each pool and vdev has done, and how many bytes they moved, and each disk when every disk timing is
	// asked for.
	Operations Part = "operations"
	// SlowIOs is how many requests each disk took longer than ZFS's limit to answer.
	SlowIOs Part = "slow-ios"
	// Space is how big each pool and top-level vdev is, how much of it is used, and how broken up each vdev's free space is.
	Space Part = "space"
	// Health is the state of each pool, vdev and disk, the read, write and checksum errors counted of each, and each pool's damaged data.
	Health Part = "health"
	// Scrub and Resilver are each pool's last scrub or resilver: whether it is running, how far along it is, how fast it goes and what it found.
	Scrub    Part = "scrub"
	Resilver Part = "resilver"
)

// Levels and Kinds are every part of each group, in the order they are listed.
var (
	Levels = []Part{Pools, Vdevs, Disks}
	Kinds  = []Part{Poll, Latency, RequestSizes, Queues, Operations, SlowIOs, Space, Health, Scrub, Resilver}
)

// Parts is every part, in the order they are listed.
var Parts = slices.Concat(Levels, Kinds)

// Set is the parts left out. The empty set leaves nothing out.
type Set map[Part]bool

// Parse reads a list of parts to leave out, with a comma between: disks,request-sizes. A name that is no part is refused rather than left to leave
// nothing out unnoticed.
func Parse(list string) (Set, error) {
	s := Set{}
	for named := range strings.SplitSeq(list, ",") {
		part := Part(strings.ToLower(strings.TrimSpace(named)))
		if part == "" {
			continue
		}
		if !slices.Contains(Parts, part) {
			return nil, fmt.Errorf("%q is not one of the parts: %s", named, s.list(Parts))
		}

		s[part] = true
	}

	return s, nil
}

// AllBut reads a list of the only parts to ask about, as --metrics gives it, into the parts that leaves out. Levels and kinds are taken apart: naming
// a level leaves out the other levels, naming a kind the other kinds, so --metrics latency is the latency of every level and --metrics disks,latency
// that of the disks alone. Nothing named leaves nothing out.
func AllBut(list string) (Set, error) {
	named, err := Parse(list)
	if err != nil || len(named) == 0 {
		return named, err
	}

	s := Set{}
	for _, group := range [][]Part{Levels, Kinds} {
		if !slices.ContainsFunc(group, named.Has) {
			continue
		}

		for _, part := range group {
			if !named.Has(part) {
				s[part] = true
			}
		}
	}

	return s, nil
}

// Has says whether a part is left out.
func (s Set) Has(part Part) bool { return s[part] }

// Shows says whether readings of this level and kind are on the page: neither is left out.
func (s Set) Shows(level, kind Part) bool { return !s[level] && !s[kind] }

// String is the parts left out, as a list the settings would take.
func (s Set) String() string {
	var named []Part
	for _, part := range Parts {
		if s[part] {
			named = append(named, part)
		}
	}

	return s.list(named)
}

func (Set) list(parts []Part) string {
	names := make([]string, 0, len(parts))
	for _, part := range parts {
		names = append(names, string(part))
	}

	return strings.Join(names, ", ")
}
