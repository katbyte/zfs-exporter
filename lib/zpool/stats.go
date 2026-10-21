package zpool

import (
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/katbyte/zfs-exporter/lib/poll"
)

// StatsArgs is what zpool_influxdb is asked: every pool, with each histogram's buckets counting everything at or below them, as Prometheus keeps
// them. NoHistograms leaves the histograms out, when none is wanted.
var StatsArgs = []string{"--sum-histogram-buckets"}

const NoHistograms = "--no-histograms"

// The measurements zpool_influxdb writes that are read. Each pool's queues are written twice, under two names: the first is written with or without
// histograms, and its values end in _queue.
const (
	measureOperations = "zpool_stats"
	measureLatency    = "zpool_latency"
	measureSizes      = "zpool_io_size"
	measureQueues     = "zpool_vdev_stats"
	measureQueuesToo  = "zpool_vdev_queue"
)

// The tags zpool_influxdb writes on each line that are read.
const (
	tagPool  = "name"
	tagVdev  = "vdev"
	tagPath  = "path"
	tagBound = "le"
)

// waits is the kind of wait and the way each of zpool_influxdb's latency histograms is of. Scrubs read, and trims and rebuilds write, as ZFS's own
// request sizes say. A histogram not listed here is one this program does not know, and is left out.
var waits = map[string][2]string{
	"total_read":  {poll.WaitTotal, poll.OpRead},
	"total_write": {poll.WaitTotal, poll.OpWrite},
	"disk_read":   {poll.WaitDisk, poll.OpRead},
	"disk_write":  {poll.WaitDisk, poll.OpWrite},
	"sync_read":   {poll.WaitSyncQueue, poll.OpRead},
	"sync_write":  {poll.WaitSyncQueue, poll.OpWrite},
	"async_read":  {poll.WaitAsyncQueue, poll.OpRead},
	"async_write": {poll.WaitAsyncQueue, poll.OpWrite},
	"scrub":       {poll.WaitScrubQueue, poll.OpRead},
	"trim":        {poll.WaitTrimQueue, poll.OpWrite},
	"rebuild":     {poll.WaitRebuildQueue, poll.OpWrite},
}

// queues is the queue and the way each of zpool_influxdb's queue counts is of, by the start of its name, sync_r_active and async_scrub_pend, in the
// order it writes them.
var queues = []struct {
	start, queue, op string
}{
	{"sync_r", poll.QueueSync, poll.OpRead},
	{"sync_w", poll.QueueSync, poll.OpWrite},
	{"async_r", poll.QueueAsync, poll.OpRead},
	{"async_w", poll.QueueAsync, poll.OpWrite},
	{"async_scrub", poll.QueueScrub, poll.OpRead},
	{"rebuild", poll.QueueRebuild, poll.OpWrite},
}

// point is one line of a histogram: the bucket it ends at, as the power of two of nanoseconds or bytes ZFS counts its buckets in, and how many
// requests were in it or below. The last line has no bucket of its own, only everything.
type point struct {
	power int
	all   bool
	count uint64
}

// vdevBuild is what has been read of one vdev so far.
type vdevBuild struct {
	stats   poll.VdevStats
	latency map[string][]point
	sizes   map[string][]point
	// order is each histogram's name in the order first read, so the page keeps one order
	order []string
}

// poolBuild is what has been read of one pool so far.
type poolBuild struct {
	name   string
	vdevs  []*vdevBuild
	byName map[string]*vdevBuild
	queues []poll.Queue
}

// vdev is the vdev a line is of, started the first time it is seen.
func (p *poolBuild) vdev(l *line) *vdevBuild {
	name := l.tags[tagVdev]
	v, found := p.byName[name]
	if !found {
		v = &vdevBuild{stats: poll.VdevStats{Name: name, Path: l.tags[tagPath]}, latency: map[string][]point{}, sizes: map[string][]point{}}
		p.byName[name] = v
		p.vdevs = append(p.vdevs, v)
	}

	return v
}

// Stats reads what zpool_influxdb wrote: every pool and vdev in the order written. A line that cannot be read is an error, since the rest would be
// as suspect.
func Stats(out []byte) (poll.Stats, error) {
	var order []*poolBuild
	pools := map[string]*poolBuild{}

	for number, text := range strings.Split(string(out), "\n") {
		if text = strings.TrimSpace(text); text == "" {
			continue
		}

		l, err := parseLine(text)
		if err != nil {
			return poll.Stats{}, fmt.Errorf("reading line %d of what zpool_influxdb wrote: %w", number+1, err)
		}

		name := l.tags[tagPool]
		if name == "" || l.tags[tagVdev] == "" {
			continue
		}

		p, found := pools[name]
		if !found {
			p = &poolBuild{name: name, byName: map[string]*vdevBuild{}}
			pools[name] = p
			order = append(order, p)
		}

		if err := p.read(&l); err != nil {
			return poll.Stats{}, fmt.Errorf("reading line %d of what zpool_influxdb wrote: %w", number+1, err)
		}
	}

	stats := poll.Stats{Pools: make([]poll.PoolStats, 0, len(order))}
	for _, p := range order {
		stats.Pools = append(stats.Pools, p.done())
	}

	return stats, nil
}

// read takes in one line of the pool's.
func (p *poolBuild) read(l *line) error {
	switch l.measurement {
	case measureOperations:
		v := p.vdev(l)
		reads, _ := l.count("read_ops")
		writes, _ := l.count("write_ops")
		readBytes, _ := l.count("read_bytes")
		writtenBytes, _ := l.count("write_bytes")
		v.stats.Operations = &poll.Operations{Reads: reads, Writes: writes, ReadBytes: readBytes, WrittenBytes: writtenBytes}

		// ZFS writes the most a number holds for a vdev with no space of its own to break up
		if fragmentation, found := l.count("fragmentation"); found && fragmentation <= 100 {
			v.stats.Fragmentation = &fragmentation
		}

	case measureLatency, measureSizes:
		latency := l.measurement == measureLatency
		at, err := bound(l.tags[tagBound], latency)
		if err != nil {
			return err
		}

		v := p.vdev(l)
		into := v.sizes
		if latency {
			into = v.latency
		}

		for _, field := range l.names {
			count, ok := l.count(field)
			if !ok {
				continue
			}
			if _, seen := into[field]; !seen {
				v.order = append(v.order, field)
			}
			at.count = count
			into[field] = append(into[field], at)
		}

	case measureQueues, measureQueuesToo:
		// the same counts come twice, once under each name: the first read is kept
		if len(p.queues) == 0 && l.tags[tagVdev] == poll.Root {
			p.queues = queuesOf(l)
		}
	}

	return nil
}

// bound is the bucket a histogram's line ends at. zpool_influxdb writes each bucket's bottom where its top belongs, so the power of two it writes is
// the bucket's: one at 0.000004 seconds is the bucket of 4096ns to 8191ns.
func bound(le string, latency bool) (point, error) {
	if le == "+Inf" {
		return point{all: true}, nil
	}

	value, err := strconv.ParseFloat(le, 64)
	if err != nil || value <= 0 {
		return point{}, fmt.Errorf("%q is not a histogram's bound", le)
	}

	// a latency is written in seconds with six places, which rounds the small ones: 0.000033 is 32768ns
	if latency {
		value *= 1e9
	}

	return point{power: int(math.Round(math.Log2(value)))}, nil
}

// queuesOf is the queue counts on one line, in the order zpool_influxdb writes them.
func queuesOf(l *line) []poll.Queue {
	var out []poll.Queue
	for _, q := range queues {
		active, found := l.count(q.start + "_active")
		if !found {
			active, found = l.count(q.start + "_active_queue")
		}
		pending, foundToo := l.count(q.start + "_pend")
		if !foundToo {
			pending, foundToo = l.count(q.start + "_pend_queue")
		}
		if !found && !foundToo {
			continue
		}

		out = append(out, poll.Queue{Queue: q.queue, Op: q.op, Active: active, Pending: pending})
	}

	return out
}

// done is the pool as read.
func (p *poolBuild) done() poll.PoolStats {
	out := poll.PoolStats{Name: p.name, Queues: p.queues, Vdevs: make([]poll.VdevStats, 0, len(p.vdevs))}
	for _, v := range p.vdevs {
		for _, field := range v.order {
			if points, found := v.latency[field]; found {
				if which, known := waits[field]; known {
					v.stats.Latency = append(v.stats.Latency, poll.Latency{Wait: which[0], Op: which[1], Histogram: histogram(points, true)})
				}
			}

			if points, found := v.sizes[field]; found {
				if size, known := sizeOf(field); known {
					size.Histogram = histogram(points, false)
					v.stats.Sizes = append(v.stats.Sizes, size)
				}
			}
		}

		out.Vdevs = append(out.Vdevs, v.stats)
	}

	return out
}

// sizeOf is what one of zpool_influxdb's request size histograms is of, by its name: queue, way, and ind for requests as they were asked for or agg
// for ones ZFS joined together. A name not written that way is not known.
func sizeOf(field string) (poll.Size, bool) {
	parts := strings.Split(field, "_")
	if len(parts) != 3 || (parts[1] != poll.OpRead && parts[1] != poll.OpWrite) || (parts[2] != "ind" && parts[2] != "agg") {
		return poll.Size{}, false
	}

	return poll.Size{Queue: parts[0], Op: parts[1], Aggregated: parts[2] == "agg"}, true
}

// histogram is a histogram from its lines, each bucket ending at its true top: the most nanoseconds or bytes it can hold, 2047 for the bucket ZFS
// counts from 1024.
//
// ZFS keeps no total, so the sum is worked out from the buckets. A latency counts each request at the middle of its bucket, as zpool iostat -l works
// out its averages; a size counts each at the bottom, since nearly every request ZFS makes is a power of two in size.
func histogram(points []point, latency bool) poll.Histogram {
	unit, share := 1.0, 1.0
	if latency {
		unit, share = 1e-9, 1.5
	}

	var h poll.Histogram
	var below uint64
	last := 0
	for _, p := range points {
		power := p.power
		if p.all {
			power = last + 1
		}
		last = power

		top := math.Inf(1)
		if !p.all {
			top = (math.Exp2(float64(power+1)) - 1) * unit
		}
		h.Buckets = append(h.Buckets, poll.Bucket{Bound: top, Count: p.count})

		if p.count > below {
			h.Sum += float64(p.count-below) * share * math.Exp2(float64(power)) * unit
			below = p.count
		}
	}

	// every histogram ends with a bucket that holds everything, which zpool_influxdb always writes
	if n := len(h.Buckets); n > 0 && !math.IsInf(h.Buckets[n-1].Bound, 1) {
		h.Buckets = append(h.Buckets, poll.Bucket{Bound: math.Inf(1), Count: h.Buckets[n-1].Count})
	}

	return h
}
