package machine_test

import (
	"strings"
	"testing"
	"time"

	"github.com/katbyte/zfs-exporter/lib/host"
	"github.com/katbyte/zfs-exporter/lib/machine"
	"github.com/katbyte/zfs-exporter/lib/machine/machinetest"
	"github.com/katbyte/zfs-exporter/lib/poll"
)

// asking is the made-up machine data, and what asks it, given ten seconds for each program.
func asking(t *testing.T, o machine.Options) (string, *machine.Machine) {
	t.Helper()

	root := machinetest.Data(t)
	if o.Limit == 0 {
		o.Limit = 10 * time.Second
	}

	m, err := machine.New(host.New(root), o)
	if err != nil {
		t.Fatal(err)
	}

	return root, m
}

// Every disk zpool status names on the real machine is found among the machine's own, through the machine's own loader, as in a container.
func TestEveryDiskIsFound(t *testing.T) {
	t.Parallel()

	root, m := asking(t, machine.Options{})

	status, err := m.Status(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if status.SlowAfter != 30*time.Second {
		t.Errorf("slow is after %s, want data's 30s", status.SlowAfter)
	}

	disks := 0
	var walk func(v *poll.Vdev)
	walk = func(v *poll.Vdev) {
		for i := range v.Children {
			walk(&v.Children[i])
		}
		if !v.Leaf() {
			return
		}

		disks++
		if v.Disk == nil || v.Disk.Name == "" {
			t.Errorf("%s at %s was not found", v.Name, v.Path)
		}
	}
	for _, p := range status.Pools {
		for i := range p.Vdevs {
			walk(&p.Vdevs[i])
		}
	}

	if disks != 62 {
		t.Errorf("%d disks were found, want data's 56, its 2 cache disks and 2 spares, and the disk of each of the other two pools", disks)
	}

	if got := machinetest.Asked(t, root, machinetest.Zpool); len(got) != 1 || got[0] != "status -s -p -j --json-int" {
		t.Errorf("zpool was asked %q", got)
	}

	first := status.Pools[1].Vdevs[0].Children[0].Disk
	if first.Name != "sdf" || first.Serial == "" || first.Enclosure != "1:0:24:0" || first.Slot != "4" {
		t.Errorf("data's first disk is %+v", first)
	}
}

// zpool_influxdb is asked for its histograms only when they are wanted.
func TestStatsAreAskedForWhatIsWanted(t *testing.T) {
	t.Parallel()

	for _, histograms := range []bool{true, false} {
		root, m := asking(t, machine.Options{Stats: true, Histograms: histograms})

		stats, err := m.Stats(t.Context())
		if err != nil || len(stats.Pools) != 3 {
			t.Fatalf("got %d pools (%v)", len(stats.Pools), err)
		}

		want := "--sum-histogram-buckets"
		if !histograms {
			want = "--no-histograms " + want
		}
		if got := machinetest.Asked(t, root, machinetest.Influx); len(got) != 1 || got[0] != want {
			t.Errorf("with histograms %t zpool_influxdb was asked %q, want %q", histograms, got, want)
		}
	}
}

// A program that fails says why, as it said it, and one that hangs is given up on and counted.
func TestAProgramThatFailsOrHangs(t *testing.T) {
	t.Parallel()

	root, m := asking(t, machine.Options{Limit: 5 * time.Second, Stats: true})

	machinetest.Fail(t, root, machinetest.Zpool, "cannot open '/dev/zfs': Permission denied")
	if _, err := m.Status(t.Context()); err == nil || !strings.Contains(err.Error(), "Permission denied") {
		t.Errorf("got %v, want what zpool said", err)
	}

	root, m = asking(t, machine.Options{Limit: 300 * time.Millisecond, Stats: true})
	machinetest.Hang(t, root, machinetest.Influx)
	if _, err := m.Stats(t.Context()); err == nil || !strings.Contains(err.Error(), "did not finish") {
		t.Errorf("got %v, want it given up on", err)
	}

	if got := m.Commands(); len(got) != 1 || got[0] != (poll.Command{Name: "zpool_influxdb", Timeouts: 1}) {
		t.Errorf("got %+v", got)
	}
}

// A machine that lacks a program says so at the start, where it looked.
func TestAMachineWithoutTheProgramsSaysSo(t *testing.T) {
	t.Parallel()

	if _, err := machine.New(host.New(t.TempDir()), machine.Options{Limit: time.Second}); err == nil || !strings.Contains(err.Error(), "/usr/sbin/zpool") {
		t.Errorf("got %v, want where zpool was looked for", err)
	}
}
