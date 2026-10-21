package zpool_test

import (
	"math"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/katbyte/go-kt/pointer"
	"github.com/katbyte/zfs-exporter/lib/poll"
	"github.com/katbyte/zfs-exporter/lib/zpool"
	"github.com/katbyte/zfs-exporter/lib/zpool/zpooltest"
)

// Every pool on the real machine is read, with its vdevs of each class in the order zpool status lists them, and every disk with its path and its
// count of slow requests.
func TestTheRealStatus(t *testing.T) {
	t.Parallel()

	pools, err := zpool.Status(zpooltest.Status)
	if err != nil {
		t.Fatal(err)
	}

	if len(pools) != 3 || pools[0].Name != "boot-pool" || pools[1].Name != "data" || pools[2].Name != "host" {
		t.Fatalf("got %d pools, want boot-pool, data and host in that order: %+v", len(pools), pools)
	}

	data := pools[1]
	if data.GUID != "13310023206516862183" || data.State != "ONLINE" {
		t.Errorf("data is %s %s, want its GUID whole and ONLINE", data.GUID, data.State)
	}
	if data.Space == nil || *data.Space != (poll.Space{Size: 1119972851974144, Allocated: 736205182611456, Usable: 796230699450368}) || data.Errors == nil || *data.Errors != (poll.Errors{}) || data.DataErrors == nil || *data.DataErrors != 0 {
		t.Errorf("data's space is %+v, its errors %+v and its damaged data %v, want what its own line says", data.Space, data.Errors, data.DataErrors)
	}

	scan := data.Scan
	if scan == nil || scan.Function != poll.ScanScrub || scan.State != poll.ScanFinished || scan.Running() || !scan.Start.Equal(time.Unix(1790845202, 0)) || !scan.End.Equal(time.Unix(1790961133, 0)) || !scan.Paused.IsZero() || scan.Issued != 774781332185088 || scan.Skipped != 877584384 {
		t.Errorf("data's last scan is %+v, want the scrub that finished", scan)
	}
	if pools[2].Scan != nil {
		t.Errorf("host has had no scrub, but has %+v", pools[2].Scan)
	}

	classes := make([]string, 0, len(data.Vdevs))
	for _, v := range data.Vdevs {
		classes = append(classes, v.Class)
	}
	if want := slices.Concat(slices.Repeat([]string{poll.ClassData}, 7), []string{poll.ClassCache, poll.ClassCache, poll.ClassSpare, poll.ClassSpare}); !slices.Equal(classes, want) {
		t.Errorf("data's vdevs are in classes %s, want %s", classes, want)
	}

	raidz := data.Vdevs[3]
	if raidz.Name != "raidz2-3" || raidz.Type != "raidz" || raidz.Leaf() || len(raidz.Children) != 8 || raidz.SlowIOs != nil {
		t.Errorf("the fourth vdev is %+v, want raidz2-3 of eight disks", raidz)
	}
	if raidz.Space == nil || raidz.Space.Size == 0 || raidz.Space.Usable >= raidz.Space.Size || raidz.Errors == nil {
		t.Errorf("raidz2-3's space is %+v and its errors %+v, want a usable size less than its size with parity", raidz.Space, raidz.Errors)
	}
	for _, disk := range raidz.Children {
		if !disk.Leaf() || disk.Class != poll.ClassData || !strings.HasPrefix(disk.Path, "/dev/disk/by-partuuid/") || disk.SlowIOs == nil || disk.GUID == "" || disk.Errors == nil || disk.Space != nil {
			t.Errorf("a disk of raidz2-3 is %+v, want its errors and no space of its own", disk)
		}
	}
	if raidz.Children[3].Name != "97c664cf-921f-40b8-b859-e1fb4f3a953d" {
		t.Errorf("raidz2-3's fourth disk is %s: the disks are not in the order written", raidz.Children[3].Name)
	}

	cache := data.Vdevs[7]
	if cache.Name != "nvme1n1p1" || cache.Path != "/dev/nvme1n1p1" || cache.SlowIOs == nil || *cache.SlowIOs != 0 || cache.Space == nil || cache.Space.Allocated == 0 || cache.Space.Usable != 0 {
		t.Errorf("the first cache disk is %+v, want its space with no usable size, which zpool status does not give", cache)
	}

	spare := data.Vdevs[9]
	if spare.State != "AVAIL" || spare.SlowIOs != nil || spare.Path == "" || spare.Space != nil || spare.Errors != nil {
		t.Errorf("the first spare is %+v, want one waiting to be used, with no count of slow requests, space or errors", spare)
	}

	boot := pools[0].Vdevs
	if len(boot) != 1 || !boot[0].Leaf() || boot[0].Path != "/dev/sda3" {
		t.Errorf("boot-pool's vdevs are %+v, want the one disk sda3", boot)
	}
}

// A pool's log, special and dedup vdevs are listed apart from those that hold data, each in its class; and numbers written as text, as zpool status
// writes them without --json-int, are read as well.
func TestEveryClass(t *testing.T) {
	t.Parallel()

	disk := func(name string) string {
		return `"` + name + `": {"name": "` + name + `", "vdev_type": "disk", "guid": "1` + name[len(name)-1:] + `", "path": "/dev/` + name + `", "state": "ONLINE", "slow_ios": "3"}`
	}

	pools, err := zpool.Status([]byte(`{"pools": {"tank": {"name": "tank", "state": "DEGRADED", "pool_guid": "77",
		"vdevs": {"tank": {"name": "tank", "vdev_type": "root", "vdevs": {"mirror-0": {"name": "mirror-0", "vdev_type": "mirror", "class": "normal", "vdevs": {` + disk("sda1") + `, ` + disk("sdb1") + `}}}}},
		"special": {"mirror-1": {"name": "mirror-1", "vdev_type": "mirror", "class": "special", "vdevs": {` + disk("sdc1") + `}}},
		"dedup": {` + disk("sdd1") + `},
		"logs": {` + disk("sde1") + `},
		"error_count": "0"}}}`))
	if err != nil {
		t.Fatal(err)
	}

	got := map[string]string{}
	for _, v := range pools[0].Vdevs {
		got[v.Name] = v.Class
	}
	want := map[string]string{"mirror-0": poll.ClassData, "mirror-1": poll.ClassSpecial, "sdd1": poll.ClassDedup, "sde1": poll.ClassLog}
	for name, class := range want {
		if got[name] != class {
			t.Errorf("%s is in class %q, want %q", name, got[name], class)
		}
	}

	if d := pools[0].Vdevs[1].Children[0]; d.Class != poll.ClassSpecial || d.SlowIOs == nil || *d.SlowIOs != 3 || d.GUID != "11" {
		t.Errorf("a disk of the special vdev is %+v, want it in that class with its count read from text", d)
	}
}

// A scan under way has no end, though ZFS still writes the last one's, and a paused scrub says since when. A pool that has had no scan says so with a
// function of NONE.
func TestAScanUnderWay(t *testing.T) {
	t.Parallel()

	pools, err := zpool.Status([]byte(`{"pools": {
		"tank": {"name": "tank", "state": "ONLINE", "pool_guid": "77", "scan_stats": {"function": "SCRUB", "state": "SCANNING", "start_time": 1790845202, "end_time": 1790000000, "to_examine": 1000, "examined": 600, "skipped": 100, "issued": 450, "processed": 0, "errors": 0, "pass_start": 1790845202, "scrub_pause": 1790846202, "scrub_spent_paused": 60, "issued_bytes_per_scan": 450}},
		"zoo": {"name": "zoo", "state": "ONLINE", "pool_guid": "78", "scan_stats": {"function": "NONE", "state": "NONE"}}}}`))
	if err != nil {
		t.Fatal(err)
	}

	s := pools[0].Scan
	if s == nil || !s.Running() || !s.End.IsZero() || !s.Paused.Equal(time.Unix(1790846202, 0)) || s.PassPaused != time.Minute || s.PassIssued != 450 || s.Done() != 0.5 {
		t.Errorf("the scrub under way is %+v, want it running, paused, with no end and half done", s)
	}
	if pools[1].Scan != nil {
		t.Errorf("a pool with no scan has %+v", pools[1].Scan)
	}
}

// What is not zpool status's JSON is an error, not a machine with no pools.
func TestStatusThatIsNotJSON(t *testing.T) {
	t.Parallel()

	for _, out := range []string{"no pools available\n", `{"output_version": {}}`} {
		if _, err := zpool.Status([]byte(out)); err == nil {
			t.Errorf("%q gave no error", out)
		}
	}

	pools, err := zpool.Status([]byte(`{"pools": {}}`))
	if err != nil || len(pools) != 0 {
		t.Errorf("a machine with no pools gave %+v (%v)", pools, err)
	}
}

// Every pool and vdev zpool_influxdb wrote of on the real machine is read, in the order written, with each count it gave.
func TestTheRealStats(t *testing.T) {
	t.Parallel()

	stats, err := zpool.Stats(zpooltest.Stats)
	if err != nil {
		t.Fatal(err)
	}

	if len(stats.Pools) != 3 || stats.Pools[1].Name != "data" {
		t.Fatalf("got %+v, want boot-pool, data and host", stats.Pools)
	}

	data := stats.Pools[1]
	if len(data.Vdevs) != 19 || data.Vdevs[0].Name != poll.Root || data.Vdevs[1].Name != "root/raidz-0" || data.Vdevs[2].Name != "root/raidz-0/disk-0" {
		t.Fatalf("data has %d vdevs, want the pool, two raidz2 vdevs and their sixteen disks, the pool first", len(data.Vdevs))
	}

	if f := data.Vdevs[1].Fragmentation; f == nil || *f != 15 {
		t.Errorf("raidz-0's fragmentation is %v, want 15", f)
	}

	root := data.Vdevs[0]
	if root.Operations == nil || *root.Operations != (poll.Operations{Reads: 838542086, Writes: 477013715, ReadBytes: 249320902512640, WrittenBytes: 33027446173696}) {
		t.Errorf("data's reads and writes are %+v", root.Operations)
	}

	waits := make([]string, 0, len(root.Latency))
	for _, l := range root.Latency {
		waits = append(waits, l.Wait+" "+l.Op)
	}
	if got := strings.Join(waits, ", "); got != "total read, total write, disk read, disk write, sync_queue read, sync_queue write, async_queue read, async_queue write, scrub_queue read, trim_queue write, rebuild_queue write" {
		t.Errorf("data's latencies are %s", got)
	}

	total := root.Latency[0].Histogram
	if len(total.Buckets) != 27 || total.Count() != 838541892 || !math.IsInf(total.Buckets[26].Bound, 1) {
		t.Errorf("data's latency of all reads has %d buckets holding %d, want 27 holding 838541892, the last +Inf", len(total.Buckets), total.Count())
	}
	if first := total.Buckets[0].Bound; first != (math.Exp2(11)-1)*1e-9 {
		t.Errorf("the first bucket ends at %g, want 2047ns: zpool_influxdb writes it as 0.000001, its bottom", first)
	}
	for i, b := range total.Buckets[:26] {
		if want := (math.Exp2(float64(11+i)) - 1) * 1e-9; b.Bound != want {
			t.Errorf("bucket %d ends at %g, want %g: each is twice the one before, rounded as zpool_influxdb writes them or not", i, b.Bound, want)
		}
	}
	if total.Sum <= 0 {
		t.Errorf("the sum is %g, want it worked out from the buckets", total.Sum)
	}

	if first := root.Sizes[0]; len(root.Sizes) != 14 || first.Queue != poll.QueueSync || first.Op != poll.OpRead || first.Aggregated || !root.Sizes[5].Aggregated {
		t.Errorf("data's request sizes are %+v", root.Sizes)
	}
	if got := root.Sizes[0].Histogram.Buckets[0].Bound; got != 1023 {
		t.Errorf("the first size bucket ends at %g, want 1023 bytes: zpool_influxdb writes it as 512, its bottom", got)
	}

	if len(data.Queues) != 6 || data.Queues[4] != (poll.Queue{Queue: poll.QueueScrub, Op: poll.OpRead}) {
		t.Errorf("data's queues are %+v", data.Queues)
	}

	disk := data.Vdevs[5]
	if disk.Name != "root/raidz-0/disk-3" || disk.Path != "/dev/disk/by-partuuid/81af67df-9fe1-4186-bd88-eb625924cde5" || disk.Latency[2].Histogram.Count() != 15782682 {
		t.Errorf("the fourth disk of raidz2-0 is %+v", disk)
	}
}

// Each bucket is labelled with its top, the most it can hold, and the sum counts each request at the middle of its bucket for a latency, as zpool
// iostat -l does, and at the bottom for a size.
func TestBuckets(t *testing.T) {
	t.Parallel()

	stats, err := zpool.Stats([]byte(`zpool_latency,le=0.000001,name=p,vdev=root disk_read=0u 1
zpool_latency,le=0.000002,name=p,vdev=root disk_read=5u 1
zpool_latency,le=+Inf,name=p,vdev=root disk_read=7u 1
zpool_io_size,le=512,name=p,vdev=root sync_read_ind=1u 1
zpool_io_size,le=4096,name=p,vdev=root sync_read_ind=3u 1
zpool_io_size,le=+Inf,name=p,vdev=root sync_read_ind=3u 1
`))
	if err != nil {
		t.Fatal(err)
	}

	latency := stats.Pools[0].Vdevs[0].Latency[0].Histogram
	want := []poll.Bucket{{Bound: (math.Exp2(11) - 1) * 1e-9}, {Bound: (math.Exp2(12) - 1) * 1e-9, Count: 5}, {Bound: math.Inf(1), Count: 7}}
	if len(latency.Buckets) != 3 || latency.Buckets[0] != want[0] || latency.Buckets[1] != want[1] || latency.Buckets[2] != want[2] {
		t.Errorf("the latency's buckets are %+v, want %+v", latency.Buckets, want)
	}
	if want := (5*1.5*2048 + 2*1.5*4096) * 1e-9; math.Abs(latency.Sum-want) > 1e-15 {
		t.Errorf("the latency's sum is %g, want %g", latency.Sum, want)
	}

	size := stats.Pools[0].Vdevs[0].Sizes[0].Histogram
	if size.Buckets[0].Bound != 1023 || size.Buckets[1].Bound != 8191 || size.Sum != 1*512+2*4096 {
		t.Errorf("the size's buckets are %+v with the sum %g", size.Buckets, size.Sum)
	}
}

// A name or a value written with a backslash keeps what follows it, as a path with a space or a comma in it needs; a value that is not a count is
// left out; and a line that cannot be read is an error that says which.
func TestLines(t *testing.T) {
	t.Parallel()

	stats, err := zpool.Stats([]byte(`zpool_stats,name=my\ pool,state=ONLINE,path=/dev/disk/by-id/a\,b\ c,vdev=root/disk-0 read_ops=4u,write_ops=2i,read_bytes=x,write_bytes=8u 1
zpool_scan_stats,function=scrub,name=my\ pool,state=finished pct_done=100.00 1
`))
	if err != nil {
		t.Fatal(err)
	}

	v := stats.Pools[0].Vdevs[0]
	if stats.Pools[0].Name != "my pool" || v.Path != "/dev/disk/by-id/a,b c" || pointer.From(v.Operations) != (poll.Operations{Reads: 4, Writes: 2, WrittenBytes: 8}) {
		t.Errorf("got %+v of %s", v, stats.Pools[0].Name)
	}

	if _, err := zpool.Stats([]byte("zpool_stats,name=p,vdev=root read_ops=1u 1\nnonsense\n")); err == nil || !strings.Contains(err.Error(), "line 2") {
		t.Errorf("a line that cannot be read gave %v", err)
	}
	if _, err := zpool.Stats([]byte("zpool_latency,le=soon,name=p,vdev=root disk_read=1u 1\n")); err == nil {
		t.Error("a bound that is not a number gave no error")
	}
}
