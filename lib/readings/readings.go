// Package readings is what the poller knows of the machine's pools, as Prometheus readings for a metrics page to hold.
package readings

import (
	"math"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/katbyte/go-kt/pointer"
	"github.com/katbyte/go-kt/prometheus/metrics"
	"github.com/katbyte/zfs-exporter/lib/poll"
	"github.com/katbyte/zfs-exporter/lib/skip"
	"github.com/prometheus/client_golang/prometheus"
)

// Namespace starts every reading's name.
const Namespace = "zfs"

// The labels that say where a reading is from: the labels truenas-exporter puts on each disk, so the two join, and sas-exporter's for where a disk
// sits.
const (
	poolLabel      = "pool"
	classLabel     = "class"
	vdevLabel      = "vdev"
	diskLabel      = "disk"
	serialLabel    = "serial"
	waitLabel      = "wait"
	opLabel        = "op"
	queueLabel     = "queue"
	aggregateLabel = "aggregated"
	commandLabel   = "command"
	stateLabel     = "state"
	errorLabel     = "error"
)

// list is every reading the page can hold.
var list = metrics.NewList(Namespace)

// reading describes one reading.
func reading(name, help string, labels ...string) *prometheus.Desc {
	return list.Reading(name, help, labels...)
}

var (
	pool = []string{poolLabel}
	vdev = []string{poolLabel, classLabel, vdevLabel}
	disk = []string{poolLabel, classLabel, vdevLabel, diskLabel, serialLabel}
)

// latencyHelp and sizeHelp end the help of every latency and every size histogram: how its buckets and its sum are to be read.
const (
	latencyHelp = " By wait: total from being asked to being answered, disk at the disk itself, and sync_queue, async_queue, scrub_queue, trim_queue and rebuild_queue in each of ZFS's queues before being sent to a disk; and by op, read or write. ZFS counts in powers of two, so each bucket ends at the most it holds, 2047ns for the one from 1024ns. ZFS keeps no total, so the sum is worked out the way zpool iostat -l works out its averages: each request counted at the middle of its bucket."
	sizeHelp    = " By queue, sync, async, scrub, trim or rebuild; op, read or write; and aggregated, true for requests ZFS made by joining smaller ones next to each other. ZFS counts in powers of two, so each bucket ends at the most it holds, 8191 bytes for the one from 4096. ZFS keeps no total, so the sum counts each request at the bottom of its bucket, since nearly every request is a power of two in size: zfs_*_bytes_total over zfs_*_operations_total is the exact average."
)

var (
	up              = reading("up", "Whether the last round of asking ZFS about the pools was answered in full (1) or not (0). A pool that has stopped answering, or a program that cannot be run, shows here first.")
	slowAfter       = reading("slow_io_threshold_seconds", "How long a request takes before ZFS counts it as slow, as zio_slow_io_ms sets it.")
	commandTimeouts = reading("command_timeouts_total", "How many times a ZFS program ran past its time limit and was stopped.", commandLabel)
	commandStuck    = reading("command_stuck", "Whether a ZFS program that ran past its time limit has still not ended when killed (1) or not (0): ZFS itself is likely stuck on a pool, and nothing more is asked until it ends.", commandLabel)

	poolLatency = reading("pool_latency_seconds", "How long each request to the pool took, as a histogram, since the pool was imported."+latencyHelp, slices.Concat(pool, []string{waitLabel, opLabel})...)
	poolSizes   = reading("pool_request_size_bytes", "How big each request to the pool was, as a histogram, since the pool was imported."+sizeHelp, slices.Concat(pool, []string{queueLabel, opLabel, aggregateLabel})...)
	poolOps     = reading("pool_operations_total", "How many reads and writes the pool has done since it was imported, by op.", slices.Concat(pool, []string{opLabel})...)
	poolBytes   = reading("pool_bytes_total", "How many bytes the pool has read and written since it was imported, by op.", slices.Concat(pool, []string{opLabel})...)
	poolActive  = reading("pool_active_requests", "How many requests the pool had with its disks when it was last asked, by queue and op: a glimpse once a stats interval, so it shows a pool that stays busy and not a burst.", slices.Concat(pool, []string{queueLabel, opLabel})...)
	poolPending = reading("pool_pending_requests", "How many requests the pool had waiting their turn for its disks when it was last asked, by queue and op: a glimpse once a stats interval.", slices.Concat(pool, []string{queueLabel, opLabel})...)

	vdevLatency = reading("vdev_latency_seconds", "How long each request to the top-level vdev took, as a histogram, since the pool was imported."+latencyHelp, slices.Concat(vdev, []string{waitLabel, opLabel})...)
	vdevSizes   = reading("vdev_request_size_bytes", "How big each request to the top-level vdev was, as a histogram, since the pool was imported."+sizeHelp, slices.Concat(vdev, []string{queueLabel, opLabel, aggregateLabel})...)
	vdevOps     = reading("vdev_operations_total", "How many reads and writes the top-level vdev has done since the pool was imported, by op.", slices.Concat(vdev, []string{opLabel})...)
	vdevBytes   = reading("vdev_bytes_total", "How many bytes the top-level vdev has read and written since the pool was imported, by op.", slices.Concat(vdev, []string{opLabel})...)

	diskInfo    = reading("disk_info", "Always 1, with what each disk in a pool is and where: ZFS's GUID for it and the path it has for it, the disk's model and world wide name, and the enclosure and slot it sits in as sas-exporter names them. The disk label is the kernel's name for the disk today, or ZFS's GUID for a disk it cannot find; the serial label is the one thing that never changes.", slices.Concat(disk, []string{"guid", "path", "model", "wwn", "enclosure", "slot"})...)
	diskSlowIOs = reading("disk_slow_ios_total", "How many requests the disk took longer than zfs_slow_io_threshold_seconds to answer, since the pool was imported or its counts were cleared. A disk going bad is often slow before it gives errors.", disk...)
	diskLatency = reading("disk_latency_seconds", "How long each request to the disk took, as a histogram, since the pool was imported: compare a disk with the others in its vdev, since a stripe waits for its slowest disk. Only the time at the disk itself, wait disk, unless every disk timing is asked for."+latencyHelp, slices.Concat(disk, []string{waitLabel, opLabel})...)
	diskSizes   = reading("disk_request_size_bytes", "How big each request to the disk was, as a histogram, since the pool was imported, when every disk timing is asked for."+sizeHelp, slices.Concat(disk, []string{queueLabel, opLabel, aggregateLabel})...)
	diskOps     = reading("disk_operations_total", "How many reads and writes the disk has done since the pool was imported, by op, when every disk timing is asked for.", slices.Concat(disk, []string{opLabel})...)
	diskBytes   = reading("disk_bytes_total", "How many bytes the disk has read and written since the pool was imported, by op, when every disk timing is asked for.", slices.Concat(disk, []string{opLabel})...)
)

// The states ZFS gives a pool, and a vdev or disk, as zpool status names them, and a spare's. Each is written as 1 for the state it is in and 0 for
// the rest, so a state left is seen to go; one ZFS adds later is written too.
var (
	poolStates  = []string{"ONLINE", "DEGRADED", "FAULTED", "OFFLINE", "REMOVED", "UNAVAIL", "SUSPENDED"}
	vdevStates  = []string{"ONLINE", "DEGRADED", "FAULTED", "OFFLINE", "REMOVED", "UNAVAIL"}
	spareStates = []string{"AVAIL", "INUSE"}
)

// stateHelp and errorsHelp end the help of every state and every count of errors.
const (
	stateHelp  = " Each state is a reading of its own, 1 for the one it is in and 0 for the rest: ONLINE, DEGRADED, FAULTED, OFFLINE, REMOVED or UNAVAIL, and for a spare AVAIL or INUSE."
	errorsHelp = " By error: read, write or checksum, the last a block that read back other than it was written. Since the pool was imported or its counts cleared."
)

var (
	poolState      = reading("pool_state", "The pool's health, as zpool status gives it. Each state is a reading of its own, 1 for the one it is in and 0 for the rest: ONLINE, DEGRADED, FAULTED, OFFLINE, REMOVED, UNAVAIL, or SUSPENDED for a pool that has stopped reading and writing until its disks come back.", slices.Concat(pool, []string{stateLabel})...)
	poolErrors     = reading("pool_errors_total", "How many requests to the pool as a whole went wrong, as zpool status counts them on the pool's own line."+errorsHelp, slices.Concat(pool, []string{errorLabel})...)
	poolDataErrors = reading("pool_data_errors", "How many places in the pool hold data ZFS could not repair, as zpool status -v lists them: anything but 0 is damaged files.", pool...)
	vdevState      = reading("vdev_state", "The top-level vdev's health, as zpool status gives it."+stateHelp, slices.Concat(vdev, []string{stateLabel})...)
	vdevErrors     = reading("vdev_errors_total", "How many requests to the top-level vdev went wrong, as zpool status counts them on its own line."+errorsHelp, slices.Concat(vdev, []string{errorLabel})...)
	diskState      = reading("disk_state", "The disk's health, as zpool status gives it."+stateHelp, slices.Concat(disk, []string{stateLabel})...)
	diskErrors     = reading("disk_errors_total", "How many requests to the disk went wrong, as zpool status counts them."+errorsHelp, slices.Concat(disk, []string{errorLabel})...)

	poolSize          = reading("pool_size_bytes", "How big the pool is, raidz parity counted, as zpool list shows it.", pool...)
	poolAllocated     = reading("pool_allocated_bytes", "How much of the pool is used, raidz parity counted, as zpool list shows it: zfs_pool_size_bytes less this is what is free.", pool...)
	poolUsable        = reading("pool_usable_bytes", "How big the pool is with raidz parity taken out: about what its files can fill, less what ZFS keeps back for itself.", pool...)
	vdevSize          = reading("vdev_size_bytes", "How big the top-level vdev is, raidz parity counted, as zpool list -v shows it.", vdev...)
	vdevAllocated     = reading("vdev_allocated_bytes", "How much of the top-level vdev is used, raidz parity counted, as zpool list -v shows it.", vdev...)
	vdevUsable        = reading("vdev_usable_bytes", "How big the top-level vdev is with raidz parity taken out.", vdev...)
	vdevFragmentation = reading("vdev_fragmentation_ratio", "How broken up the top-level vdev's free space is, from 0 to 1, as zpool list -v shows FRAG: a vdev whose free space is in small pieces writes more slowly. The pool's FRAG is its vdevs' weighed by their size.", vdev...)
)

// scanStates is the states of a scrub or a resilver, as zpool status names them and one more: a scrub that is SCANNING but paused is PAUSED.
var scanStates = []string{poll.ScanScanning, scanPaused, poll.ScanFinished, poll.ScanCanceled}

const scanPaused = "PAUSED"

// scan is the readings of one kind of scan, a scrub or a resilver, each under its own names so that one can be left out and the other kept.
type scan struct {
	part      skip.Part
	function  string
	state     *prometheus.Desc
	start     *prometheus.Desc
	end       *prometheus.Desc
	toExamine *prometheus.Desc
	examined  *prometheus.Desc
	skipped   *prometheus.Desc
	issued    *prometheus.Desc
	processed *prometheus.Desc
	errors    *prometheus.Desc
	done      *prometheus.Desc
	rate      *prometheus.Desc
	remaining *prometheus.Desc
}

// scanReadings is the readings of the scan named, a scrub or a resilver, with the name and help of what it does: repairs or rebuilds.
func scanReadings(part skip.Part, function, processed, processedHelp string) scan {
	what := string(part)
	last := "the pool's last " + what
	named := func(name, help string, labels ...string) *prometheus.Desc {
		return reading("pool_"+what+"_"+name, help, slices.Concat(pool, labels)...)
	}

	return scan{
		part:      part,
		function:  function,
		state:     named("state", "Whether "+last+" is running or not. Each state is a reading of its own, 1 for the one it is in and 0 for the rest: SCANNING, PAUSED, FINISHED or CANCELED. ZFS keeps only a pool's last scrub or resilver, so a pool shows one or the other.", stateLabel),
		start:     named("start_timestamp_seconds", "When "+last+" started."),
		end:       named("end_timestamp_seconds", "When "+last+" finished or was canceled, and absent while it runs."),
		toExamine: named("to_examine_bytes", "How much "+last+" has to look through in all."),
		examined:  named("examined_bytes", "How much "+last+" has looked through so far."),
		skipped:   named("skipped_bytes", "How much "+last+" found it did not need to read."),
		issued:    named("issued_bytes", "How much "+last+" has read from the disks so far: it is done with a part once that part is read."),
		processed: named(processed+"_bytes", "How much "+last+" "+processedHelp+"."),
		errors:    named("errors", "How many errors "+last+" came across."),
		done:      named("done_ratio", "How far along the pool's "+what+" is, from 0 to 1, as zpool status works out its percent done: what it has read of what it has to. Only while it runs."),
		rate:      named("rate_bytes_per_second", "How fast the pool's "+what+" is reading, as zpool status works it out: what it has read since it was started, resumed or the pool imported, over how long it has run since. Only while it runs and is not paused."),
		remaining: named("remaining_seconds", "How long the pool's "+what+" has left at its rate, as zpool status estimates it: only once it is under way at 10MiB a second or more."),
	}
}

var (
	scrubbed   = scanReadings(skip.Scrub, poll.ScanScrub, "repaired", "has repaired")
	resilvered = scanReadings(skip.Resilver, poll.ScanResilver, "resilvered", "has rebuilt on the disks it is for")
)

// Source is where the pools' state comes from: the poller, or a test's own.
type Source interface {
	State() poll.State
}

// Readings writes what a source knows as readings. It asks ZFS nothing, so a scrape is answered from memory however slow a pool is.
type Readings struct {
	source      Source
	skip        skip.Set
	diskTimings bool
}

// New is the readings of one machine's pools.
func New(source Source) *Readings {
	return &Readings{source: source}
}

// Skipping leaves those parts off the page. Whether ZFS is answering is always on it.
func (r *Readings) Skipping(parts skip.Set) *Readings {
	r.skip = parts

	return r
}

// EveryDiskTiming puts on the page every timing, size and count of each disk that the pools and vdevs have, and not just how long each disk took
// itself: about four times the readings on the page.
func (r *Readings) EveryDiskTiming() *Readings {
	r.diskTimings = true

	return r
}

// Describe names every reading there can be.
func (*Readings) Describe(ch chan<- *prometheus.Desc) {
	list.Describe(ch)
}

// Collect writes every reading there is an answer for. A pool or a disk that has never been heard of has no readings, which is not the same as
// readings of zero.
func (r *Readings) Collect(ch chan<- prometheus.Metric) {
	s := r.source.State()
	w := writer{Writer: metrics.NewWriter(ch), ch: ch, skip: r.skip, diskTimings: r.diskTimings, histograms: map[string]bool{}}

	if s.Ran() {
		w.Flag(up, s.Up())
	}

	if !r.skip.Has(skip.Poll) {
		for _, c := range s.Commands {
			w.Total(commandTimeouts, c.Timeouts, c.Name)
			w.Flag(commandStuck, c.Stuck, c.Name)
		}
	}

	pools := map[string]*poll.Pool{}
	if s.Status.Known() {
		status := s.Status.Value
		if status.SlowAfter > 0 && !r.skip.Has(skip.SlowIOs) {
			w.Gauge(slowAfter, status.SlowAfter.Seconds())
		}

		for i := range status.Pools {
			pools[status.Pools[i].Name] = &status.Pools[i]
			w.status(&status.Pools[i], s.Status.At)
		}
	}

	if s.Stats.Known() {
		for i := range s.Stats.Value.Pools {
			w.stats(&s.Stats.Value.Pools[i], pools[s.Stats.Value.Pools[i].Name])
		}
	}
}

// writer writes the readings. A thing written twice is written once: two readings under one set of labels would spoil the whole page.
type writer struct {
	*metrics.Writer
	ch          chan<- prometheus.Metric
	skip        skip.Set
	diskTimings bool
	histograms  map[string]bool
}

// histogram writes one histogram, once.
func (w writer) histogram(d *prometheus.Desc, h *poll.Histogram, labels ...string) {
	key := d.String() + "\x00" + strings.Join(labels, "\x00")
	if w.histograms[key] || len(h.Buckets) == 0 {
		return
	}
	w.histograms[key] = true

	buckets := make(map[float64]uint64, len(h.Buckets))
	for _, b := range h.Buckets {
		if !math.IsInf(b.Bound, 1) {
			buckets[b.Bound] = b.Count
		}
	}

	m, err := prometheus.NewConstHistogram(d, h.Count(), h.Sum, buckets, labels...)
	if err != nil {
		m = prometheus.NewInvalidMetric(d, err)
	}

	w.ch <- m
}

// place is where a vdev zpool_influxdb counts sits in its pool, as zpool status describes the pool.
type place struct {
	class string
	// vdev is the label of the top-level vdev it is, or is in
	vdev string
	// top says it is a top-level vdev, and disk that it is a disk: a pool of one disk has one that is both
	top  bool
	disk *poll.Vdev
}

// places is where each vdev zpool_influxdb names sits, by the names it gives them and by the paths of disks. zpool_influxdb names a vdev by its
// kind and its number among its siblings, root/raidz-3/disk-2, where zpool status names a vdev made of others by its kind and number, raidz2-3, and
// a disk by its path; both list a vdev's disks in the order of their numbers. A pool's cache and spare disks are not among zpool_influxdb's.
func places(p *poll.Pool) (byName, byPath map[string]place) {
	byName, byPath = map[string]place{}, map[string]place{}

	var under func(v *poll.Vdev, name string, at place)
	under = func(v *poll.Vdev, name string, at place) {
		for i := range v.Children {
			child := &v.Children[i]
			inside := name + "/" + child.Type + "-" + strconv.Itoa(i)

			if !child.Leaf() {
				under(child, inside, at)
				continue
			}

			here := place{class: at.class, vdev: at.vdev, disk: child}
			byName[inside] = here
			if child.Path != "" {
				byPath[child.Path] = here
			}
		}
	}

	for i := range p.Vdevs {
		top := &p.Vdevs[i]
		at := place{class: top.Class, vdev: vdevName(top), top: true}

		if top.Leaf() {
			at.disk = top
			if top.Path != "" {
				byPath[top.Path] = at
			}

			continue
		}

		// the number is the end of zpool status's name for it: raidz2-3 is the fourth top-level vdev
		number := top.Name[strings.LastIndex(top.Name, "-")+1:]
		name := poll.Root + "/" + top.Type + "-" + number
		byName[name] = at
		under(top, name, at)
	}

	return byName, byPath
}

// vdevName is a top-level vdev's label: zpool status's name for one made of others, raidz2-0, and the name of the partition for one that is a disk,
// as truenas-exporter names it, or zpool status's name when the partition cannot be found.
func vdevName(v *poll.Vdev) string {
	if v.Leaf() && v.Disk != nil {
		return v.Disk.Partition
	}

	return v.Name
}

// diskLabels is the labels of a disk in a top-level vdev: the kernel's name for it and its serial number, or ZFS's GUID for it and no serial
// number when it cannot be found.
func diskLabels(pool string, at place, d *poll.Vdev) []string {
	if d.Disk == nil {
		return []string{pool, at.class, at.vdev, d.GUID, ""}
	}

	return []string{pool, at.class, at.vdev, d.Disk.Name, d.Disk.Serial}
}

// status is what zpool status says of a pool, as it was at the time given: its health, space and last scan, and those of its vdevs and disks.
func (w writer) status(p *poll.Pool, at time.Time) {
	if !w.skip.Has(skip.Pools) {
		if !w.skip.Has(skip.Health) {
			w.States(poolState, poolStates, p.State, p.Name)
			w.errors(poolErrors, p.Errors, p.Name)
			if p.DataErrors != nil {
				w.Gauge(poolDataErrors, *p.DataErrors, p.Name)
			}
		}

		if p.Space != nil && !w.skip.Has(skip.Space) {
			w.Gauge(poolSize, p.Space.Size, p.Name)
			w.Gauge(poolAllocated, p.Space.Allocated, p.Name)
			if p.Space.Usable > 0 {
				w.Gauge(poolUsable, p.Space.Usable, p.Name)
			}
		}

		w.scan(scrubbed, p, at)
		w.scan(resilvered, p, at)
	}

	for i := range p.Vdevs {
		top := &p.Vdevs[i]
		w.top(top, p.Name, top.Class, vdevName(top))
	}

	w.disks(p)
}

// states is the states a vdev or disk of this class can be in.
func states(class string) []string {
	if class == poll.ClassSpare {
		return spareStates
	}

	return vdevStates
}

// errors writes the errors counted of a pool, vdev or disk, one reading for each kind, and nothing when zpool status does not say.
func (w writer) errors(d *prometheus.Desc, e *poll.Errors, labels ...string) {
	if e == nil {
		return
	}

	w.Total(d, e.Read, slices.Concat(labels, []string{"read"})...)
	w.Total(d, e.Write, slices.Concat(labels, []string{"write"})...)
	w.Total(d, e.Checksum, slices.Concat(labels, []string{"checksum"})...)
}

// top is what zpool status says of a top-level vdev: its health and its space.
func (w writer) top(v *poll.Vdev, labels ...string) {
	if w.skip.Has(skip.Vdevs) {
		return
	}

	if !w.skip.Has(skip.Health) {
		w.States(vdevState, states(v.Class), v.State, labels...)
		w.errors(vdevErrors, v.Errors, labels...)
	}

	if v.Space != nil && !w.skip.Has(skip.Space) {
		w.Gauge(vdevSize, v.Space.Size, labels...)
		w.Gauge(vdevAllocated, v.Space.Allocated, labels...)
		if v.Space.Usable > 0 {
			w.Gauge(vdevUsable, v.Space.Usable, labels...)
		}
	}
}

// scan is a pool's last scan, when it is of this kind, as it was at the time given.
func (w writer) scan(r scan, p *poll.Pool, at time.Time) {
	s := p.Scan
	if s == nil || s.Function != r.function || w.skip.Has(r.part) {
		return
	}

	state := s.State
	if s.Running() && !s.Paused.IsZero() {
		state = scanPaused
	}

	w.States(r.state, scanStates, state, p.Name)
	w.At(r.start, s.Start, p.Name)
	w.At(r.end, s.End, p.Name)
	w.Gauge(r.toExamine, s.ToExamine, p.Name)
	w.Gauge(r.examined, s.Examined, p.Name)
	w.Gauge(r.skipped, s.Skipped, p.Name)
	w.Gauge(r.issued, s.Issued, p.Name)
	w.Gauge(r.processed, s.Processed, p.Name)
	w.Gauge(r.errors, s.Errors, p.Name)

	if !s.Running() {
		return
	}
	w.Gauge(r.done, s.Done(), p.Name)

	if rate := s.Rate(at); rate > 0 {
		w.Gauge(r.rate, rate, p.Name)
	}
	if left, known := s.Remaining(at); known {
		w.Gauge(r.remaining, left.Seconds(), p.Name)
	}
}

// disks is what zpool status says of each disk in a pool: what it is and where, its health, and how many slow requests it has had.
func (w writer) disks(p *poll.Pool) {
	if w.skip.Has(skip.Disks) {
		return
	}

	var each func(v *poll.Vdev, at place)
	each = func(v *poll.Vdev, at place) {
		for i := range v.Children {
			each(&v.Children[i], at)
		}
		if !v.Leaf() {
			return
		}

		labels := diskLabels(p.Name, at, v)
		d := pointer.From(v.Disk)
		w.Gauge(diskInfo, 1, slices.Concat(labels, []string{v.GUID, v.Path, d.Model, d.WWN, d.Enclosure, d.Slot})...)

		if !w.skip.Has(skip.Health) {
			w.States(diskState, states(at.class), v.State, labels...)
			w.errors(diskErrors, v.Errors, labels...)
		}

		if v.SlowIOs != nil && !w.skip.Has(skip.SlowIOs) {
			w.Total(diskSlowIOs, pointer.From(v.SlowIOs), labels...)
		}
	}

	for i := range p.Vdevs {
		top := &p.Vdevs[i]
		each(top, place{class: top.Class, vdev: vdevName(top)})
	}
}

// stats is what zpool_influxdb counts of one pool: of the pool as a whole, of each top-level vdev and of each disk, each where zpool status says it
// is. With nothing from zpool status to say where each is, only the pool as a whole is written.
func (w writer) stats(s *poll.PoolStats, described *poll.Pool) {
	byName, byPath := map[string]place{}, map[string]place{}
	if described != nil {
		byName, byPath = places(described)
	}

	for i := range s.Vdevs {
		v := &s.Vdevs[i]
		if v.Name == poll.Root {
			w.pool(s.Name, v, s.Queues)
			continue
		}

		at, found := byPath[v.Path]
		if v.Path == "" || !found {
			at, found = byName[v.Name]
		}
		if !found {
			continue
		}

		if at.top {
			w.vdev(v, s.Name, at.class, at.vdev)
		}
		if at.disk != nil {
			w.disk(v, diskLabels(s.Name, at, at.disk))
		}
	}
}

// pool is what zpool_influxdb counts of a pool as a whole.
func (w writer) pool(name string, v *poll.VdevStats, queues []poll.Queue) {
	if w.skip.Has(skip.Pools) {
		return
	}

	w.counts(poolLatency, poolSizes, poolOps, poolBytes, v, name)

	if w.skip.Has(skip.Queues) {
		return
	}
	for _, q := range queues {
		w.Gauge(poolActive, q.Active, name, q.Queue, q.Op)
		w.Gauge(poolPending, q.Pending, name, q.Queue, q.Op)
	}
}

// vdev is what zpool_influxdb counts of a top-level vdev.
func (w writer) vdev(v *poll.VdevStats, labels ...string) {
	if w.skip.Has(skip.Vdevs) {
		return
	}

	w.counts(vdevLatency, vdevSizes, vdevOps, vdevBytes, v, labels...)

	if v.Fragmentation != nil && !w.skip.Has(skip.Space) {
		w.Gauge(vdevFragmentation, float64(*v.Fragmentation)/100, labels...)
	}
}

// counts is the requests, sizes and latencies of a pool or a top-level vdev, each under the readings given.
func (w writer) counts(latency, sizes, ops, bytes *prometheus.Desc, v *poll.VdevStats, labels ...string) {
	if v.Operations != nil && !w.skip.Has(skip.Operations) {
		o := pointer.From(v.Operations)
		w.Total(ops, o.Reads, slices.Concat(labels, []string{poll.OpRead})...)
		w.Total(ops, o.Writes, slices.Concat(labels, []string{poll.OpWrite})...)
		w.Total(bytes, o.ReadBytes, slices.Concat(labels, []string{poll.OpRead})...)
		w.Total(bytes, o.WrittenBytes, slices.Concat(labels, []string{poll.OpWrite})...)
	}

	if !w.skip.Has(skip.Latency) {
		for i := range v.Latency {
			l := &v.Latency[i]
			w.histogram(latency, &l.Histogram, slices.Concat(labels, []string{l.Wait, l.Op})...)
		}
	}

	if !w.skip.Has(skip.RequestSizes) {
		for i := range v.Sizes {
			s := &v.Sizes[i]
			w.histogram(sizes, &s.Histogram, slices.Concat(labels, []string{s.Queue, s.Op, strconv.FormatBool(s.Aggregated)})...)
		}
	}
}

// disk is what zpool_influxdb counts of a disk: how long each request took at the disk itself, or everything it counts when every disk timing is
// asked for.
func (w writer) disk(v *poll.VdevStats, labels []string) {
	if w.skip.Has(skip.Disks) {
		return
	}

	if w.diskTimings {
		w.counts(diskLatency, diskSizes, diskOps, diskBytes, v, labels...)
		return
	}

	if w.skip.Has(skip.Latency) {
		return
	}
	for i := range v.Latency {
		if l := &v.Latency[i]; l.Wait == poll.WaitDisk {
			w.histogram(diskLatency, &l.Histogram, slices.Concat(labels, []string{l.Wait, l.Op})...)
		}
	}
}
