// Package machine asks the machine's own ZFS programs about its pools, and finds each disk they name among the machine's own. The programs are the
// machine's, so they always match the ZFS it runs, after an update too. In a container they are run through the machine's own loader and libraries,
// since the container has none of its own that would do.
package machine

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/katbyte/go-kt/clog"
	"github.com/katbyte/zfs-exporter/lib/command"
	"github.com/katbyte/zfs-exporter/lib/host"
	"github.com/katbyte/zfs-exporter/lib/poll"
	"github.com/katbyte/zfs-exporter/lib/zpool"
)

// Where the programs are, by the paths OpenZFS installs them at and the ones Debian, and so TrueNAS, moves them to.
var (
	zpoolPlaces  = []string{"/usr/sbin/zpool", "/sbin/zpool", "/usr/local/sbin/zpool"}
	influxPlaces = []string{"/usr/libexec/zfs/zpool_influxdb", "/usr/lib/zfs-linux/zpool_influxdb", "/usr/local/libexec/zfs/zpool_influxdb"}
)

// arch is where a machine of one kind keeps its program loader, and the name of the directories its libraries are in.
type arch struct {
	loaders []string
	triplet string
}

// arches is each kind of machine the program is built for that the loader can be found on. Those under /usr come last, for a machine whose /usr
// alone is mounted: on Debian, and so TrueNAS, everything is under /usr and the rest are links to it.
var arches = map[string]arch{
	"amd64": {loaders: []string{"/lib64/ld-linux-x86-64.so.2", "/lib/x86_64-linux-gnu/ld-linux-x86-64.so.2", "/usr/lib64/ld-linux-x86-64.so.2", "/usr/lib/x86_64-linux-gnu/ld-linux-x86-64.so.2"}, triplet: "x86_64-linux-gnu"},
	"arm64": {loaders: []string{"/lib/ld-linux-aarch64.so.1", "/lib/aarch64-linux-gnu/ld-linux-aarch64.so.1", "/usr/lib/ld-linux-aarch64.so.1", "/usr/lib/aarch64-linux-gnu/ld-linux-aarch64.so.1"}, triplet: "aarch64-linux-gnu"},
}

// Machine asks one machine about its pools.
type Machine struct {
	host       host.Host
	runner     *command.Runner
	zpool      string
	influx     string
	histograms bool

	mu sync.Mutex
	// lost is the disks last found at no disk, by path, so one is logged when that changes and not every round
	lost map[string]bool
}

// Options says what is asked, and how.
type Options struct {
	// Limit is how long each program is given to answer.
	Limit time.Duration
	// Stats says zpool_influxdb is asked as well as zpool status, and Histograms that it is asked for its histograms too.
	Stats      bool
	Histograms bool
}

// New is a machine whose own / is the host's, its programs found now: one that is not there is said at the start, since nothing could be asked later.
func New(h host.Host, o Options) (*Machine, error) {
	m := &Machine{host: h, histograms: o.Histograms, lost: map[string]bool{}}

	var err error
	if m.zpool, err = h.Program("zpool", zpoolPlaces...); err != nil {
		return nil, err
	}
	if o.Stats {
		if m.influx, err = h.Program("zpool_influxdb", influxPlaces...); err != nil {
			return nil, err
		}
	}

	through, err := m.loader(runtime.GOARCH)
	if err != nil {
		return nil, err
	}
	m.runner = command.New(o.Limit, through...)

	return m, nil
}

// loader is what the programs are run through: nothing on the machine itself, and its own loader and libraries for a machine mounted here.
func (m *Machine) loader(goarch string) ([]string, error) {
	if m.host.Root() == "/" {
		return nil, nil
	}

	a, known := arches[goarch]
	if !known {
		return nil, fmt.Errorf("the machine's programs cannot be run from a %s build: its program loader is not known", goarch)
	}

	loader, err := m.host.Program("program loader", a.loaders...)
	if err != nil {
		return nil, err
	}

	var libs []string
	for _, dir := range []string{"/lib/" + a.triplet, "/usr/lib/" + a.triplet, "/lib", "/usr/lib"} {
		at, err := m.host.Path(dir)
		if err != nil || slices.Contains(libs, at) {
			continue
		}
		if info, err := os.Stat(at); err == nil && info.IsDir() {
			libs = append(libs, at)
		}
	}

	return []string{loader, "--library-path", strings.Join(libs, ":")}, nil
}

// Status is what zpool status says of every pool, with each disk found among the machine's own now.
func (m *Machine) Status(ctx context.Context) (poll.Status, error) {
	out, err := m.runner.Run(ctx, "zpool", m.zpool, zpool.StatusArgs...)
	if err != nil {
		return poll.Status{}, err
	}

	pools, err := zpool.Status(out)
	if err != nil {
		return poll.Status{}, err
	}

	for i := range pools {
		for j := range pools[i].Vdevs {
			m.find(pools[i].Name, &pools[i].Vdevs[j])
		}
	}

	return poll.Status{Pools: pools, SlowAfter: m.host.SlowAfter()}, nil
}

// find finds the disk at each disk's path in a vdev. A disk ZFS has a path for that leads nowhere has gone, or been moved, and is labelled by its
// GUID.
func (m *Machine) find(pool string, v *poll.Vdev) {
	for i := range v.Children {
		m.find(pool, &v.Children[i])
	}

	if !v.Leaf() || v.Path == "" {
		return
	}

	d, err := m.host.Disk(v.Path)
	if err == nil {
		v.Disk = &d
	}

	m.mu.Lock()
	before := m.lost[v.Path]
	m.lost[v.Path] = err != nil
	m.mu.Unlock()

	switch {
	case err != nil && !before:
		clog.Log.Warnf("pool %s: the disk %s cannot be found at %s, and is labelled by its GUID until it is: %v", pool, v.GUID, v.Path, err)
	case err == nil && before:
		clog.Log.Infof("pool %s: the disk %s is found again at %s, as %s", pool, v.GUID, v.Path, d.Name)
	}
}

// Stats is what zpool_influxdb counts of every pool.
func (m *Machine) Stats(ctx context.Context) (poll.Stats, error) {
	args := zpool.StatsArgs
	if !m.histograms {
		args = append([]string{zpool.NoHistograms}, args...)
	}

	out, err := m.runner.Run(ctx, "zpool_influxdb", m.influx, args...)
	if err != nil {
		return poll.Stats{}, err
	}

	return zpool.Stats(out)
}

// Commands is how each program has been going.
func (m *Machine) Commands() []poll.Command { return m.runner.Commands() }
