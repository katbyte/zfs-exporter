package cli

import (
	"fmt"
	"os/signal"
	"syscall"

	"github.com/katbyte/go-kt/prometheus/exporter"
	"github.com/spf13/cobra"
)

func serveCmd(connect connector) *cobra.Command {
	return &cobra.Command{
		Use:   "serve",
		Short: "Serve the metrics page",
		Long: `Asks ZFS about the machine's pools on a schedule and serves the last answers at /metrics in
the Prometheus text format, with a health probe at /healthz.

A scrape is answered from memory and never waits on ZFS. zpool status is run every minute, for
each disk's slow requests and where it is, and zpool_influxdb every minute, for how many requests
each pool, vdev and disk has had, how big they were and how long they took. --intervals changes
how often: status=2m,stats=30s.

Each is run on its own, given 30s to answer (--timeout), and stopped if it has not. One that will
not stop even then is ZFS stuck on a pool: nothing more is run until it ends, and zfs_up and
zfs_command_stuck say so. The programs are the machine's own, so they always match its ZFS.

Each disk is labelled as truenas-exporter labels it, by pool, class, vdev, the kernel's name for
it and its serial number, worked out each round so a disk that was swapped or renamed is right
at once. zfs_disk_info adds its model, world wide name, and the enclosure and slot it sits in as
sas-exporter names them.

Everything is reported unless --skip leaves it out: the levels pools, vdevs and disks, and the
kinds poll, latency, request-sizes, queues, operations and slow-ios. A part left out is not asked
for either where that saves a question. --metrics names the only parts to ask about instead:
--metrics disks,latency is how long each disk takes and nothing else.

--root says where the machine's own / is, /host for a container given it there; its programs
are then run through its own loader and libraries. --labels puts labels of your choosing on every
reading, hostname=nas say, for when whatever scrapes the page does not say whose it is.

Every setting is also an environment variable: ZFS_EXPORTER_ROOT, ZFS_EXPORTER_LISTEN,
ZFS_EXPORTER_LABELS, ZFS_EXPORTER_METRICS, ZFS_EXPORTER_SKIP, ZFS_EXPORTER_INTERVALS and
ZFS_EXPORTER_TIMEOUT.`,
		Args:          cobra.NoArgs,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cmd.SilenceUsage = true

			// said before anything else, so a log shows which build it came from
			_, _ = fmt.Fprintln(cmd.ErrOrStderr(), versionString())

			f := GetFlags()
			poller, err := f.poller(connect)
			if err != nil {
				return err
			}

			p, err := f.page(poller)
			if err != nil {
				return err
			}

			// one signal stops both the polling and the serving
			ctx, stop := signal.NotifyContext(cmd.Context(), syscall.SIGINT, syscall.SIGTERM)
			defer stop()

			return exporter.Serve(ctx, f.Listen, poller.Inner(), p)
		},
	}
}
