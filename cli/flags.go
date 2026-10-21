package cli

import (
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/katbyte/go-kt/clog"
	"github.com/katbyte/go-kt/prometheus/metrics"
	"github.com/katbyte/go-kt/prometheus/poller"
	"github.com/katbyte/zfs-exporter/lib/poll"
	"github.com/katbyte/zfs-exporter/lib/readings"
	"github.com/katbyte/zfs-exporter/lib/skip"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"github.com/spf13/viper"
)

// The defaults. Both programs are run every minute, which is often enough to see a disk slow down and costs a pool a second of reading its own
// state. Each is given half of that before it is given up on: long enough for a pool busy with a scrub or a resilver, short enough to still say so
// the same minute.
const (
	defaultRoot          = "/"
	defaultListen        = ":9087"
	defaultStatusEvery   = time.Minute
	defaultStatsEvery    = time.Minute
	defaultCommandsLimit = 30 * time.Second
)

type FlagData struct {
	Root        string `mapstructure:"root"`
	Listen      string `mapstructure:"listen"`
	Labels      string `mapstructure:"labels"`
	Metrics     string `mapstructure:"metrics"`
	Skip        string `mapstructure:"skip"`
	DiskTimings bool   `mapstructure:"disk-timings"`
	Intervals   string `mapstructure:"intervals"`
	Timeout     string `mapstructure:"timeout"`
}

// envNames binds each flag to its environment variable.
var envNames = map[string]string{
	"root":         "ZFS_EXPORTER_ROOT",
	"listen":       "ZFS_EXPORTER_LISTEN",
	"labels":       "ZFS_EXPORTER_LABELS",
	"metrics":      "ZFS_EXPORTER_METRICS",
	"skip":         "ZFS_EXPORTER_SKIP",
	"disk-timings": "ZFS_EXPORTER_DISK_TIMINGS",
	"intervals":    "ZFS_EXPORTER_INTERVALS",
	"timeout":      "ZFS_EXPORTER_TIMEOUT",
}

// persistent is the flag set configureFlags bound, for the one setting that has to be read before viper is: where the settings file is.
var persistent *pflag.FlagSet

func configureFlags(root *cobra.Command) error {
	pflags := root.PersistentFlags()

	pflags.String("root", defaultRoot, "where the machine's own / is: /host say, for a container given the machine's there, whose ZFS programs are then run through the machine's own loader")
	pflags.String("listen", defaultListen, "serve the metrics page on this address")
	pflags.String("labels", "", "labels to put on every reading, as name=value,name=value: hostname=nas say, when whatever scrapes the page does not add its own")
	pflags.String("metrics", "", "the only parts to ask about, with a comma between, named as for --skip: naming a level leaves out the other levels, naming a kind the other kinds")
	pflags.String("skip", "", "parts to leave off the page, and not ask for where that saves a question, with a comma between: the levels pools, vdevs and disks, and the kinds poll (how the asking goes), latency, request-sizes, queues, operations, slow-ios, space, health, scrub and resilver")
	pflags.Bool("disk-timings", false, "give each disk every timing, request size and count its vdev has, and not just how long it took itself: about four times the readings")
	pflags.String("intervals", "", "how often each program is run, as name=interval,name=interval: status (zpool status, every 1m) and stats (zpool_influxdb, every 1m)")
	pflags.String("timeout", "", "how long each ZFS program is given to answer before it is stopped and the round counted as failed: 30s unless set, and no longer than the shortest interval")
	pflags.String("config", "", "read settings from this file and no other, in place of ./.zfs-exporter or ~/.zfs-exporter (or export ZFS_EXPORTER_CONFIG)")

	persistent = pflags

	for name, env := range envNames {
		if err := viper.BindPFlag(name, pflags.Lookup(name)); err != nil {
			return fmt.Errorf("error binding '%s' flag: %w", name, err)
		}

		if err := viper.BindEnv(name, env); err != nil {
			return fmt.Errorf("error binding '%s' to env '%s' : %w", name, env, err)
		}
	}

	return nil
}

// configEnv names the settings file from the environment, as --config does on the command line.
const configEnv = "ZFS_EXPORTER_CONFIG"

// configPath is the settings file a run was told to read, "" for none: the flag's, then the environment's. A leading ~/ is the home directory, for
// whatever starts the program without a shell to expand it.
func configPath() string {
	path := os.Getenv(configEnv)
	if persistent != nil {
		if fl := persistent.Lookup("config"); fl != nil && fl.Changed {
			path = fl.Value.String()
		}
	}

	if rest, ok := strings.CutPrefix(path, "~/"); ok {
		if home, err := os.UserHomeDir(); err == nil {
			path = filepath.Join(home, rest)
		}
	}

	return path
}

// loadConfig reads the settings file, once the flags are parsed: the one --config or ZFS_EXPORTER_CONFIG names and no other, or else the first
// .zfs-exporter found in the working directory and then in $HOME. A file that was named and cannot be read is an error; one merely absent from the
// usual places is not.
func loadConfig() error {
	viper.SetConfigType("env")
	if path := configPath(); path != "" {
		viper.SetConfigFile(path)
		if err := viper.ReadInConfig(); err != nil {
			return fmt.Errorf("reading the settings file %s (--config / %s): %w", path, configEnv, err)
		}
	} else {
		viper.SetConfigName(".zfs-exporter")
		// viper reads the first file it finds, so the working directory comes first
		viper.AddConfigPath(".")
		if home, err := os.UserHomeDir(); err == nil {
			viper.AddConfigPath(home)
		}

		if err := viper.ReadInConfig(); err != nil {
			if _, ok := errors.AsType[viper.ConfigFileNotFoundError](err); !ok {
				clog.Log.Errorf("Error reading config file: %v", err)
			}
		}
	}

	return nil
}

// GetFlags returns the fully populated FlagData. It is read from viper rather than from the flags themselves, since viper is what lays the
// environment and the settings file under the command line.
func GetFlags() *FlagData {
	var f FlagData
	if err := viper.Unmarshal(&f); err != nil {
		clog.Log.Fatalf("failed to unmarshal configuration: %v", err)
	}

	return &f
}

// source is the machine the pools are on, and what is asked of it.
type source struct {
	// root is where the machine's own / is.
	root string
	// limit is how long each program is given.
	limit time.Duration
	// stats says zpool_influxdb is asked at all, and histograms that it is asked for its histograms.
	stats      bool
	histograms bool
}

// skipped is the parts the settings leave out: those --metrics does not name, and those --skip does.
func (f *FlagData) skipped() (skip.Set, error) {
	parts, err := skip.AllBut(f.Metrics)
	if err != nil {
		return nil, fmt.Errorf("--metrics (%s): %w", envNames["metrics"], err)
	}

	named, err := skip.Parse(f.Skip)
	if err != nil {
		return nil, fmt.Errorf("--skip (%s): %w", envNames["skip"], err)
	}

	maps.Copy(parts, named)

	return parts, nil
}

// asks is what zpool_influxdb is asked for, given the parts left out and whether every disk timing is wanted: nothing when nothing it says is on
// the page, and its histograms only when a latency or a size is.
func asks(parts skip.Set, diskTimings bool) (stats, histograms bool) {
	histograms = parts.Shows(skip.Pools, skip.Latency) || parts.Shows(skip.Vdevs, skip.Latency) || parts.Shows(skip.Disks, skip.Latency) || parts.Shows(skip.Pools, skip.RequestSizes) || parts.Shows(skip.Vdevs, skip.RequestSizes) || (diskTimings && parts.Shows(skip.Disks, skip.RequestSizes))
	stats = histograms || parts.Shows(skip.Pools, skip.Queues) || parts.Shows(skip.Pools, skip.Operations) || parts.Shows(skip.Vdevs, skip.Operations) || parts.Shows(skip.Vdevs, skip.Space) || (diskTimings && parts.Shows(skip.Disks, skip.Operations))

	return stats, histograms
}

// every is how often each program is run: the defaults, with what --intervals says in place of any of them.
func (f *FlagData) every() (poll.Intervals, error) {
	every, err := poller.ParseIntervals(f.Intervals, map[string]time.Duration{string(poll.RoundStatus): defaultStatusEvery, string(poll.RoundStats): defaultStatsEvery})
	if err != nil {
		return poll.Intervals{}, fmt.Errorf("--intervals (%s): %w", envNames["intervals"], err)
	}

	return poll.Intervals{Status: every[string(poll.RoundStatus)], Stats: every[string(poll.RoundStats)]}, nil
}

// limit is how long each program is given: what --timeout says, or the default, and never longer than a round of either program has.
func (f *FlagData) limit(every poll.Intervals, stats bool) (time.Duration, error) {
	limit := defaultCommandsLimit
	if f.Timeout != "" {
		d, err := time.ParseDuration(strings.TrimSpace(f.Timeout))
		if err != nil || d <= 0 {
			return 0, fmt.Errorf("--timeout (%s): how long a program is given is written as 30s or 1m, and not %q", envNames["timeout"], f.Timeout)
		}
		limit = d
	}

	shortest := every.Status
	if stats {
		shortest = min(shortest, every.Stats)
	}
	if limit > shortest {
		return 0, fmt.Errorf("--timeout (%s): a program cannot be given %s when a round of it comes every %s: shorten --timeout or lengthen --intervals", envNames["timeout"], limit, shortest)
	}

	return limit, nil
}

// connector makes what asks the machine about its pools.
type connector func(source) (poll.ZFS, error)

// poller is the poller of this machine's pools, not yet started.
func (f *FlagData) poller(connect connector) (*poll.Poller, error) {
	parts, err := f.skipped()
	if err != nil {
		return nil, err
	}

	every, err := f.every()
	if err != nil {
		return nil, err
	}

	stats, histograms := asks(parts, f.DiskTimings)
	limit, err := f.limit(every, stats)
	if err != nil {
		return nil, err
	}

	zfs, err := connect(source{root: f.Root, limit: limit, stats: stats, histograms: histograms})
	if err != nil {
		return nil, fmt.Errorf("--root (%s): %w", envNames["root"], err)
	}

	p, err := poll.New(zfs, every, stats)
	if err != nil {
		return nil, err
	}

	if stats {
		clog.Log.Infof("asking ZFS about the pools under %s: zpool status every %s and zpool_influxdb every %s, each given %s", f.Root, every.Status, every.Stats, limit)
	} else {
		clog.Log.Infof("asking ZFS about the pools under %s: zpool status every %s, given %s", f.Root, every.Status, limit)
	}
	if len(parts) > 0 {
		clog.Log.Infof("leaving out %s", parts)
	}
	if f.DiskTimings {
		clog.Log.Infof("giving each disk every timing")
	}

	return p, nil
}

// name is what the program is called, on the page's front and in its help.
const name = "zfs-exporter"

// page is the metrics page of the poller's readings, with the labels the settings ask for on every one. A label that cannot be used is said here,
// before ZFS is asked anything: one written wrongly, or one named as a reading's own label is, pool say.
func (f *FlagData) page(asking *poll.Poller) (*metrics.Page, error) {
	labels, err := metrics.ParseLabels(f.Labels)
	if err != nil {
		return nil, fmt.Errorf("--labels (%s): %w", envNames["labels"], err)
	}

	parts, err := f.skipped()
	if err != nil {
		return nil, err
	}

	r := readings.New(asking).Skipping(parts)
	if f.DiskTimings {
		r.EveryDiskTiming()
	}

	collectors := []prometheus.Collector{r}
	if !parts.Has(skip.Poll) {
		collectors = append(collectors, asking.Inner())
	}

	p, err := metrics.New(metrics.Options{Name: name, Labels: labels}, collectors...)
	if err != nil {
		return nil, fmt.Errorf("--labels (%s): %w", envNames["labels"], err)
	}

	return p, nil
}
