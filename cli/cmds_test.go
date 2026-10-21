package cli

import (
	"bytes"
	"context"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/katbyte/go-kt/prometheus/metrics"
	"github.com/katbyte/go-kt/version"
	"github.com/katbyte/zfs-exporter/lib/machine/machinetest"
	"github.com/spf13/cobra"
)

// program is the program given these arguments, not yet run, and where what it prints will go.
func program(t *testing.T, args ...string) (*cobra.Command, *bytes.Buffer) {
	t.Helper()

	root, err := makeWith(connect)
	if err != nil {
		t.Fatalf("makeWith: %v", err)
	}

	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(io.Discard)
	root.SetArgs(args)

	return root, &out
}

// run executes the program with these arguments, and gives back what it printed.
func run(t *testing.T, args ...string) (string, error) {
	t.Helper()

	root, out := program(t, args...)
	err := root.ExecuteContext(t.Context())

	return out.String(), err
}

// count is how many readings of that name the page holds.
func count(page, name string) int {
	var n int
	for line := range strings.Lines(page) {
		if strings.HasPrefix(line, name+"{") || strings.HasPrefix(line, name+" ") {
			n++
		}
	}

	return n
}

//nolint:paralleltest // viper is global state; Make mutates it
func TestMakeBuildsTheCommands(t *testing.T) {
	alone(t)

	root, err := Make()
	if err != nil {
		t.Fatal(err)
	}

	for _, name := range []string{"version", "print", "serve"} {
		if c, _, err := root.Find([]string{name}); err != nil || c.Name() != name {
			t.Errorf("no %s command: %v", name, err)
		}
	}
	// a setting is a flag on every command, so it can be given after whichever one is run
	for _, name := range []string{"root", "listen", "labels", "metrics", "skip", "intervals", "timeout", "config"} {
		if root.PersistentFlags().Lookup(name) == nil {
			t.Errorf("no --%s flag", name)
		}
	}
}

//nolint:paralleltest // viper is global state; Make mutates it
func TestVersionCommand(t *testing.T) {
	alone(t)

	out, err := run(t, "version")
	if err != nil || !strings.HasPrefix(out, "zfs-exporter ") {
		t.Errorf("version printed %q (%v), want the name and a version", out, err)
	}
}

// The version says which commit it was built from once, whether or not the version already names it.
//
//nolint:paralleltest // the version is global state
func TestVersionString(t *testing.T) {
	before, commit := version.Version, version.GitCommit
	t.Cleanup(func() { version.Version, version.GitCommit = before, commit })

	for _, tc := range []struct{ version, commit, want string }{
		{"v0.1.0", "51999e6", "zfs-exporter v0.1.0 (51999e6)"},
		{"v0.1.0+3@g1a2b3c4", "1a2b3c4", "zfs-exporter v0.1.0+3@g1a2b3c4"},
		{"v0.1.0", "", "zfs-exporter v0.1.0"},
	} {
		version.Version, version.GitCommit = tc.version, tc.commit
		if got := versionString(); got != tc.want {
			t.Errorf("version %q and commit %q read as %q, want %q", tc.version, tc.commit, got, tc.want)
		}
	}
}

// With no command the program says where the help is rather than serving or failing.
//
//nolint:paralleltest // viper is global state; Make mutates it
func TestNoCommandPointsAtHelp(t *testing.T) {
	alone(t)

	out, err := run(t)
	if err != nil || !strings.Contains(out, "zfs-exporter help") {
		t.Errorf("no command printed %q (%v), want a pointer to help", out, err)
	}
}

// The program whole, run against the machine data as a container sees it: its / somewhere else, and its own zpool and zpool_influxdb printing what
// they printed on data. Every disk is on the page by its serial number, and each disk and vdev zpool_influxdb counts is found where it is.
//
//nolint:paralleltest // viper is global state; Make mutates it
func TestPrintReadsTheRealMachine(t *testing.T) {
	alone(t)

	root := machinetest.Data(t)
	out, err := run(t, "print", "--root", root)
	if err != nil {
		t.Fatalf("print: %v", err)
	}

	for name, want := range map[string]int{
		"zfs_up":                            1,
		"zfs_disk_info":                     62,
		"zfs_disk_slow_ios_total":           60,
		"zfs_disk_latency_seconds_count":    2 * 18,
		"zfs_vdev_latency_seconds_count":    11 * 4,
		"zfs_pool_latency_seconds_count":    11 * 3,
		"zfs_pool_operations_total":         2 * 3,
		"zfs_pool_active_requests":          6 * 3,
		"zfs_poll_failures_total":           2,
		"zfs_command_timeouts_total":        2,
		"zfs_slow_io_threshold_seconds":     1,
		"zfs_pool_request_size_bytes_count": 14 * 3,
		"zfs_pool_state":                    7 * 3,
		"zfs_pool_size_bytes":               3,
		"zfs_pool_scrub_state":              4 * 2,
		"zfs_pool_resilver_state":           0,
		"zfs_vdev_state":                    6*11 + 2*2,
		"zfs_vdev_fragmentation_ratio":      4,
		"zfs_disk_state":                    6*60 + 2*2,
		"zfs_disk_errors_total":             3 * 60,
	} {
		if got := count(out, name); got != want {
			t.Errorf("%d readings of %s, want %d", got, name, want)
		}
	}

	for _, want := range []string{"zfs_up 1", `zfs_poll_failures_total{poll="stats"} 0`, "zfs_slow_io_threshold_seconds 30", `zfs_disk_info{class="data",disk="sdf",enclosure="1:0:24:0",`} {
		if !strings.Contains(out, want) {
			t.Errorf("the page lacks %s", want)
		}
	}

	// once each, not on a schedule
	if got := machinetest.Asked(t, root, machinetest.Zpool); len(got) != 1 {
		t.Errorf("zpool was run %d times, want once", len(got))
	}
}

// Parts left out are off the page, and not asked for: with no latency or size on it zpool_influxdb is not asked for its histograms, and with nothing
// it says on it, not run at all.
//
//nolint:paralleltest // viper is global state; Make mutates it
func TestPrintLeavesOutWhatItIsTold(t *testing.T) {
	alone(t)

	root := machinetest.Data(t)
	out, err := run(t, "print", "--root", root, "--skip", "latency,request-sizes,poll")
	if err != nil {
		t.Fatalf("print: %v", err)
	}
	if count(out, "zfs_disk_latency_seconds_count") != 0 || count(out, "zfs_poll_failures_total") != 0 || count(out, "zfs_pool_operations_total") != 6 {
		t.Errorf("the page has what was left out, or lacks what was not:\n%s", out)
	}
	if got := machinetest.Asked(t, root, machinetest.Influx); len(got) != 1 || got[0] != "--no-histograms --sum-histogram-buckets" {
		t.Errorf("zpool_influxdb was asked %q, want it asked for no histograms", got)
	}

	alone(t)
	root = machinetest.Data(t)
	out, err = run(t, "print", "--root", root, "--metrics", "disks,slow-ios")
	if err != nil {
		t.Fatalf("print: %v", err)
	}
	if count(out, "zfs_disk_slow_ios_total") != 60 || count(out, "zfs_pool_operations_total") != 0 || !strings.Contains(out, "zfs_up 1") {
		t.Errorf("the page should hold the disks' slow requests and whether ZFS is up, and nothing of the pools:\n%s", out)
	}
	if got := machinetest.Asked(t, root, machinetest.Influx); len(got) != 0 {
		t.Errorf("zpool_influxdb was run with nothing it says on the page: %q", got)
	}

	alone(t)
	root = machinetest.Data(t)
	out, err = run(t, "print", "--root", root, "--metrics", "disks,request-sizes", "--disk-timings")
	if err != nil {
		t.Fatalf("print: %v", err)
	}
	if count(out, "zfs_disk_request_size_bytes_count") != 14*18 || count(out, "zfs_disk_latency_seconds_count") != 0 || count(out, "zfs_disk_operations_total") != 0 {
		t.Errorf("the page should hold every disk's request sizes and nothing else of them:\n%s", out)
	}
	if got := machinetest.Asked(t, root, machinetest.Influx); len(got) != 1 || got[0] != "--sum-histogram-buckets" {
		t.Errorf("zpool_influxdb was asked %q, want it asked for its histograms", got)
	}
}

// Every disk timing gives each disk all that zpool_influxdb counts of it: every wait, every request size, and its reads and writes.
//
//nolint:paralleltest // viper is global state; Make mutates it
func TestPrintEveryDiskTiming(t *testing.T) {
	alone(t)

	out, err := run(t, "print", "--root", machinetest.Data(t), "--disk-timings")
	if err != nil {
		t.Fatalf("print: %v", err)
	}

	for name, want := range map[string]int{
		"zfs_disk_latency_seconds_count":    11 * 18,
		"zfs_disk_request_size_bytes_count": 14 * 18,
		"zfs_disk_operations_total":         2 * 18,
		"zfs_disk_bytes_total":              2 * 18,
	} {
		if got := count(out, name); got != want {
			t.Errorf("%d readings of %s, want %d", got, name, want)
		}
	}
}

// print is also the check that ZFS can be asked, so a program that fails makes it fail, with the page still printed to show what that looks like.
//
//nolint:paralleltest // viper is global state; Make mutates it
func TestPrintFailsWhenZFSDoesNotAnswer(t *testing.T) {
	alone(t)

	root := machinetest.Data(t)
	machinetest.Fail(t, root, machinetest.Influx, "cannot open '/dev/zfs': Permission denied")

	out, err := run(t, "print", "--root", root)
	if err == nil || !strings.Contains(err.Error(), "stats") {
		t.Errorf("print with zpool_influxdb failing = %v, want an error naming its round", err)
	}
	for _, want := range []string{"zfs_up 0", `zfs_poll_failures_total{poll="stats"} 1`, `zfs_poll_failures_total{poll="status"} 0`, `zfs_disk_info{class="data",disk="sdf",`} {
		if !strings.Contains(out, want) {
			t.Errorf("the page lacks %s:\n%s", want, out)
		}
	}
}

// On a machine with no pools there is nothing to print, and print says so with the setting to check rather than succeed with an empty page.
//
//nolint:paralleltest // viper is global state; Make mutates it
func TestPrintFailsOnAMachineWithNoPools(t *testing.T) {
	alone(t)

	root := machinetest.Machine(t, "", `{"output_version": {"command": "zpool status"}, "pools": {}}`, "")
	out, err := run(t, "print", "--root", root)
	if err == nil || !strings.Contains(err.Error(), "no pools") || !strings.Contains(err.Error(), "ZFS_EXPORTER_ROOT") {
		t.Errorf("print with no pools = %v, want an error saying so and naming the setting", err)
	}
	if count(out, "zfs_disk_info") != 0 || !strings.Contains(out, "zfs_exporter_build_info") {
		t.Errorf("the page should hold the build and no pool:\n%s", out)
	}
}

// A / that is not there, or a machine without ZFS's programs, stops both commands at once, naming the setting: it is a mistake in how the program
// was started, not a machine with no pools.
//
//nolint:paralleltest // viper is global state; Make mutates it
func TestARootThatIsNotTheMachines(t *testing.T) {
	for _, command := range []string{"print", "serve"} {
		for root, says := range map[string]string{"/no/such/root": "/no/such/root", t.TempDir(): "/usr/sbin/zpool"} {
			alone(t)

			_, err := run(t, command, "--listen", "127.0.0.1:0", "--root", root)
			if err == nil || !strings.Contains(err.Error(), says) || !strings.Contains(err.Error(), "ZFS_EXPORTER_ROOT") {
				t.Errorf("%s --root %s = %v, want an error saying %s and naming the setting", command, root, err, says)
			}
		}
	}
}

// A setting that cannot be used stops both commands before anything is asked, naming the setting.
//
//nolint:paralleltest // viper is global state; Make mutates it
func TestSettingsThatCannotBeUsed(t *testing.T) {
	for _, command := range []string{"print", "serve"} {
		for _, args := range [][]string{{"--skip", "fans"}, {"--metrics", "histograms"}, {"--timeout", "2m"}, {"--intervals", "status=never"}, {"--labels", "pool=x"}} {
			alone(t)

			root := machinetest.Data(t)
			_, err := run(t, append([]string{command, "--listen", "127.0.0.1:0", "--root", root}, args...)...)
			env := envNames[strings.TrimPrefix(args[0], "--")]
			if err == nil || !strings.Contains(err.Error(), env) {
				t.Errorf("%s %s = %v, want an error naming %s", command, strings.Join(args, " "), err, env)
			}
			if got := machinetest.Asked(t, root, machinetest.Zpool); len(got) != 0 {
				t.Errorf("%s %s ran zpool before refusing", command, strings.Join(args, " "))
			}
		}
	}
}

// freePort is an address on this machine nothing else is listening on: taken, noted and given back.
func freePort(t *testing.T) string {
	t.Helper()

	ln, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()

	return ln.Addr().String()
}

// fetch reads a path from the program once it is serving, trying for a second while it starts.
func fetch(t *testing.T, addr, path string) string {
	t.Helper()

	for range 50 {
		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://"+addr+path, http.NoBody)
		if err != nil {
			t.Fatal(err)
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			time.Sleep(20 * time.Millisecond)
			continue
		}
		body, err := io.ReadAll(res.Body)
		_ = res.Body.Close()
		if err != nil {
			t.Fatal(err)
		}

		return string(body)
	}
	t.Fatalf("nothing answered at %s%s", addr, path)

	return ""
}

// waitFor fetches the page until it holds every one of these, or ten seconds have passed.
func waitFor(t *testing.T, addr string, wanted ...string) {
	t.Helper()

	var body string
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		body = fetch(t, addr, metrics.Path)

		all := true
		for _, want := range wanted {
			all = all && strings.Contains(body, want)
		}
		if all {
			return
		}
	}

	for _, want := range wanted {
		if !strings.Contains(body, want) {
			t.Errorf("the page lacks %s:\n%s", want, body)
		}
	}
}

// serve is the whole program: it runs ZFS's programs on its own schedule, serves what they said, gives up on one that hangs and says so, keeps
// serving what the other says, and stops cleanly when told to.
//
//nolint:paralleltest // viper is global state; Make mutates it
func TestServe(t *testing.T) {
	alone(t)

	root := machinetest.Data(t)
	addr := freePort(t)

	cmd, _ := program(t, "serve", "--listen", addr, "--root", root, "--intervals", "status=1s,stats=1s", "--timeout", "500ms")
	ctx, cancel := context.WithCancel(t.Context())
	stopped := make(chan error, 1)
	go func() { stopped <- cmd.ExecuteContext(ctx) }()

	if got := strings.TrimSpace(fetch(t, addr, metrics.HealthPath)); got != "ok" {
		t.Errorf("%s = %q, want ok", metrics.HealthPath, got)
	}

	// the first round is run as the program starts, so it is on the page within moments
	waitFor(t, addr, "zfs_up 1", `zfs_disk_info{class="data",disk="sdf",`, `zfs_disk_latency_seconds_count{class="data",disk="sdf",op="read",pool="data"`)

	// a program that hangs is given up on and counted, and the other goes on answering
	machinetest.Hang(t, root, machinetest.Influx)
	waitFor(t, addr, "zfs_up 0", `zfs_command_timeouts_total{command="zpool_influxdb"} 1`, `zfs_command_stuck{command="zpool_influxdb"} 0`, `zfs_poll_failures_total{poll="status"} 0`)

	machinetest.Mend(t, root, machinetest.Influx)
	waitFor(t, addr, "zfs_up 1")

	cancel()
	select {
	case err := <-stopped:
		if err != nil {
			t.Errorf("serve stopped with %v, want nil", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("serve did not stop when told to")
	}
}

// A port that is already taken stops the program with the address in the error, and the polling it had started stops with it.
//
//nolint:paralleltest // viper is global state; Make mutates it
func TestServeSaysWhenThePortIsTaken(t *testing.T) {
	alone(t)

	ln, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	addr := ln.Addr().String()

	cmd, _ := program(t, "serve", "--listen", addr, "--root", machinetest.Data(t))
	stopped := make(chan error, 1)
	go func() { stopped <- cmd.ExecuteContext(t.Context()) }()

	select {
	case err := <-stopped:
		if err == nil || !strings.Contains(err.Error(), addr) {
			t.Errorf("serve on a taken port = %v, want an error naming %s", err, addr)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("serve did not give up on a port that was taken")
	}
}
