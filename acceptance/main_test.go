//go:build acceptance

// Package acceptance_test runs the image a deployment runs, as docker runs it, and checks the page it serves. The unit tests run the program in
// the test's own process; these are where the built image gets to run as a container does: as its own user and not root, with the machine's /
// at /host, its programs run through the machine's loader, asked over a published port.
//
// The machine is the one the unit tests use: data's disks as they were saved, with scripts in place of its loader, zpool and zpool_influxdb that
// print what those printed on data. Neither needs ZFS, and neither can stand in for it: whether the machine's own programs run from inside a
// container, and what ZFS says when a pool is stuck, is only ever seen on a machine with pools.
//
//	make testacc                                                 # builds the image and runs these against it
//	ZFS_EXPORTER_TEST_PROGRAM_IMAGE=zfs-exporter make test-live  # or against one already built
package acceptance_test
