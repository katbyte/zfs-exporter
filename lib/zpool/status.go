// Package zpool reads what ZFS's own programs print about the pools: the JSON zpool status writes, and the lines zpool_influxdb writes. It only reads
// text; running the programs is lib/command's, and finding each disk lib/host's.
package zpool

import (
	"bytes"
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"time"

	"github.com/katbyte/zfs-exporter/lib/poll"
)

// StatusArgs is what zpool status is asked: every pool, with each disk's slow requests, numbers as plain numbers, as JSON.
var StatusArgs = []string{"status", "-s", "-p", "-j", "--json-int"}

// number is a number zpool status writes, as a JSON number with --json-int and as a string of digits without it. It is kept as the digits, since a
// GUID is larger than a float holds exactly.
type number string

func (n *number) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		s = string(b)
	}

	*n = number(s)

	return nil
}

// count is the number as a count, and nil when it is missing or not a whole number.
func (n *number) count() *uint64 {
	if n == nil {
		return nil
	}

	var v uint64
	if _, err := fmt.Sscan(string(*n), &v); err != nil {
		return nil
	}

	return &v
}

// value is the number as a count, and 0 when it is missing or not a whole number.
func (n *number) value() uint64 {
	if v := n.count(); v != nil {
		return *v
	}

	return 0
}

// time is the number as a time in seconds since 1970, and the zero time when it is missing or 0, which ZFS writes for a time that has not come.
func (n *number) time() time.Time {
	v := n.value()
	if v == 0 {
		return time.Time{}
	}

	return time.Unix(int64(v), 0) //nolint:gosec // a time ZFS writes is seconds since 1970, far short of where it would overflow
}

// vdevJSON is one vdev as zpool status writes it.
type vdevJSON struct {
	Name    string   `json:"name"`
	Type    string   `json:"vdev_type"`
	GUID    number   `json:"guid"`
	Path    string   `json:"path"`
	Class   string   `json:"class"`
	State   string   `json:"state"`
	SlowIOs *number  `json:"slow_ios"`
	Vdevs   vdevList `json:"vdevs"`

	Allocated *number `json:"alloc_space"`
	Size      *number `json:"total_space"`
	Usable    *number `json:"def_space"`

	ReadErrors     *number `json:"read_errors"`
	WriteErrors    *number `json:"write_errors"`
	ChecksumErrors *number `json:"checksum_errors"`
}

// space is how big the vdev is and how much of it is used, and nil when zpool status does not say, as for a disk inside another.
func (v *vdevJSON) space() *poll.Space {
	if v.Size.count() == nil {
		return nil
	}

	return &poll.Space{Size: v.Size.value(), Allocated: v.Allocated.value(), Usable: v.Usable.value()}
}

// errors is the errors ZFS has counted of the vdev, and nil when zpool status does not say, as for a spare.
func (v *vdevJSON) errors() *poll.Errors {
	if v.ReadErrors.count() == nil && v.WriteErrors.count() == nil && v.ChecksumErrors.count() == nil {
		return nil
	}

	return &poll.Errors{Read: v.ReadErrors.value(), Write: v.WriteErrors.value(), Checksum: v.ChecksumErrors.value()}
}

// scanJSON is a pool's last scrub or resilver as zpool status writes it.
type scanJSON struct {
	Function   string  `json:"function"`
	State      string  `json:"state"`
	Start      *number `json:"start_time"`
	End        *number `json:"end_time"`
	Paused     *number `json:"scrub_pause"`
	ToExamine  *number `json:"to_examine"`
	Examined   *number `json:"examined"`
	Skipped    *number `json:"skipped"`
	Issued     *number `json:"issued"`
	Processed  *number `json:"processed"`
	Errors     *number `json:"errors"`
	PassStart  *number `json:"pass_start"`
	PassIssued *number `json:"issued_bytes_per_scan"`
	PassPaused *number `json:"scrub_spent_paused"`
}

// scan is the scan as read, and nil for none: a pool that has had no scrub or resilver says so with a function of NONE, or by saying nothing.
func (s *scanJSON) scan() *poll.Scan {
	if s == nil || (s.Function != poll.ScanScrub && s.Function != poll.ScanResilver) {
		return nil
	}

	out := poll.Scan{
		Function:   s.Function,
		State:      s.State,
		Start:      s.Start.time(),
		Paused:     s.Paused.time(),
		ToExamine:  s.ToExamine.value(),
		Examined:   s.Examined.value(),
		Skipped:    s.Skipped.value(),
		Issued:     s.Issued.value(),
		Processed:  s.Processed.value(),
		Errors:     s.Errors.value(),
		PassStart:  s.PassStart.time(),
		PassIssued: s.PassIssued.value(),
		PassPaused: time.Duration(s.PassPaused.value()) * time.Second, //nolint:gosec // seconds spent paused, far short of where it would overflow
	}

	// ZFS keeps the last end while the next scan runs
	if !out.Running() {
		out.End = s.End.time()
	}

	return &out
}

// vdevList is the vdevs in a JSON object keyed by name, in the order written: a disk's place among its siblings is its number, which is what
// zpool_influxdb names it by.
type vdevList []vdevJSON

func (l *vdevList) UnmarshalJSON(b []byte) error {
	dec := json.NewDecoder(bytes.NewReader(b))
	if _, err := dec.Token(); err != nil {
		return err
	}

	for dec.More() {
		// the key is the vdev's name, which it also has inside
		if _, err := dec.Token(); err != nil {
			return err
		}

		var v vdevJSON
		if err := dec.Decode(&v); err != nil {
			return err
		}
		*l = append(*l, v)
	}

	return nil
}

// sections is the parts of a pool that hold top-level vdevs, in the order they are listed: vdevs holds the pool's root, under which are the ones that
// hold data, and the rest hold one class each. A part of another name that holds vdevs is listed after these.
var sections = []string{"vdevs", "special", "dedup", "logs", "l2cache", "spares"}

// classes is the class label for each class zpool status names.
var classes = map[string]string{
	"normal":  poll.ClassData,
	"special": poll.ClassSpecial,
	"dedup":   poll.ClassDedup,
	"log":     poll.ClassLog,
	"logs":    poll.ClassLog,
	"l2cache": poll.ClassCache,
	"spare":   poll.ClassSpare,
	"spares":  poll.ClassSpare,
}

// Status reads what zpool status -j wrote: every pool, in order of name, each with its top-level vdevs of every class.
func Status(out []byte) ([]poll.Pool, error) {
	var doc struct {
		Pools map[string]json.RawMessage `json:"pools"`
	}
	if err := json.Unmarshal(out, &doc); err != nil {
		return nil, fmt.Errorf("reading what zpool status wrote: %w", err)
	}
	if doc.Pools == nil {
		return nil, errors.New("reading what zpool status wrote: it lists no pools, not even as none")
	}

	pools := make([]poll.Pool, 0, len(doc.Pools))
	for _, name := range slices.Sorted(maps.Keys(doc.Pools)) {
		pool, err := poolOf(name, doc.Pools[name])
		if err != nil {
			return nil, fmt.Errorf("reading what zpool status wrote of %s: %w", name, err)
		}
		pools = append(pools, pool)
	}

	return pools, nil
}

// poolOf is one pool from what zpool status wrote of it.
func poolOf(name string, raw json.RawMessage) (poll.Pool, error) {
	var head struct {
		Name       string    `json:"name"`
		State      string    `json:"state"`
		GUID       number    `json:"pool_guid"`
		DataErrors *number   `json:"error_count"`
		Scan       *scanJSON `json:"scan_stats"`
	}
	if err := json.Unmarshal(raw, &head); err != nil {
		return poll.Pool{}, err
	}

	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return poll.Pool{}, err
	}

	pool := poll.Pool{Name: cmp.Or(head.Name, name), GUID: string(head.GUID), State: head.State, DataErrors: head.DataErrors.count(), Scan: head.Scan.scan()}

	// the other parts that hold vdevs come after the known ones, in order of name
	order := slices.Clone(sections)
	for _, key := range slices.Sorted(maps.Keys(fields)) {
		if !slices.Contains(order, key) && holdsVdevs(fields[key]) {
			order = append(order, key)
		}
	}

	for _, section := range order {
		raw, found := fields[section]
		if !found {
			continue
		}

		var list vdevList
		if err := json.Unmarshal(raw, &list); err != nil {
			return poll.Pool{}, fmt.Errorf("the %s: %w", section, err)
		}

		// the pool's root is not a vdev of its own here: its space and errors are the pool's, and what is under it is the pool's vdevs
		top := []vdevJSON(list)
		if section == "vdevs" {
			top = nil
			for i := range list {
				pool.Space, pool.Errors = list[i].space(), list[i].errors()
				top = append(top, list[i].Vdevs...)
			}
		}

		for i := range top {
			pool.Vdevs = append(pool.Vdevs, vdevOf(&top[i], classes[section]))
		}
	}

	return pool, nil
}

// holdsVdevs says whether a part of a pool is an object of vdevs, each with a kind.
func holdsVdevs(raw json.RawMessage) bool {
	var list vdevList
	if err := json.Unmarshal(raw, &list); err != nil || len(list) == 0 {
		return false
	}

	return !slices.ContainsFunc(list, func(v vdevJSON) bool { return v.Type == "" })
}

// vdevOf is one vdev and those within it. A vdev that does not say its class is in its section's.
func vdevOf(v *vdevJSON, class string) poll.Vdev {
	if named, found := classes[v.Class]; found {
		class = named
	} else if v.Class != "" {
		class = v.Class
	}
	if class == "" {
		class = poll.ClassData
	}

	out := poll.Vdev{Name: v.Name, Type: v.Type, Class: class, GUID: string(v.GUID), State: v.State, Space: v.space(), Errors: v.errors(), Path: v.Path, SlowIOs: v.SlowIOs.count()}
	for i := range v.Vdevs {
		out.Children = append(out.Children, vdevOf(&v.Vdevs[i], class))
	}

	return out
}
