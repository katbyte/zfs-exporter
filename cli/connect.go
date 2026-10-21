package cli

import (
	"fmt"
	"os"

	"github.com/katbyte/zfs-exporter/lib/host"
	"github.com/katbyte/zfs-exporter/lib/machine"
	"github.com/katbyte/zfs-exporter/lib/poll"
)

// connect makes what asks the machine about its pools: its own ZFS programs, found under the / it is told. A / that is not there, or a machine
// without the programs, is said now, since nothing could be asked later.
func connect(from source) (poll.ZFS, error) {
	info, err := os.Stat(from.root)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("%s is not a directory", from.root)
	}

	return machine.New(host.New(from.root), machine.Options{Limit: from.limit, Stats: from.stats, Histograms: from.histograms})
}
