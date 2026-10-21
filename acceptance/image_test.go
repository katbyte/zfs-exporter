//go:build acceptance

package acceptance_test

import (
	"context"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/katbyte/go-kt/prometheus/metrics"
	"github.com/katbyte/go-kt/prometheus/metricstest"
	"github.com/katbyte/zfs-exporter/lib/machine/machinetest"
)

// image is the program's own image, as make docker builds it. make testacc builds it and names it here.
var image = os.Getenv("ZFS_EXPORTER_TEST_PROGRAM_IMAGE")

// patience is how long a page is waited for before a test gives up on it.
const patience = 30 * time.Second

// docker runs one docker command and gives back what it printed. It is not stopped when the test ends, so a cleanup can still put things back.
func docker(t *testing.T, args ...string) (string, error) {
	t.Helper()

	cmd := exec.CommandContext(context.WithoutCancel(t.Context()), "docker", args...) //nolint:gosec // every argument is one a test here wrote
	out, err := cmd.CombinedOutput()

	return strings.TrimSpace(string(out)), err
}

// readable lets anyone read the made-up machine, as anyone can read a real one's programs and /sys: the image's user is not the one that laid it out.
func readable(t *testing.T, root string) {
	t.Helper()

	if err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.Type()&fs.ModeSymlink != 0 {
			return err
		}

		info, err := d.Info()
		if err != nil {
			return err
		}

		// a directory can be gone into, and a program run, by anyone
		mode := info.Mode().Perm() | 0o444
		if d.IsDir() || mode&0o100 != 0 {
			mode |= 0o111
		}

		return os.Chmod(path, mode) //nolint:gosec // G122: the machine is the test's own, made a moment ago, and nothing else is changing it
	}); err != nil {
		t.Fatal(err)
	}
}

// mounted is where the image looks for the machine's own /, as a deployment mounts it.
const mounted = "/host"

// started starts the image under that name with the machine at root where a deployment mounts one, running each program every second and giving it
// half that, and gives back where its page is. The machine is copied in rather than mounted, since docker may run somewhere this machine's files
// are not. It is removed when the test ends.
func started(t *testing.T, name, root string) string {
	t.Helper()

	if image == "" {
		t.Skip("no image to run: make testacc builds one and names it in ZFS_EXPORTER_TEST_PROGRAM_IMAGE")
	}

	_, _ = docker(t, "rm", "--force", name)
	t.Cleanup(func() { _, _ = docker(t, "rm", "--force", name) })

	// the health check is asked every two seconds where the image says thirty, so the test need not wait half a minute for the first
	args := []string{"create", "--name", name, "--publish", "127.0.0.1::9087", "--health-interval", "2s", "--env", "ZFS_EXPORTER_INTERVALS=status=1s,stats=1s", "--env", "ZFS_EXPORTER_TIMEOUT=500ms", "--env", "ZFS_EXPORTER_LOG=INFO"}
	if out, err := docker(t, append(args, image)...); err != nil {
		t.Fatalf("making a container of the image %s: %v: %s", image, err, out)
	}

	readable(t, root)
	if out, err := docker(t, "cp", root+"/.", name+":"+mounted); err != nil {
		t.Fatalf("handing the container the machine: %v: %s", err, out)
	}

	if out, err := docker(t, "start", name); err != nil {
		t.Fatalf("starting the container: %v: %s", err, out)
	}

	published, err := docker(t, "port", name, "9087/tcp")
	if err != nil || published == "" {
		logs, _ := docker(t, "logs", name)
		t.Fatalf("finding where the image's port is published: %v: %s\nits log:\n%s", err, published, logs)
	}

	return "http://" + strings.TrimSpace(strings.Split(published, "\n")[0])
}

// count is how many readings of that name the page holds.
func count(t *testing.T, page, name string) int {
	t.Helper()

	return len(metricstest.Readings(t, page, name))
}

// histograms is how many histograms of that name the page holds, which go-kt's reader of pages passes over.
func histograms(page, name string) int {
	n := 0
	for line := range strings.Lines(page) {
		if strings.HasPrefix(line, name+"_count{") {
			n++
		}
	}

	return n
}

// served reads the page until it satisfies check or patience runs out, and gives back the last page read and whether it did.
func served(t *testing.T, base string, check func(page string) bool) (string, bool) {
	t.Helper()

	return metricstest.Until(t, base+metrics.Path, patience, check)
}

// one is the value of the one reading of that name with these labels, and -1 when there is not exactly one.
func one(t *testing.T, page, name string, labels map[string]string) float64 {
	t.Helper()

	value, found := metricstest.One(t, page, name, labels)
	if !found {
		return -1
	}

	return value
}

// healthy waits for the image's own health check to pass.
func healthy(t *testing.T, name string) {
	t.Helper()

	deadline := time.Now().Add(patience)
	for {
		health, err := docker(t, "inspect", "--format", "{{.State.Health.Status}}", name)
		if health == "healthy" {
			return
		}
		if time.Now().After(deadline) {
			t.Errorf("the image's own health check says %q (%v), want healthy", health, err)
			return
		}

		time.Sleep(500 * time.Millisecond)
	}
}

// The image a deployment runs, given the machine data's / where a deployment gives it: it runs the machine's programs through the machine's own
// loader, finds every disk, serves a page with each by its serial number, passes its own health check, runs as someone other than root, and goes
// on asking. A program that then hangs is given up on and said to have been, while the other goes on answering, and once it answers again ZFS is
// up again.
func TestTheImageReadsARealMachine(t *testing.T) {
	root := machinetest.Data(t)
	name := "zfs-exporter-acceptance-data"
	base := started(t, name, root)

	page, ok := served(t, base, func(page string) bool {
		return one(t, page, "zfs_up", nil) == 1 && count(t, page, "zfs_disk_info") == 62
	})
	if !ok {
		logs, _ := docker(t, "logs", name)
		t.Fatalf("the image never served a page with ZFS up and every disk:\n%s\nits log:\n%s", page, logs)
	}

	for reading, want := range map[string]int{"zfs_disk_slow_ios_total": 60, "zfs_pool_operations_total": 2 * 3} {
		if got := count(t, page, reading); got != want {
			t.Errorf("%d readings of %s, want %d", got, reading, want)
		}
	}
	for reading, want := range map[string]int{"zfs_disk_latency_seconds": 2 * 18, "zfs_vdev_latency_seconds": 11 * 4} {
		if got := histograms(page, reading); got != want {
			t.Errorf("%d histograms of %s, want %d", got, reading, want)
		}
	}
	for _, want := range []string{`zfs_disk_info{class="data",disk="sdf",enclosure="1:0:24:0",`, `zfs_slow_io_threshold_seconds 30`, `zfs_command_stuck{command="zpool"} 0`} {
		if !strings.Contains(page, want) {
			t.Errorf("the page lacks %s", want)
		}
	}

	if body := metricstest.Get(t, base+metrics.HealthPath); strings.TrimSpace(body) != "ok" {
		t.Errorf("%s answered %q, want ok", metrics.HealthPath, body)
	}
	if who, err := docker(t, "exec", name, "id", "-u"); err != nil || who == "" || who == "0" {
		t.Errorf("the program runs as user %q (%v), want someone other than root", who, err)
	}
	healthy(t, name)

	// it goes on asking on its own schedule, so a later scrape reads a later answer
	lastAsked := func(page string) float64 {
		return one(t, page, "zfs_poll_last_success_timestamp_seconds", map[string]string{"poll": "status"})
	}
	first := lastAsked(page)
	if later, asked := served(t, base, func(later string) bool { return lastAsked(later) > first }); !asked {
		t.Errorf("zpool status last answered at %v and the page never showed a later answer than %v", lastAsked(later), first)
	}

	// zpool_influxdb hangs: it is given up on and counted, and zpool status goes on answering
	if out, err := docker(t, "exec", "--user", "0", name, "touch", mounted+"/"+machinetest.Influx+".hang"); err != nil {
		t.Fatalf("making zpool_influxdb hang: %v: %s", err, out)
	}
	hung, ok := served(t, base, func(page string) bool {
		return one(t, page, "zfs_up", nil) == 0 && one(t, page, "zfs_command_timeouts_total", map[string]string{"command": "zpool_influxdb"}) >= 1
	})
	if !ok {
		t.Fatalf("zpool_influxdb hung and the page never said so:\n%s", hung)
	}
	if failed := one(t, hung, "zfs_poll_failures_total", map[string]string{"poll": "status"}); failed != 0 {
		t.Errorf("zpool status failed %v rounds while zpool_influxdb hung, want none", failed)
	}
	if stuck := one(t, hung, "zfs_command_stuck", map[string]string{"command": "zpool_influxdb"}); stuck != 0 {
		t.Error("zpool_influxdb is said to be stuck, though it ended when killed")
	}

	if out, err := docker(t, "exec", "--user", "0", name, "rm", mounted+"/"+machinetest.Influx+".hang"); err != nil {
		t.Fatalf("letting zpool_influxdb answer again: %v: %s", err, out)
	}
	if again, ok := served(t, base, func(page string) bool { return one(t, page, "zfs_up", nil) == 1 }); !ok {
		t.Errorf("zpool_influxdb answered again and the page never said ZFS was up:\n%s", again)
	}

	// it is ZFS that was in trouble, not the program: its own health check still answers
	metricstest.Get(t, base+metrics.HealthPath)
}

// The image given nothing, as it would be deployed without the machine's / mounted: it stops at once, saying where it looked and which setting
// says where, and so does print.
func TestTheImageWithoutTheMachine(t *testing.T) {
	if image == "" {
		t.Skip("no image to run: make testacc builds one and names it in ZFS_EXPORTER_TEST_PROGRAM_IMAGE")
	}

	for _, command := range []string{"serve", "print"} {
		out, err := docker(t, "run", "--rm", image, command)
		if err == nil || !strings.Contains(out, "/host") || !strings.Contains(out, "ZFS_EXPORTER_ROOT") {
			t.Errorf("%s with nothing mounted = %v, saying %q; want it to fail, say where it looked and name the setting", command, err, out)
		}
	}
}
