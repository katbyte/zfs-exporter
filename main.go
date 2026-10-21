// Package main implements zfs-exporter, which reports how long a machine's ZFS pools, vdevs and disks take to answer as a Prometheus metrics page.
package main

import (
	"os"

	"github.com/gookit/color"
	"github.com/katbyte/go-kt/clog"
	"github.com/katbyte/zfs-exporter/cli"
)

func main() {
	// the log level comes from ZFS_EXPORTER_LOG; read it once here, before anything logs
	clog.SetLevelFromEnv("ZFS_EXPORTER_LOG")

	cmd, err := cli.Make()
	if err != nil {
		clog.Log.Error(color.Sprintf("<red>zfs-exporter: building cmd</> %v", err))

		os.Exit(1)
	}

	if err := cmd.Execute(); err != nil {
		clog.Log.Error(color.Sprintf("<red>zfs-exporter:</> %v", err))

		os.Exit(1)
	}

	os.Exit(0)
}
