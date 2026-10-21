// Package cli implements the zfs-exporter command line interface: the cobra commands, flag and config handling, and the server that serves the
// metrics page.
package cli

import (
	"fmt"
	"strings"

	"github.com/katbyte/go-kt/prometheus/exporter"
	"github.com/katbyte/go-kt/version"
	"github.com/spf13/cobra"
)

func Make() (*cobra.Command, error) {
	return makeWith(connect)
}

// makeWith is Make with what asks ZFS made by connect, so a test can stand in for one.
func makeWith(connect connector) (*cobra.Command, error) {
	root := &cobra.Command{
		Use:   "zfs-exporter [command]",
		Short: "zfs-exporter reports how long a machine's ZFS pools, vdevs and disks take to answer as a Prometheus metrics page",
		Long: `Reports what ZFS counts of the pools on the machine it runs on as a Prometheus metrics page:
how long each request to each pool, vdev and disk took, how big each was, how many each has
had, how many are queued, and how many each disk took too long to answer. Every disk is
labelled by its serial number, so it joins with what other exporters say of it. It runs the
machine's own zpool and zpool_influxdb, and only ever reads.
Complete documentation is available at https://github.com/katbyte/zfs-exporter`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			_, _ = fmt.Fprintln(cmd.OutOrStdout(), "Run \"zfs-exporter help\" for more information about available zfs-exporter commands.")
			return nil
		},
	}

	root.AddCommand(&cobra.Command{
		Use:           "version",
		Short:         "Print the version number of zfs-exporter",
		Long:          `Print the version number of zfs-exporter`,
		Args:          cobra.NoArgs,
		SilenceErrors: true,
		Run: func(cmd *cobra.Command, _ []string) {
			_, _ = fmt.Fprintln(cmd.OutOrStdout(), versionString())
		},
	})

	root.AddCommand(&cobra.Command{
		Use:   "print",
		Short: "Ask ZFS once and print the metrics page",
		Long: `Runs zpool status and zpool_influxdb once and prints the page a scrape would read, without
serving anything: a check that the pools can be seen from where the program runs, and a look at
what would be reported.`,
		Args:          cobra.NoArgs,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cmd.SilenceUsage = true

			f := GetFlags()
			poller, err := f.poller(connect)
			if err != nil {
				return err
			}

			p, err := f.page(poller)
			if err != nil {
				return err
			}

			// the page is printed either way, with what a program that did not answer looks like on it
			if err := exporter.Print(cmd.Context(), cmd.OutOrStdout(), poller.Inner(), p); err != nil {
				return err
			}
			if s := poller.State(); s.Status.Known() && len(s.Status.Value.Pools) == 0 {
				return fmt.Errorf("ZFS lists no pools: this has to run on the machine the pools are on, with --root (%s) naming its / when that is mounted somewhere else", envNames["root"])
			}

			return nil
		},
	})

	root.AddCommand(serveCmd(connect))

	if err := configureFlags(root); err != nil {
		return nil, fmt.Errorf("unable to configure flags: %w", err)
	}
	// the settings file is read once the flags are parsed, since a flag can name it; before any command's own checks, which read what it holds
	root.PersistentPreRunE = func(*cobra.Command, []string) error { return loadConfig() }

	return root, nil
}

// versionString is the name, version and commit the binary was built from, as the version command prints it and serve starts by saying.
// A build past a tag already names its commit in the version, so the commit is only added when it is not there.
func versionString() string {
	s := "zfs-exporter " + version.Version
	if version.GitCommit != "" && !strings.Contains(version.Version, version.GitCommit) {
		s += " (" + version.GitCommit + ")"
	}

	return s
}
