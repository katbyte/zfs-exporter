# Changelog

## 0.1.1 (2026-10-20)

- **A virtual disk with no serial number now has one**: the name the virtual machine gives it, such as `drive-scsi0`, as `lsblk` shows it. Before, its serial was empty, so it could not be matched with what other exporters say of it.

## 0.1.0 (2026-10-20)

The first release: how long a machine's ZFS pools, vdevs and disks take to answer, and how healthy and full they are, as a page Prometheus can read.

- **How long every request took**: for each pool and vdev, split into time at the disk and time waiting in ZFS's queues; for each disk, its own time at the disk, to pick out the slow one in a vdev.
- **Slow requests for each disk**, which neither TrueNAS nor `zpool_influxdb` reports.
- **Every timing for each disk too, if asked for** with `--disk-timings`. It is off by default, since it makes the page about four times bigger.
- **Health and space**: the state and error counts of every pool, vdev and disk, damaged files, size and space used, and how broken up each vdev's free space is.
- **Scrubs and resilvers**: whether one is running, how far along it is, how long it has left, and what it found and repaired.
- **Request sizes and read and write counts** for each pool and vdev, and the requests each pool has waiting.
- **Every disk by its serial number**, with the labels truenas-exporter uses and the enclosure and slot sas-exporter uses, so all three join. It is checked every round, so a swapped disk is right at once.
- **Timing buckets labelled by their top**: `zpool_influxdb` labels each by its bottom, which makes every request look twice as fast as it was.
- **The machine's own ZFS programs**, even from a container, so they always match its ZFS.
- **Never stuck behind ZFS**: each command runs alone with a time limit, and the page is served from the last good answer. `zfs_up` and `zfs_command_stuck` say when ZFS is not answering.
- **Leave out any part**: `--skip` and `--metrics` pick which levels and kinds of reading to collect, and anything left out is not asked of ZFS at all.
