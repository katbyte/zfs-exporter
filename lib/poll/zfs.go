// Package poll asks ZFS about the machine's pools on a schedule and keeps the last good answers, so the metrics page is served from memory and a
// pool that is slow to answer never holds up a scrape.
//
// A disk is named by the kernel's name for it today, sdf, which is what the machine's own tools print. That can change at a reboot, so each disk also
// carries its serial number, which does not.
package poll

import (
	"context"
	"time"
)

// ZFS is the questions asked of the machine about its pools. Every one of them only reads.
type ZFS interface {
	// Status is every pool's layout and what zpool status says of each disk, with each disk found among the machine's own.
	Status(ctx context.Context) (Status, error)
	// Stats is what zpool_influxdb counts of every pool, vdev and disk: how many requests, how big and how long each took.
	Stats(ctx context.Context) (Stats, error)
	// Commands is how each program the questions are asked through has been going, which costs nothing to ask.
	Commands() []Command
}

// Status is what zpool status says of every pool.
type Status struct {
	Pools []Pool
	// SlowAfter is how long a request takes before ZFS counts it as slow, and 0 when the machine does not say.
	SlowAfter time.Duration
}

// The classes a vdev can be in, as the class label names them: the words truenas-exporter uses, so the two join.
const (
	ClassData    = "data"
	ClassLog     = "log"
	ClassSpecial = "special"
	ClassDedup   = "dedup"
	ClassCache   = "cache"
	ClassSpare   = "spare"
)

// Pool is one pool, as zpool status describes it.
type Pool struct {
	Name  string
	GUID  string
	State string
	// Space is how big the pool is and how much of it is used, and Errors the errors ZFS has counted of the pool as a whole. Each is nil when zpool
	// status does not say.
	Space  *Space
	Errors *Errors
	// DataErrors is how many places in the pool ZFS has found data it could not repair, as zpool status -v lists them, and nil when it does not say.
	DataErrors *uint64
	// Scan is the pool's last scrub or resilver, and nil for a pool that has had neither.
	Scan *Scan
	// Vdevs is the pool's top-level vdevs of every class, in the order zpool status lists them: the ones that hold data, then the special, dedup,
	// log, cache and spare ones.
	Vdevs []Vdev
}

// Space is how big a pool or a vdev is and how much of it is used, in bytes, as zpool list shows them: raidz parity counted in both.
type Space struct {
	Size      uint64
	Allocated uint64
	// Usable is the size with raidz parity taken out, which is what the files in it can fill, and 0 when zpool status does not say.
	Usable uint64
}

// Errors is how many reads, writes and checksums of a pool, vdev or disk went wrong, as zpool status counts them since the pool was imported or
// its counts cleared.
type Errors struct {
	Read     uint64
	Write    uint64
	Checksum uint64
}

// The two kinds of scan, as zpool status names them: a scrub reads everything to check it, a resilver rebuilds what a new or returning disk lacks.
const (
	ScanScrub    = "SCRUB"
	ScanResilver = "RESILVER"
)

// The states of a scan, as zpool status names them.
const (
	ScanScanning = "SCANNING"
	ScanFinished = "FINISHED"
	ScanCanceled = "CANCELED"
)

// Scan is a pool's last scrub or resilver. ZFS keeps only the last of either, so a resilver puts the scrub before it out of sight.
type Scan struct {
	// Function is ScanScrub or ScanResilver, and State ScanScanning, ScanFinished or ScanCanceled.
	Function string
	State    string
	Start    time.Time
	// End is when it finished or was canceled, and the zero time while it runs. Paused is when a scrub was paused, and the zero time for one that
	// is not.
	End    time.Time
	Paused time.Time
	// ToExamine is how much it has to look through in all, Examined how much it has, Skipped how much it found it need not read, and Issued how
	// much it has read from the disks: it is done with a part once that part is read.
	ToExamine uint64
	Examined  uint64
	Skipped   uint64
	Issued    uint64
	// Processed is how much it has repaired, for a scrub, or rebuilt, for a resilver. Errors is how many errors it has come across.
	Processed uint64
	Errors    uint64
	// PassStart, PassIssued and PassPaused are of this pass, which starts again when a scan is resumed or its pool imported: when it started, how
	// much it has read, and how long it spent paused.
	PassStart  time.Time
	PassIssued uint64
	PassPaused time.Duration
}

// Running says the scan has not ended, paused or not.
func (s *Scan) Running() bool { return s.State == ScanScanning }

// Done is how far along the scan is, from 0 to 1, as zpool status works out its percent done: what it has read of what it has to.
func (s *Scan) Done() float64 {
	if s.ToExamine <= s.Skipped {
		return 0
	}

	return float64(s.Issued) / float64(s.ToExamine-s.Skipped)
}

// Rate is how fast a running scan is reading, in bytes a second, as zpool status works it out at the time given: what this pass has read over how
// long it has run, less the time it spent paused. It is 0 for one that is paused or has ended.
func (s *Scan) Rate(at time.Time) float64 {
	if !s.Running() || !s.Paused.IsZero() {
		return 0
	}

	// a pass that has just started has run for a second, as zpool status counts it
	elapsed := max(at.Sub(s.PassStart)-s.PassPaused, time.Second)

	return float64(s.PassIssued) / elapsed.Seconds()
}

// Remaining is how long a running scan has left at its rate, as zpool status estimates it at the time given. Like zpool status, it gives none until
// the scan has read at 10MiB a second or more and done some of its work.
func (s *Scan) Remaining(at time.Time) (time.Duration, bool) {
	rate := s.Rate(at)
	total := s.ToExamine - min(s.Skipped, s.ToExamine)

	started := s.Issued > 0
	if s.Function == ScanResilver {
		started = s.Processed > 0
	}
	if rate < 10<<20 || total < s.Issued || !started {
		return 0, false
	}

	return time.Duration(float64(total-s.Issued) / rate * float64(time.Second)), true
}

// Vdev is one vdev: a top-level one, one inside it such as a disk being replaced, or a disk.
type Vdev struct {
	// Name is what zpool status calls it: raidz2-0 for one made of others, and for a disk the last part of its path, which on TrueNAS is the
	// partition's UUID.
	Name string
	// Type is what kind of vdev it is, as zpool status says: raidz, mirror, disk.
	Type string
	// Class is which of the pool's classes it is in: ClassData and the rest.
	Class string
	GUID  string
	State string
	// Space is how big the vdev is and how much of it is used, and nil for a disk inside another, whose space is that vdev's, and for a spare.
	Space *Space
	// Errors is the errors ZFS has counted of the vdev, and nil for a spare.
	Errors *Errors
	// Path is where ZFS last found a disk, /dev/disk/by-partuuid/<uuid>, and empty for a vdev made of others.
	Path string
	// SlowIOs is how many requests the disk took longer than Status.SlowAfter to answer since the pool was imported or its counts cleared, and nil
	// for a vdev made of others or a spare.
	SlowIOs *uint64
	// Disk is the disk at Path as the machine knows it now, and nil when there is none to find: a disk that has gone, or a vdev made of others.
	Disk     *Disk
	Children []Vdev
}

// Leaf says whether the vdev is a disk, or a file standing in for one, rather than made of others.
func (v *Vdev) Leaf() bool { return len(v.Children) == 0 }

// Disk is a disk as the machine knows it, found from the path ZFS has for it.
type Disk struct {
	// Name is the kernel's name for the disk today, sdf, and Partition for the part of it ZFS was given, sdf1; the two are the same for a vdev on a
	// whole disk.
	Name      string
	Partition string
	// Serial is the disk's own serial number, and Model what the maker calls it. Either is empty when the disk does not say.
	Serial string
	Model  string
	// WWN is the disk's world wide name as hex with nothing before it, and empty for a disk with none.
	WWN string
	// Enclosure and Slot are where the disk sits, as its enclosure tells the kernel: the enclosure's name in the kernel, 1:0:24:0, and the slot's
	// number. Both are empty for a disk in no enclosure's slot.
	Enclosure string
	Slot      string
}

// Stats is what zpool_influxdb counts of every pool.
type Stats struct {
	Pools []PoolStats
}

// PoolStats is what zpool_influxdb counts of one pool.
type PoolStats struct {
	Name string
	// Vdevs is every vdev zpool_influxdb reports on, the pool as a whole first: cache and spare disks are not among them.
	Vdevs []VdevStats
	// Queues is how many requests are with the pool's disks and how many are waiting their turn, at the moment it was asked.
	Queues []Queue
}

// Root is what zpool_influxdb calls the pool as a whole, and starts the name of every vdev in it with.
const Root = "root"

// VdevStats is what zpool_influxdb counts of one vdev.
type VdevStats struct {
	// Name is what zpool_influxdb calls the vdev, Root for the pool as a whole, then root/raidz-0 for a top-level vdev and root/raidz-0/disk-3 for a
	// disk in it: its kind and its number among its siblings, as ZFS numbers them.
	Name string
	// Path is where ZFS last found a disk, and empty for a vdev made of others.
	Path string
	// Operations is how many reads and writes the vdev has done and how much they moved, and nil when zpool_influxdb did not say.
	Operations *Operations
	// Fragmentation is how broken up the vdev's free space is, as a percent, and nil when zpool_influxdb did not say. Only a top-level vdev's means
	// anything: the pool as a whole and a disk inside another are written as 0.
	Fragmentation *uint64
	Latency       []Latency
	Sizes         []Size
}

// Operations is how many reads and writes a vdev has done since the pool was imported, and how many bytes they moved.
type Operations struct {
	Reads        uint64
	Writes       uint64
	ReadBytes    uint64
	WrittenBytes uint64
}

// The kinds of wait ZFS times each request for, as the wait label names them.
const (
	// WaitTotal is the whole of it, from being asked to being answered.
	WaitTotal = "total"
	// WaitDisk is the time at the disk, from being sent to it to its answer.
	WaitDisk = "disk"
	// The rest are the time spent in each of ZFS's queues before being sent to the disk.
	WaitSyncQueue    = "sync_queue"
	WaitAsyncQueue   = "async_queue"
	WaitScrubQueue   = "scrub_queue"
	WaitTrimQueue    = "trim_queue"
	WaitRebuildQueue = "rebuild_queue"
)

// The two ways a request goes, as the op label names them.
const (
	OpRead  = "read"
	OpWrite = "write"
)

// Latency is how long one kind of request waited, as a histogram in seconds.
type Latency struct {
	Wait      string
	Op        string
	Histogram Histogram
}

// The queues ZFS keeps requests in before they are sent to a disk, as the queue label names them.
const (
	QueueSync    = "sync"
	QueueAsync   = "async"
	QueueScrub   = "scrub"
	QueueTrim    = "trim"
	QueueRebuild = "rebuild"
)

// Size is how big one kind of request was, as a histogram in bytes.
type Size struct {
	Queue string
	Op    string
	// Aggregated says these are requests ZFS made by joining smaller ones next to each other on the disk, and not ones as they were asked for.
	Aggregated bool
	Histogram  Histogram
}

// Histogram is a count of requests by how long or how big each was, as Prometheus keeps one.
type Histogram struct {
	// Buckets is how many requests were at or below each bound, smallest first, the last bound being +Inf.
	Buckets []Bucket
	// Sum is what all the requests add up to. ZFS keeps no such total, so it is worked out from the buckets: see each reading's help.
	Sum float64
}

// Bucket is one bound of a histogram, and how many requests were at or below it.
type Bucket struct {
	Bound float64
	Count uint64
}

// Count is how many requests the histogram holds in all.
func (h *Histogram) Count() uint64 {
	if len(h.Buckets) == 0 {
		return 0
	}

	return h.Buckets[len(h.Buckets)-1].Count
}

// Queue is how many requests of one kind are with a pool's disks now, and how many are waiting their turn.
type Queue struct {
	Queue   string
	Op      string
	Active  uint64
	Pending uint64
}
