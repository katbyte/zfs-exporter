// Package zpooltest is what ZFS's own programs printed on the real machine data, for tests: three pools, data with seven raidz2 vdevs of eight disks,
// two cache disks and two spares. Only partition UUIDs and ZFS's own GUIDs name a disk in either, so neither needed anything made up.
package zpooltest

import (
	_ "embed"
)

// Status is what zpool status -s -p -j --json-int printed.
//
//go:embed testdata/status.json
var Status []byte

// Stats is what zpool_influxdb --sum-histogram-buckets printed, cut down for data to the pool as a whole, its first two vdevs and their sixteen
// disks: the rest is more of the same.
//
//go:embed testdata/stats.txt
var Stats []byte
