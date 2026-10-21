package readings_test

import (
	"math"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/katbyte/go-kt/prometheus/metrics"
	"github.com/katbyte/go-kt/prometheus/metricstest"
	"github.com/katbyte/go-kt/prometheus/poller"
	"github.com/katbyte/zfs-exporter/lib/host"
	"github.com/katbyte/zfs-exporter/lib/machine"
	"github.com/katbyte/zfs-exporter/lib/machine/machinetest"
	"github.com/katbyte/zfs-exporter/lib/poll"
	"github.com/katbyte/zfs-exporter/lib/readings"
	"github.com/katbyte/zfs-exporter/lib/skip"
	"github.com/prometheus/client_golang/prometheus"
)

// fixed is a machine whose pools are in whatever state the test says.
type fixed poll.State

func (f fixed) State() poll.State { return poll.State(f) }

// answered is an answer given now.
func answered[T any](value T) poll.Answer[T] {
	return poll.Answer[T]{Value: value, At: time.Now()}
}

// histogram is a made-up histogram of three buckets, as ZFS counts them: up to 2047ns, up to 4095ns, and everything.
func histogram(below2047, below4095, all uint64) poll.Histogram {
	return poll.Histogram{Buckets: []poll.Bucket{{Bound: 2047e-9, Count: below2047}, {Bound: 4095e-9, Count: below4095}, {Bound: math.Inf(1), Count: all}}, Sum: float64(all) * 3e-9}
}

// sizes is a made-up histogram of request sizes, as ZFS counts them: up to 1023 bytes, up to 8191, and everything.
func sizes(below1023, below8191, all uint64) poll.Histogram {
	return poll.Histogram{Buckets: []poll.Bucket{{Bound: 1023, Count: below1023}, {Bound: 8191, Count: below8191}, {Bound: math.Inf(1), Count: all}}, Sum: float64(all) * 4096}
}

// The made-up pool's disks, by the paths ZFS has for them.
const (
	first  = "/dev/disk/by-partuuid/11111111-1111-1111-1111-111111111111"
	second = "/dev/disk/by-partuuid/22222222-2222-2222-2222-222222222222"
	third  = "/dev/disk/by-partuuid/33333333-3333-3333-3333-333333333333"
	spare  = "/dev/disk/by-partuuid/44444444-4444-4444-4444-444444444444"
)

// scrubbing is a scrub of a terabyte, less a tenth it found it need not read, half read: the last hour at 100MB a second, so 75 minutes are left.
func scrubbing(now time.Time) *poll.Scan {
	return &poll.Scan{Function: poll.ScanScrub, State: poll.ScanScanning, Start: now.Add(-2 * time.Hour), ToExamine: 1e12, Examined: 6e11, Skipped: 1e11, Issued: 4.5e11, Processed: 4096, Errors: 1, PassStart: now.Add(-time.Hour), PassIssued: 3.6e11}
}

// tank is a pool of one raidz2 vdev of three disks: two with what they are and where, one of them with four slow requests counted and two
// checksum errors, and one ZFS can no longer find. It has a cache disk and a spare, which zpool_influxdb counts nothing of, and is halfway through a
// scrub.
func tank() poll.State {
	now := time.Now()
	status := poll.Status{SlowAfter: 30 * time.Second, Pools: []poll.Pool{{Name: "tank", GUID: "900", State: "DEGRADED", Space: &poll.Space{Size: 6e13, Allocated: 2.4e13, Usable: 2e13}, Errors: &poll.Errors{}, DataErrors: new(uint64(0)), Scan: scrubbing(now), Vdevs: []poll.Vdev{
		{Name: "raidz2-0", Type: "raidz", Class: poll.ClassData, GUID: "100", State: "DEGRADED", Space: &poll.Space{Size: 6e13, Allocated: 2.4e13, Usable: 2e13}, Errors: &poll.Errors{}, Children: []poll.Vdev{
			{Name: "11111111-1111-1111-1111-111111111111", Type: "disk", Class: poll.ClassData, GUID: "101", State: "ONLINE", Errors: &poll.Errors{}, Path: first, SlowIOs: new(uint64(0)), Disk: &poll.Disk{Name: "sdb", Partition: "sdb1", Serial: "Z0000001", Model: "WUH722020BL5204", WWN: "5000cca000000001", Enclosure: "1:0:24:0", Slot: "0"}},
			{Name: "22222222-2222-2222-2222-222222222222", Type: "disk", Class: poll.ClassData, GUID: "102", State: "ONLINE", Errors: &poll.Errors{Checksum: 2}, Path: second, SlowIOs: new(uint64(4)), Disk: &poll.Disk{Name: "sdc", Partition: "sdc1", Serial: "Z0000002", Model: "WUH722020BL5204", WWN: "5000cca000000002", Enclosure: "1:0:24:0", Slot: "1"}},
			{Name: "33333333-3333-3333-3333-333333333333", Type: "disk", Class: poll.ClassData, GUID: "103", State: "REMOVED", Errors: &poll.Errors{}, Path: third, SlowIOs: new(uint64(0))},
		}},
		{Name: "nvme0n1p1", Type: "disk", Class: poll.ClassCache, GUID: "104", State: "ONLINE", Space: &poll.Space{Size: 7.6e12, Allocated: 1e12}, Errors: &poll.Errors{}, Path: "/dev/nvme0n1p1", SlowIOs: new(uint64(0)), Disk: &poll.Disk{Name: "nvme0n1", Partition: "nvme0n1p1", Serial: "S0000001", Model: "SAMSUNG MZQL27T6HBLA-00A07"}},
		{Name: "44444444-4444-4444-4444-444444444444", Type: "disk", Class: poll.ClassSpare, GUID: "105", State: "AVAIL", Path: spare, Disk: &poll.Disk{Name: "sdd", Partition: "sdd1", Serial: "Z0000004", Model: "WUH722020BL5204", WWN: "5000cca000000004", Enclosure: "1:0:24:0", Slot: "3"}},
	}}}}

	read := func(total, disk poll.Histogram) []poll.Latency {
		return []poll.Latency{{Wait: poll.WaitTotal, Op: poll.OpRead, Histogram: total}, {Wait: poll.WaitDisk, Op: poll.OpRead, Histogram: disk}, {Wait: poll.WaitDisk, Op: poll.OpWrite, Histogram: histogram(1, 2, 2)}}
	}
	stats := poll.Stats{Pools: []poll.PoolStats{{
		Name: "tank",
		Vdevs: []poll.VdevStats{
			{Name: poll.Root, Operations: &poll.Operations{Reads: 30, Writes: 6, ReadBytes: 122880, WrittenBytes: 24576}, Latency: read(histogram(10, 25, 30), histogram(12, 26, 30)), Sizes: []poll.Size{{Queue: poll.QueueSync, Op: poll.OpRead, Histogram: sizes(0, 30, 30)}, {Queue: poll.QueueAsync, Op: poll.OpWrite, Aggregated: true, Histogram: sizes(0, 0, 6)}}},
			{Name: "root/raidz-0", Operations: &poll.Operations{Reads: 30, Writes: 6, ReadBytes: 122880, WrittenBytes: 24576}, Fragmentation: new(uint64(15)), Latency: read(histogram(10, 25, 30), histogram(12, 26, 30))},
			{Name: "root/raidz-0/disk-0", Path: first, Operations: &poll.Operations{Reads: 15, Writes: 3}, Latency: read(histogram(5, 12, 15), histogram(6, 13, 15)), Sizes: []poll.Size{{Queue: poll.QueueSync, Op: poll.OpRead, Histogram: sizes(0, 15, 15)}}},
			{Name: "root/raidz-0/disk-1", Path: second, Operations: &poll.Operations{Reads: 15, Writes: 3}, Latency: read(histogram(5, 13, 15), histogram(6, 13, 15))},
			{Name: "root/raidz-0/disk-2", Latency: read(histogram(0, 0, 0), histogram(0, 0, 0))},
		},
		Queues: []poll.Queue{{Queue: poll.QueueSync, Op: poll.OpRead, Active: 1, Pending: 2}, {Queue: poll.QueueScrub, Op: poll.OpRead}},
	}}}

	return poll.State{
		Status:      poll.Answer[poll.Status]{Value: status, At: now},
		Stats:       answered(stats),
		StatsAsked:  true,
		StatusRound: poll.RoundState{Ran: true, OK: true, Took: 800 * time.Millisecond, LastSuccess: time.Now()},
		StatsRound:  poll.RoundState{Ran: true, OK: true, Took: 600 * time.Millisecond, LastSuccess: time.Now(), Failures: 1},
		Commands:    []poll.Command{{Name: "zpool"}, {Name: "zpool_influxdb", Timeouts: 1}},
	}
}

// asking is go-kt's poller holding how the asking went, as the program's own would after asking: the readings of that are its to write.
func asking(t *testing.T, s *poll.State) prometheus.Collector {
	t.Helper()

	p, err := poller.New(poller.Options[string]{Namespace: readings.Namespace, Kind: "pools"})
	if err != nil {
		t.Fatal(err)
	}

	p.Schedule(string(poll.RoundStatus), time.Minute)
	p.Schedule(string(poll.RoundStats), time.Minute)
	p.SetRound("", string(poll.RoundStatus), poller.Round(s.StatusRound))
	p.SetRound("", string(poll.RoundStats), poller.Round(s.StatsRound))

	return p
}

// page is the page these collectors write.
func page(t *testing.T, collectors ...prometheus.Collector) string {
	t.Helper()

	p, err := metrics.New(metrics.Options{Name: "zfs-exporter"}, collectors...)
	if err != nil {
		t.Fatal(err)
	}

	var out strings.Builder
	if err := p.Write(&out); err != nil {
		t.Fatal(err)
	}

	return out.String()
}

// write is the page of this state's readings.
func write(t *testing.T, s poll.State, parts skip.Set) string {
	t.Helper()

	return page(t, readings.New(fixed(s)).Skipping(parts))
}

// lines is the page's readings without the comments above each, and without the one line that changes with the Go it was built by.
func lines(text string) []string {
	var out []string
	for line := range strings.Lines(text) {
		if line = strings.TrimSpace(line); line != "" && !strings.HasPrefix(line, "#") && !strings.HasPrefix(line, "zfs_exporter_build_info") {
			out = append(out, line)
		}
	}

	return out
}

// has says whether the page holds a reading that starts as given: its name, or its name and labels.
func has(text, start string) bool {
	for _, line := range lines(text) {
		if strings.HasPrefix(line, start) {
			return true
		}
	}

	return false
}

// counted is how many of the page's readings start as given.
func counted(text, start string) int {
	n := 0
	for _, line := range lines(text) {
		if strings.HasPrefix(line, start) {
			n++
		}
	}

	return n
}

// expect fails the test for each of these readings the page lacks.
func expect(t *testing.T, text string, wanted ...string) {
	t.Helper()

	for _, want := range wanted {
		if !has(text, want) {
			t.Errorf("the page lacks %s:\n%s", want, text)
		}
	}
}

// The page is what dashboards and alerts are written against, so the whole of it, the poller's readings of how the asking went included, is kept
// in testdata/page.txt and any change to a name, a label or a help text shows up as a difference there: go test ./lib/readings -update rewrites it
// once the change is meant. It is written at the start of the year 2000, where the test's clock begins.
func TestThePageIsWhatWasSaved(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		s := tank()
		metricstest.Saved(t, page(t, readings.New(fixed(s)), asking(t, &s)), "testdata/page.txt")
	})
}

// The names follow the conventions Prometheus checks for: a unit at the end, _total on what only goes up, help on everything.
func TestThePageFollowsTheNamingConventions(t *testing.T) {
	t.Parallel()

	s := tank()
	metricstest.Lint(t, page(t, readings.New(fixed(s)), asking(t, &s)))
}

// Every reading of the pools says which pool it is of; only whether ZFS is answering, and how the asking goes, are of the machine.
func TestEveryReadingSaysWhichPool(t *testing.T) {
	t.Parallel()

	for _, line := range lines(write(t, tank(), nil)) {
		if !strings.Contains(line, `pool="tank"`) && !strings.HasPrefix(line, "zfs_up") && !strings.HasPrefix(line, "zfs_command_") && !strings.HasPrefix(line, "zfs_slow_io_threshold_seconds") {
			t.Errorf("a reading does not say which pool it is of: %s", line)
		}
	}
}

// Each disk is labelled as truenas-exporter labels it, by its pool, class, vdev, the kernel's name for it and its serial number; a disk ZFS cannot
// find is labelled by its GUID. Each count of a disk is of the disk alone: how long it took itself, and its slow requests.
func TestEachDiskIsLabelledToJoinTheOthers(t *testing.T) {
	t.Parallel()

	got := write(t, tank(), nil)
	expect(t, got,
		`zfs_disk_info{class="data",disk="sdb",enclosure="1:0:24:0",guid="101",model="WUH722020BL5204",path="`+first+`",pool="tank",serial="Z0000001",slot="0",vdev="raidz2-0",wwn="5000cca000000001"} 1`,
		`zfs_disk_info{class="data",disk="103",enclosure="",guid="103",model="",path="`+third+`",pool="tank",serial="",slot="",vdev="raidz2-0",wwn=""} 1`,
		`zfs_disk_info{class="cache",disk="nvme0n1",enclosure="",guid="104",model="SAMSUNG MZQL27T6HBLA-00A07",path="/dev/nvme0n1p1",pool="tank",serial="S0000001",slot="",vdev="nvme0n1p1",wwn=""} 1`,
		`zfs_disk_info{class="spare",disk="sdd",`,
		`zfs_disk_slow_ios_total{class="data",disk="sdc",pool="tank",serial="Z0000002",vdev="raidz2-0"} 4`,
		`zfs_disk_latency_seconds_bucket{class="data",disk="sdb",op="read",pool="tank",serial="Z0000001",vdev="raidz2-0",wait="disk",le="4.095e-06"} 13`,
		`zfs_disk_latency_seconds_count{class="data",disk="103",op="write",pool="tank",serial="",vdev="raidz2-0",wait="disk"} 2`,
	)

	for _, absent := range []string{`zfs_disk_latency_seconds_count{class="data",disk="sdb",op="read",pool="tank",serial="Z0000001",vdev="raidz2-0",wait="total"`, "zfs_disk_request_size_bytes", "zfs_disk_operations_total"} {
		if has(got, absent) {
			t.Errorf("the page has %s, which only every disk timing puts on it", absent)
		}
	}
	if has(got, `zfs_disk_slow_ios_total{class="spare"`) {
		t.Error("a spare has a count of slow requests, which zpool status gives it none of")
	}
}

// The pool as a whole and each top-level vdev have every count zpool_influxdb gives them.
func TestThePoolAndItsVdevs(t *testing.T) {
	t.Parallel()

	expect(t, write(t, tank(), nil),
		`zfs_pool_operations_total{op="read",pool="tank"} 30`,
		`zfs_pool_bytes_total{op="write",pool="tank"} 24576`,
		`zfs_pool_latency_seconds_bucket{op="read",pool="tank",wait="total",le="2.047e-06"} 10`,
		`zfs_pool_request_size_bytes_count{aggregated="true",op="write",pool="tank",queue="async"} 6`,
		`zfs_pool_active_requests{op="read",pool="tank",queue="sync"} 1`,
		`zfs_pool_pending_requests{op="read",pool="tank",queue="sync"} 2`,
		`zfs_vdev_operations_total{class="data",op="read",pool="tank",vdev="raidz2-0"} 30`,
		`zfs_vdev_latency_seconds_sum{class="data",op="read",pool="tank",vdev="raidz2-0",wait="disk"} 9e-08`,
		`zfs_slow_io_threshold_seconds 30`,
		`zfs_up 1`,
		`zfs_command_timeouts_total{command="zpool_influxdb"} 1`,
		`zfs_command_stuck{command="zpool"} 0`,
	)
}

// Every disk timing puts on each disk all that zpool_influxdb counts of it, as it does of a vdev, each kind still left out when told: without it a
// disk has only its time at the disk.
func TestEveryDiskTiming(t *testing.T) {
	t.Parallel()

	got := page(t, readings.New(fixed(tank())).EveryDiskTiming())
	expect(t, got,
		`zfs_disk_latency_seconds_count{class="data",disk="sdb",op="read",pool="tank",serial="Z0000001",vdev="raidz2-0",wait="total"} 15`,
		`zfs_disk_latency_seconds_count{class="data",disk="sdb",op="read",pool="tank",serial="Z0000001",vdev="raidz2-0",wait="disk"} 15`,
		`zfs_disk_request_size_bytes_count{aggregated="false",class="data",disk="sdb",op="read",pool="tank",queue="sync",serial="Z0000001",vdev="raidz2-0"} 15`,
		`zfs_disk_operations_total{class="data",disk="sdb",op="read",pool="tank",serial="Z0000001",vdev="raidz2-0"} 15`,
	)
	metricstest.Lint(t, got)

	parts, err := skip.Parse("request-sizes,operations")
	if err != nil {
		t.Fatal(err)
	}

	got = page(t, readings.New(fixed(tank())).Skipping(parts).EveryDiskTiming())
	expect(t, got, `zfs_disk_latency_seconds_count{class="data",disk="sdb",op="read",pool="tank",serial="Z0000001",vdev="raidz2-0",wait="total"} 15`)
	for _, absent := range []string{"zfs_disk_request_size_bytes", "zfs_disk_operations_total"} {
		if has(got, absent) {
			t.Errorf("with request-sizes and operations left out, every disk timing puts %s on the page", absent)
		}
	}
}

// Each pool, vdev and disk has its health as zpool status gives it, a reading for each state it could be in, and the errors counted of it.
func TestHealth(t *testing.T) {
	t.Parallel()

	got := write(t, tank(), nil)
	expect(t, got,
		`zfs_pool_state{pool="tank",state="DEGRADED"} 1`,
		`zfs_pool_state{pool="tank",state="ONLINE"} 0`,
		`zfs_pool_errors_total{error="checksum",pool="tank"} 0`,
		`zfs_pool_data_errors{pool="tank"} 0`,
		`zfs_vdev_state{class="data",pool="tank",state="DEGRADED",vdev="raidz2-0"} 1`,
		`zfs_vdev_errors_total{class="data",error="read",pool="tank",vdev="raidz2-0"} 0`,
		`zfs_disk_state{class="data",disk="103",pool="tank",serial="",state="REMOVED",vdev="raidz2-0"} 1`,
		`zfs_disk_state{class="spare",disk="sdd",pool="tank",serial="Z0000004",state="AVAIL",vdev="sdd1"} 1`,
		`zfs_disk_errors_total{class="data",disk="sdc",error="checksum",pool="tank",serial="Z0000002",vdev="raidz2-0"} 2`,
	)

	if has(got, `zfs_disk_errors_total{class="spare"`) {
		t.Error("a spare has errors counted, which zpool status gives it none of")
	}
	if has(got, `zfs_disk_state{class="data",disk="sdb",pool="tank",serial="Z0000001",state="AVAIL"`) {
		t.Error("a disk that is no spare has AVAIL among its states")
	}
}

// Each pool and top-level vdev has how big it is and how much of it is used, as zpool status gives them, and each vdev how broken up its free
// space is, as zpool_influxdb gives it.
func TestSpace(t *testing.T) {
	t.Parallel()

	got := write(t, tank(), nil)
	expect(t, got,
		`zfs_pool_size_bytes{pool="tank"} 6e+13`,
		`zfs_pool_allocated_bytes{pool="tank"} 2.4e+13`,
		`zfs_pool_usable_bytes{pool="tank"} 2e+13`,
		`zfs_vdev_size_bytes{class="data",pool="tank",vdev="raidz2-0"} 6e+13`,
		`zfs_vdev_allocated_bytes{class="cache",pool="tank",vdev="nvme0n1p1"} 1e+12`,
		`zfs_vdev_fragmentation_ratio{class="data",pool="tank",vdev="raidz2-0"} 0.15`,
	)

	if has(got, `zfs_vdev_usable_bytes{class="cache"`) {
		t.Error("a cache disk has a usable size, which zpool status gives it none of")
	}
	if has(got, `zfs_vdev_size_bytes{class="spare"`) {
		t.Error("a spare has a size, which zpool status gives it none of")
	}
}

// A pool's last scan is written as a scrub or a resilver, by what it was: how far along it is, how fast it goes and how long it has left while it
// runs, and when it ended once it has.
func TestScans(t *testing.T) {
	t.Parallel()

	got := write(t, tank(), nil)
	expect(t, got,
		`zfs_pool_scrub_state{pool="tank",state="SCANNING"} 1`,
		`zfs_pool_scrub_state{pool="tank",state="PAUSED"} 0`,
		`zfs_pool_scrub_done_ratio{pool="tank"} 0.5`,
		`zfs_pool_scrub_rate_bytes_per_second{pool="tank"} 1e+08`,
		`zfs_pool_scrub_remaining_seconds{pool="tank"} 4500`,
		`zfs_pool_scrub_repaired_bytes{pool="tank"} 4096`,
		`zfs_pool_scrub_errors{pool="tank"} 1`,
	)
	for _, absent := range []string{"zfs_pool_scrub_end_timestamp_seconds", "zfs_pool_resilver_"} {
		if has(got, absent) {
			t.Errorf("a pool part way through a scrub has %s", absent)
		}
	}

	paused := tank()
	paused.Status.Value.Pools[0].Scan.Paused = paused.Status.At.Add(-time.Minute)
	got = write(t, paused, nil)
	expect(t, got, `zfs_pool_scrub_state{pool="tank",state="PAUSED"} 1`, `zfs_pool_scrub_state{pool="tank",state="SCANNING"} 0`, `zfs_pool_scrub_done_ratio{pool="tank"} 0.5`)
	for _, absent := range []string{"zfs_pool_scrub_rate_bytes_per_second", "zfs_pool_scrub_remaining_seconds"} {
		if has(got, absent) {
			t.Errorf("a paused scrub has %s", absent)
		}
	}

	resilvered := tank()
	scan := resilvered.Status.Value.Pools[0].Scan
	scan.Function, scan.State, scan.End = poll.ScanResilver, poll.ScanFinished, resilvered.Status.At.Add(-time.Minute)
	got = page(t, readings.New(fixed(resilvered)))
	metricstest.Lint(t, got)
	expect(t, got, `zfs_pool_resilver_state{pool="tank",state="FINISHED"} 1`, `zfs_pool_resilver_resilvered_bytes{pool="tank"} 4096`, `zfs_pool_resilver_end_timestamp_seconds{pool="tank"}`)
	for _, absent := range []string{"zfs_pool_scrub_", "zfs_pool_resilver_done_ratio", "zfs_pool_resilver_rate_bytes_per_second", "zfs_pool_resilver_remaining_seconds"} {
		if has(got, absent) {
			t.Errorf("a pool whose last scan was a finished resilver has %s", absent)
		}
	}
}

// With nothing yet from zpool status to say where each vdev and disk is, only the pool as a whole is written: a vdev or a disk without its labels
// would be written under ones that change when status answers.
func TestWithoutTheStatusOnlyThePools(t *testing.T) {
	t.Parallel()

	s := tank()
	s.Status = poll.Answer[poll.Status]{}

	got := write(t, s, nil)
	expect(t, got, `zfs_pool_operations_total{op="read",pool="tank"} 30`)
	for _, absent := range []string{"zfs_vdev_", "zfs_disk_"} {
		if has(got, absent) {
			t.Errorf("the page has %s with nothing to say where it is:\n%s", absent, got)
		}
	}
}

// Before every round has run, whether ZFS is answering is not said, and a round that failed says it is not.
func TestWhetherZFSIsUp(t *testing.T) {
	t.Parallel()

	s := tank()
	s.StatsRound = poll.RoundState{}
	if got := write(t, s, nil); has(got, "zfs_up") {
		t.Errorf("zfs_up is on the page before the stats have been asked for:\n%s", got)
	}

	s.StatsRound = poll.RoundState{Ran: true, OK: false, Failures: 1}
	expect(t, write(t, s, nil), "zfs_up 0")

	s.StatsAsked = false
	expect(t, write(t, s, nil), "zfs_up 1")
}

// A part left out is off the page whole, and whether ZFS is up is on it whatever is left out.
func TestPartsLeftOutAreOffThePage(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		parts   string
		absent  []string
		present []string
	}{
		{"disks", []string{"zfs_disk_"}, []string{"zfs_vdev_latency_seconds", "zfs_pool_latency_seconds", "zfs_up"}},
		{"latency", []string{"zfs_disk_latency", "zfs_vdev_latency", "zfs_pool_latency"}, []string{"zfs_disk_info", "zfs_pool_request_size_bytes"}},
		{"poll,queues,slow-ios", []string{"zfs_command_", "zfs_pool_active_requests", "zfs_disk_slow_ios_total", "zfs_slow_io_threshold_seconds"}, []string{"zfs_disk_info", "zfs_up"}},
		{"pools,vdevs,operations,request-sizes", []string{"zfs_pool_", "zfs_vdev_"}, []string{"zfs_disk_latency_seconds", "zfs_disk_info"}},
		{"health", []string{"zfs_pool_state", "zfs_pool_errors_total", "zfs_pool_data_errors", "zfs_vdev_state", "zfs_vdev_errors_total", "zfs_disk_state", "zfs_disk_errors_total"}, []string{"zfs_disk_info", "zfs_pool_size_bytes"}},
		{"space", []string{"zfs_pool_size_bytes", "zfs_pool_usable_bytes", "zfs_vdev_size_bytes", "zfs_vdev_fragmentation_ratio"}, []string{"zfs_vdev_latency_seconds", "zfs_pool_state"}},
		{"scrub", []string{"zfs_pool_scrub_"}, []string{"zfs_pool_state", "zfs_pool_size_bytes"}},
	} {
		parts, err := skip.Parse(tc.parts)
		if err != nil {
			t.Fatal(err)
		}

		got := write(t, tank(), parts)
		for _, absent := range tc.absent {
			if has(got, absent) {
				t.Errorf("with %s left out the page has %s", tc.parts, absent)
			}
		}
		for _, present := range tc.present {
			if !has(got, present) {
				t.Errorf("with %s left out the page lacks %s", tc.parts, present)
			}
		}
	}
}

// On the real machine data, every disk zpool status names is on the page under the kernel's name for it and its serial number, and each disk and
// vdev zpool_influxdb counts is found where zpool status says it is: so the two programs' names for things agree.
func TestTheRealMachine(t *testing.T) {
	t.Parallel()

	m, err := machine.New(host.New(machinetest.Data(t)), machine.Options{Limit: 5 * time.Second, Stats: true, Histograms: true})
	if err != nil {
		t.Fatal(err)
	}

	status, err := m.Status(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	stats, err := m.Stats(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	s := poll.State{Status: answered(status), Stats: answered(stats), StatsAsked: true, StatusRound: poll.RoundState{Ran: true, OK: true}, StatsRound: poll.RoundState{Ran: true, OK: true}}
	got := page(t, readings.New(fixed(s)), asking(t, &s))
	metricstest.Lint(t, got)

	if n := counted(got, "zfs_disk_info{"); n != 62 {
		t.Errorf("%d disks are on the page, want data's 56, its 2 cache disks and 2 spares, and the disk of each other pool", n)
	}
	for _, line := range lines(got) {
		if strings.HasPrefix(line, `zfs_disk_`) && strings.Contains(line, `pool="data"`) && strings.Contains(line, `serial=""`) {
			t.Errorf("a disk of data has no serial number: %s", line)
		}
	}

	// the lines kept of zpool_influxdb's are of data's first two vdevs and their sixteen disks, and the one disk of each other pool
	if n := counted(got, "zfs_disk_latency_seconds_count{"); n != 2*18 {
		t.Errorf("%d disks' latencies are on the page, want reads and writes of 18", n/2)
	}
	if n := counted(got, "zfs_vdev_latency_seconds_count{"); n != 11*4 {
		t.Errorf("%d vdevs' latencies are on the page, want 11 kinds of 4", n)
	}

	expect(t, got,
		`zfs_disk_info{class="data",disk="sdf",enclosure="1:0:24:0",guid="`,
		`zfs_disk_latency_seconds_count{class="data",disk="sdf",op="read",pool="data",serial="`,
		`zfs_vdev_operations_total{class="data",op="read",pool="data",vdev="raidz2-1"}`,
		`zfs_vdev_operations_total{class="data",op="read",pool="boot-pool",vdev="sda3"}`,
		`zfs_disk_latency_seconds_count{class="data",disk="sda",op="read",pool="boot-pool",serial="",vdev="sda3",wait="disk"}`,
		`zfs_disk_info{class="cache",disk="nvme1n1",`,
		`zfs_disk_slow_ios_total{class="data",disk="sdf",pool="data",serial="`,
		`zfs_pool_active_requests{op="read",pool="data",queue="sync"} 0`,
		`zfs_pool_state{pool="data",state="ONLINE"} 1`,
		`zfs_pool_size_bytes{pool="data"} 1.119972851974144e+15`,
		`zfs_pool_scrub_state{pool="data",state="FINISHED"} 1`,
		`zfs_pool_scrub_end_timestamp_seconds{pool="data"}`,
		`zfs_vdev_fragmentation_ratio{class="data",pool="data",vdev="raidz2-0"} 0.15`,
		`zfs_disk_state{class="spare",disk="`,
		`zfs_disk_errors_total{class="data",disk="sdf",error="checksum",pool="data",serial="`,
	)

	if has(got, `zfs_pool_scrub_state{pool="host"`) {
		t.Error("the pool host, which has had no scrub, has one on the page")
	}
}
