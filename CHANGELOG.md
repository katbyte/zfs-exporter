# Changelog

## 0.1.0 (2026-10-20)

The first release: how long a machine's ZFS pools, vdevs and disks take to answer, as a page Prometheus can read.

- **How long every request took, as histograms**: for each pool and top-level vdev, all of it and each part, at the disk and in each of ZFS's queues; for each disk, its own time at the disk, so its slowest 1% can be set beside the other disks in its vdev.
- **Requests slower than ZFS's limit, for each disk**, which neither TrueNAS nor `zpool_influxdb` reports.
- **Every disk timing, if asked for**: `--disk-timings` gives each disk all its vdev has, every wait, request sizes, reads and writes. It is off by default, since it takes the page to about four times the readings.
- **Health, errors and space**: the state of every pool, vdev and disk, the read, write and checksum errors counted of each, each pool's damaged data, how big each pool and vdev is and how much is used, and how broken up each vdev's free space is.
- **Each pool's last scrub or resilver**: whether it is running or paused, when it started and ended, how far along it is, how fast it goes and how long it has left, as `zpool status` works them out, and what it repaired and found.
- **How big each request was, how many reads and writes each pool and vdev has done, and how many requests each pool has queued.**
- **Every disk by its serial number**, with the labels truenas-exporter gives it and the enclosure and slot sas-exporter names, worked out every round so a swapped or renamed disk is right at once. A disk ZFS cannot find is labelled by its GUID.
- **Bucket bounds as ZFS counts them**: `zpool_influxdb` labels each latency bucket with its bottom, which reads every request as twice as fast as it was; each is labelled here with its top.
- **The machine's own ZFS programs**, run through its own loader when the program is in a container, so they always match the machine's ZFS.
- **Never stuck behind ZFS**: each command is run on its own schedule, one at a time, with a time limit. One that will not end even when killed holds back the rest until it does, and `zfs_up` and `zfs_command_stuck` say so. A scrape is answered from memory.
- **Leave out any part**, and it is not asked for either: `--skip` and `--metrics` take the levels `pools`, `vdevs` and `disks`, and the kinds `poll`, `latency`, `request-sizes`, `queues`, `operations`, `slow-ios`, `space`, `health`, `scrub` and `resilver`.
