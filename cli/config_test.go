package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/katbyte/zfs-exporter/lib/poll"
	"github.com/katbyte/zfs-exporter/lib/skip"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"github.com/spf13/viper"
)

// viper is global, so none of these can run in parallel and each resets it.
const testUse = "zfs-exporter"

// alone cuts a test off from the settings of whoever runs it: viper as it starts, an empty home and working directory, and none of the program's
// variables set.
func alone(t *testing.T) (home, wd string) {
	t.Helper()

	viper.Reset()
	t.Cleanup(viper.Reset)

	home, wd = t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	t.Chdir(wd)
	t.Setenv(configEnv, "")
	for _, env := range envNames {
		t.Setenv(env, "")
	}

	return home, wd
}

// load drives the real flag wiring with these command-line arguments, after whatever the test put in the environment and the settings files.
func load(t *testing.T, args ...string) (*FlagData, error) {
	t.Helper()

	root := &cobra.Command{Use: testUse}
	if err := configureFlags(root); err != nil {
		t.Fatalf("configureFlags: %v", err)
	}
	if err := root.ParseFlags(args); err != nil {
		t.Fatalf("ParseFlags: %v", err)
	}
	if err := loadConfig(); err != nil {
		return nil, err
	}

	return GetFlags(), nil
}

// must is load for a test that expects the settings to be read.
func must(t *testing.T, args ...string) *FlagData {
	t.Helper()

	f, err := load(t, args...)
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}

	return f
}

func write(t *testing.T, dir, name, body string) string {
	t.Helper()

	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	return path
}

// With nothing set the page is served where the README says, the machine is the one the program runs on, and its programs are run as often and
// given as long as the README says: these are what a dashboard's scrape interval and an example compose file are written against.
//
//nolint:paralleltest // viper is global state; these mutate it
func TestDefaults(t *testing.T) {
	alone(t)

	f := must(t)
	if want := (FlagData{Root: "/", Listen: ":9087"}); *f != want {
		t.Errorf("defaults = %+v, want %+v", *f, want)
	}

	every, err := f.every()
	if err != nil || every != (poll.Intervals{Status: time.Minute, Stats: time.Minute}) {
		t.Errorf("the intervals are %+v (%v), want a minute each", every, err)
	}
	if limit, err := f.limit(every, true); err != nil || limit != 30*time.Second {
		t.Errorf("a program is given %s (%v), want 30s", limit, err)
	}
}

// Every setting has an environment variable, which is how a container is configured.
func TestEveryFlagHasAnEnvironmentVariable(t *testing.T) {
	alone(t)

	t.Setenv("ZFS_EXPORTER_ROOT", "/host")
	t.Setenv("ZFS_EXPORTER_LISTEN", "127.0.0.1:9000")
	t.Setenv("ZFS_EXPORTER_LABELS", "hostname=nas")
	t.Setenv("ZFS_EXPORTER_METRICS", "disks")
	t.Setenv("ZFS_EXPORTER_SKIP", "request-sizes")
	t.Setenv("ZFS_EXPORTER_DISK_TIMINGS", "true")
	t.Setenv("ZFS_EXPORTER_INTERVALS", "status=2m,stats=30s")
	t.Setenv("ZFS_EXPORTER_TIMEOUT", "20s")

	want := FlagData{Root: "/host", Listen: "127.0.0.1:9000", Labels: "hostname=nas", Metrics: "disks", Skip: "request-sizes", DiskTimings: true, Intervals: "status=2m,stats=30s", Timeout: "20s"}
	if got := *must(t); got != want {
		t.Errorf("from the environment = %+v, want %+v", got, want)
	}

	root := &cobra.Command{Use: testUse}
	if err := configureFlags(root); err != nil {
		t.Fatal(err)
	}
	root.PersistentFlags().VisitAll(func(fl *pflag.Flag) {
		if _, ok := envNames[fl.Name]; !ok && fl.Name != "config" {
			t.Errorf("--%s has no environment variable", fl.Name)
		}
	})
}

// A flag beats the environment, which beats a settings file: a container overrides what an image holds, and a command line overrides both.
func TestFlagThenEnvironmentThenFile(t *testing.T) {
	home, _ := alone(t)
	write(t, home, ".zfs-exporter", "ROOT=/from-file\nLABELS=from=file\nTIMEOUT=10s\n")

	f := must(t)
	if f.Root != "/from-file" || f.Labels != "from=file" || f.Timeout != "10s" {
		t.Errorf("from the file = %+v, want every setting it holds", f)
	}

	viper.Reset()
	t.Setenv("ZFS_EXPORTER_ROOT", "/from-env")
	f = must(t)
	if f.Root != "/from-env" || f.Labels != "from=file" {
		t.Errorf("with the environment set = %+v, want the environment's root and the file's labels", f)
	}

	viper.Reset()
	f = must(t, "--root", "/from-flag")
	if f.Root != "/from-flag" {
		t.Errorf("with a flag = %+v, want the flag's root", f)
	}
}

// The settings file named by --config is the one read, and one that cannot be read is an error that says which; one merely absent from the usual
// places is not.
func TestTheSettingsFileNamed(t *testing.T) {
	home, wd := alone(t)
	write(t, home, ".zfs-exporter", "ROOT=/from-home\n")

	if f := must(t, "--config", write(t, wd, "zfs.env", "ROOT=/from-named\n")); f.Root != "/from-named" {
		t.Errorf("with --config = %+v, want the named file's root", f)
	}

	viper.Reset()
	t.Setenv(configEnv, "~/zfs.env")
	write(t, home, "zfs.env", "ROOT=/from-env-named\n")
	if f := must(t); f.Root != "/from-env-named" {
		t.Errorf("with %s = %+v, want the file it names, from home", configEnv, f)
	}

	viper.Reset()
	if _, err := load(t, "--config", filepath.Join(wd, "missing.env")); err == nil || !strings.Contains(err.Error(), "missing.env") {
		t.Errorf("a named file that is not there gave %v", err)
	}
}

// zpool_influxdb is asked only when something it says is on the page, and for its histograms only when a latency or a size is: what is left out is
// not asked for.
//
//nolint:paralleltest // viper is global state; these mutate it
func TestWhatIsAsked(t *testing.T) {
	for _, tc := range []struct {
		skip, metrics     string
		diskTimings       bool
		stats, histograms bool
	}{
		{"", "", false, true, true},
		{"latency,request-sizes", "", false, true, false},
		{"", "slow-ios", false, false, false},
		{"", "health,scrub,resilver", false, false, false},
		{"", "space", false, true, false},
		{"", "pools,space", false, false, false},
		{"", "disks", false, true, true},
		{"latency", "disks", false, false, false},
		{"latency", "disks", true, true, true},
		{"", "disks,operations", false, false, false},
		{"", "disks,operations", true, true, false},
		{"pools,vdevs", "", false, true, true},
		{"", "pools,queues", false, true, false},
	} {
		alone(t)

		f := must(t, "--skip", tc.skip, "--metrics", tc.metrics)
		parts, err := f.skipped()
		if err != nil {
			t.Fatal(err)
		}

		if stats, histograms := asks(parts, tc.diskTimings); stats != tc.stats || histograms != tc.histograms {
			t.Errorf("--skip %q --metrics %q with every disk timing %t asks for the stats %t and histograms %t, want %t and %t", tc.skip, tc.metrics, tc.diskTimings, stats, histograms, tc.stats, tc.histograms)
		}
	}

	if !(skip.Set{}).Shows(skip.Disks, skip.Latency) {
		t.Error("nothing left out leaves something out")
	}
}

// How long a program is given is read as a length of time, and can be no longer than a round of it lasts.
//
//nolint:paralleltest // viper is global state; these mutate it
func TestTimeout(t *testing.T) {
	for _, tc := range []struct {
		timeout, intervals string
		want               time.Duration
		wrong              string
	}{
		{"45s", "", 45 * time.Second, ""},
		{"", "stats=20s", 0, "every 20s"},
		{"2m", "", 0, "every 1m0s"},
		{"soon", "", 0, `"soon"`},
		{"-1s", "", 0, `"-1s"`},
	} {
		alone(t)

		f := must(t, "--timeout", tc.timeout, "--intervals", tc.intervals)
		every, err := f.every()
		if err != nil {
			t.Fatal(err)
		}

		got, err := f.limit(every, true)
		switch {
		case tc.wrong == "" && (err != nil || got != tc.want):
			t.Errorf("--timeout %q --intervals %q gives %s (%v), want %s", tc.timeout, tc.intervals, got, err, tc.want)
		case tc.wrong != "" && (err == nil || !strings.Contains(err.Error(), tc.wrong) || !strings.Contains(err.Error(), "ZFS_EXPORTER_TIMEOUT")):
			t.Errorf("--timeout %q --intervals %q gives %v, want an error saying %s and naming the setting", tc.timeout, tc.intervals, err, tc.wrong)
		}
	}

	// with zpool_influxdb not asked, only zpool status's round counts
	alone(t)
	f := must(t, "--timeout", "50s", "--intervals", "stats=20s")
	every, err := f.every()
	if err != nil {
		t.Fatal(err)
	}
	if got, err := f.limit(every, false); err != nil || got != 50*time.Second {
		t.Errorf("with no stats asked for, 50s gives %s (%v)", got, err)
	}
}
