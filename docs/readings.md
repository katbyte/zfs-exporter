# Readings

Every reading starts `zfs_`. The page holds nothing else: none of the Go runtime's or the process's own readings.

Each count is ZFS's own, kept since the pool was imported. It starts again from nothing when the machine restarts or the pool is exported and imported, which a rate reads as a restart.

## Pools

Labels: `pool`.

| Reading | Labels | What it says |
|---|---|---|
| `zfs_pool_latency_seconds` | `wait`, `op` | histogram of how long each request took |
| `zfs_pool_request_size_bytes` | `queue`, `op`, `aggregated` | histogram of how big each request was |
| `zfs_pool_operations_total` | `op` | reads and writes done |
| `zfs_pool_bytes_total` | `op` | bytes read and written |
| `zfs_pool_active_requests` | `queue`, `op` | requests with the pool's disks when it was last asked |
| `zfs_pool_pending_requests` | `queue`, `op` | requests waiting their turn when it was last asked |
| `zfs_pool_state` | `state` | 1 for the pool's health, 0 for each other state it could be in |
| `zfs_pool_errors_total` | `error` | requests to the pool as a whole that went wrong |
| `zfs_pool_data_errors` | | places in the pool holding data ZFS could not repair |
| `zfs_pool_size_bytes` | | how big it is, raidz parity counted, as `zpool list` shows it |
| `zfs_pool_allocated_bytes` | | how much of it is used, raidz parity counted |
| `zfs_pool_usable_bytes` | | how big it is with raidz parity taken out: about what its files can fill |

- `op` is `read` or `write`.
- `queue` is `sync`, `async`, `scrub`, `trim` or `rebuild`: the queue ZFS put the request in. Scrubs are reads, and trims and rebuilds are writes.
- Requests queued are a glimpse once a minute, not an average: they show a pool that stays busy, not a burst.
- `state` is ZFS's own word: `ONLINE`, `DEGRADED`, `FAULTED`, `OFFLINE`, `REMOVED`, `UNAVAIL`, or `SUSPENDED` for a pool that has stopped reading and writing until its disks come back. Each is a reading of its own, so a state the pool leaves is seen going to 0.
- `error` is `read`, `write` or `checksum`, as `zpool status` counts them on the pool's own line. A checksum error is a block that read back other than it was written. The counts start again at `zpool clear`.
- `zfs_pool_data_errors` is the count `zpool status` ends with: `errors: 2 data errors`. Anything but 0 is damaged files, which `zpool status -v` names.
- What is free is `zfs_pool_size_bytes - zfs_pool_allocated_bytes`, as `zpool list` shows it.

### How long

`wait` is which part of a request's time:

| `wait` | From | To |
|---|---|---|
| `total` | being asked for | being answered: all of it |
| `disk` | being sent to the disk | the disk's answer |
| `sync_queue`, `async_queue`, `scrub_queue`, `trim_queue`, `rebuild_queue` | being queued | being sent to the disk |

These are `zpool iostat -l`'s columns. `total` is a queue's wait and the disk's together, so summing over `wait` counts a request twice.

### How the histograms are counted

- ZFS counts in powers of two: from 1024ns to 2047ns, from 2048ns to 4095ns, and on up to 68 seconds and more.
- Each bucket's `le` is the most it holds: `2.047e-06` for the one from 1024ns.
- `zpool_influxdb` labels each bucket with its bottom, which would make every request look twice as fast as it was. This program labels each with its top.
- The smallest bucket also holds everything under a microsecond, as `zpool_influxdb` gives it.
- ZFS keeps no total time, so `_sum` is worked out from the buckets. Each request is counted at the middle of its bucket, as `zpool iostat -l` works out its averages. An average from it is good to within a third either way; the buckets themselves are exact.
- Sizes are counted the same way: 8191 bytes is the top of the bucket from 4096. Their `_sum` counts each request at the bottom of its bucket, since nearly every request ZFS makes is a power of two in size. `zfs_*_bytes_total` over `zfs_*_operations_total` is the exact average.
- `aggregated` is `true` for requests ZFS made by joining smaller ones that sit next to each other on the disk, and `false` for requests as they were asked for.

## Scrubs and resilvers

Labels: `pool`. A pool's last scrub is `zfs_pool_scrub_*`, and its last resilver `zfs_pool_resilver_*`, with the same readings.

| Reading | Labels | What it says |
|---|---|---|
| `zfs_pool_scrub_state` | `state` | 1 for the state it is in, 0 for each other: `SCANNING`, `PAUSED`, `FINISHED`, `CANCELED` |
| `zfs_pool_scrub_start_timestamp_seconds` | | when it started |
| `zfs_pool_scrub_end_timestamp_seconds` | | when it finished or was canceled |
| `zfs_pool_scrub_to_examine_bytes` | | how much it has to look through in all |
| `zfs_pool_scrub_examined_bytes` | | how much it has looked through |
| `zfs_pool_scrub_issued_bytes` | | how much it has read from the disks: a part is done once it is read |
| `zfs_pool_scrub_skipped_bytes` | | how much it found it did not need to read |
| `zfs_pool_scrub_repaired_bytes` | | how much it repaired; for a resilver, `zfs_pool_resilver_resilvered_bytes` is how much it rebuilt |
| `zfs_pool_scrub_errors` | | errors it came across |
| `zfs_pool_scrub_done_ratio` | | how far along it is, from 0 to 1, while it runs |
| `zfs_pool_scrub_rate_bytes_per_second` | | how fast it is reading, while it runs and is not paused |
| `zfs_pool_scrub_remaining_seconds` | | how long it has left at that speed |

- ZFS keeps only a pool's last scrub or resilver. A resilver hides the scrub before it, so a pool has the readings of one or the other, and none until it has had either.
- How far along, how fast and how long are worked out as `zpool status` works them out, and only when it shows them. A time left is given once the scan reads at 10MiB a second or more.
- A finished scan is left as ZFS counts it. It can have looked through more than it set out to, as the pool changed under it.
- A rebuild started with `zpool replace -s`, and an error scrub started with `zpool scrub -e`, are counted apart by ZFS and are not here.

## Vdevs

Labels: `pool`, `class`, `vdev`. One for each top-level vdev, of every class.

| Reading | Labels | What it says |
|---|---|---|
| `zfs_vdev_latency_seconds` | `wait`, `op` | histogram of how long each request took |
| `zfs_vdev_request_size_bytes` | `queue`, `op`, `aggregated` | histogram of how big each request was |
| `zfs_vdev_operations_total` | `op` | reads and writes done |
| `zfs_vdev_bytes_total` | `op` | bytes read and written |
| `zfs_vdev_state` | `state` | 1 for its health, 0 for each other state |
| `zfs_vdev_errors_total` | `error` | requests to it that went wrong, on its own line in `zpool status` |
| `zfs_vdev_size_bytes` | | how big it is, raidz parity counted |
| `zfs_vdev_allocated_bytes` | | how much of it is used |
| `zfs_vdev_usable_bytes` | | how big it is with raidz parity taken out |
| `zfs_vdev_fragmentation_ratio` | | how broken up its free space is, from 0 to 1: `zpool list -v`'s `FRAG` |

- `class` is `data`, `special`, `dedup`, `log`, `cache` or `spare`.
- `vdev` is `zpool status`'s name for a vdev made of others, `raidz2-0`. A vdev that is one disk goes by its partition's name, `sda3`, as truenas-exporter names it.
- `zpool_influxdb` counts nothing of a pool's cache and spare disks, so they have no histograms, reads, writes or fragmentation. A cache disk has its size, and a spare its state: `AVAIL` or `INUSE`.
- A vdev whose free space is in small pieces writes more slowly as it fills. The pool's `FRAG` in `zpool list` is its data vdevs' weighed by their size: see [the query](queries.md#space).
- A pool's queues are counted for the pool only.

## Disks

Labels: `pool`, `class`, `vdev`, `disk`, `serial`. One for every disk in every pool, cache and spare disks too.

| Reading | Labels | What it says |
|---|---|---|
| `zfs_disk_info` | `guid`, `path`, `model`, `wwn`, `enclosure`, `slot` | always 1; what the disk is and where |
| `zfs_disk_latency_seconds` | `wait`, `op` | histogram of how long each request took at the disk itself |
| `zfs_disk_slow_ios_total` | | requests the disk took longer than `zfs_slow_io_threshold_seconds` to answer |
| `zfs_disk_state` | `state` | 1 for its health, 0 for each other state |
| `zfs_disk_errors_total` | `error` | requests to it that went wrong |
| `zfs_slow_io_threshold_seconds` | | how long that is: ZFS's `zio_slow_io_ms`, 30 seconds unless changed. No labels |

- `vdev` is the top-level vdev the disk is in.
- `disk` is the kernel's name for the disk today, `sdf`. It can change at a restart. `serial` does not, so join on that.
- A virtual disk with no serial number of its own has the name its maker gives it as `serial`, `drive-scsi0` from Proxmox, as `lsblk` names it.
- A disk ZFS cannot find has its `guid` as `disk` and no `serial`, as truenas-exporter labels it.
- Which disk is behind each path ZFS has is worked out every round, so a disk that was swapped or renamed is right at once.
- `enclosure` and `slot` are as sas-exporter names them: the kernel's name for the enclosure, `1:0:24:0`, and the slot's number. Both are empty for a disk in no enclosure.
- A disk's `wait` is `disk` only, unless [every disk timing](#every-disk-timing) is asked for.
- A spare has no slow requests or errors counted until it is in use. A cache disk has no latency.

### Every disk timing

By default a disk has only its own time at the disk, `wait="disk"`: the one number that sets a slow disk apart from the others in its vdev. `--disk-timings` (`ZFS_EXPORTER_DISK_TIMINGS=true`) gives each disk all that its vdev has:

| Reading | Labels | What it says |
|---|---|---|
| `zfs_disk_latency_seconds` | `wait`, `op` | every `wait`, not just `disk` |
| `zfs_disk_request_size_bytes` | `queue`, `op`, `aggregated` | histogram of how big each request was |
| `zfs_disk_operations_total` | `op` | reads and writes done |
| `zfs_disk_bytes_total` | `op` | bytes read and written |

- ZFS queues each request at the disk it is for, so a disk's queue waits are its own, and a vdev's are its disks' added together.
- It takes each disk from 69 readings to 586, and the page on data from 11,190 readings to 41,176.
- `--skip` still leaves out each kind: with `--skip request-sizes` each disk has every wait, and the page 23,536 readings.

## Whether ZFS is answering

| Reading | Labels | What it says |
|---|---|---|
| `zfs_up` | | 1 while the last round of each command was answered in full, 0 once one is not |
| `zfs_command_timeouts_total` | `command` | times a command ran past its time limit and was stopped |
| `zfs_command_stuck` | `command` | 1 while a command that was stopped has still not ended |
| `zfs_poll_interval_seconds` | `poll` | how often each command is run, once it has been |
| `zfs_poll_duration_seconds` | `poll` | how long the last run took |
| `zfs_poll_last_success_timestamp_seconds` | `poll` | when it was last answered in full |
| `zfs_poll_failures_total` | `poll` | runs that have failed since the program started |
| `zfs_exporter_build_info` | `version`, `commit`, `goversion` | always 1; the version that wrote the page |

- `poll` is `status`, for `zpool status`, or `stats`, for `zpool_influxdb`. `command` is `zpool` or `zpool_influxdb`.
- Each command is run on the program's own schedule, one at a time, and the page holds the last answers. A scrape never waits on ZFS.

### When ZFS stops answering

ZFS's commands wait on a lock of each pool they read. One waiting on a pool that is stuck can wait for good, and may not even end when killed.

- Each command is given 30 seconds (`--timeout`) and then killed. The round counts as failed, and `zfs_up` goes to 0.
- A command that does not end when killed is ZFS stuck. `zfs_command_stuck` says so, and no other command is started until it ends. The log says when it does.
- What a command last said stays on the page for three rounds, to ride out a blip, and is then left off. Old numbers are not shown as current.
- A pool busy with a scrub, a resilver or an import answers more slowly, which is why the limit is generous.
- A reading that has never been answered is absent, not zero.

## Leaving parts out

`--skip` leaves parts off the page. A part left out is not asked for either, where that saves a command:

- **Levels**: `pools`, `vdevs`, `disks`.
- **Kinds**: `poll`, `latency`, `request-sizes`, `queues`, `operations`, `slow-ios`, `space`, `health`, `scrub`, `resilver`.

| Kind | Readings |
|---|---|
| `latency` | `zfs_*_latency_seconds` |
| `request-sizes` | `zfs_*_request_size_bytes` |
| `queues` | `zfs_pool_active_requests`, `zfs_pool_pending_requests` |
| `operations` | `zfs_*_operations_total`, `zfs_*_bytes_total` |
| `slow-ios` | `zfs_disk_slow_ios_total`, `zfs_slow_io_threshold_seconds` |
| `space` | `zfs_*_size_bytes`, `zfs_*_allocated_bytes`, `zfs_*_usable_bytes`, `zfs_vdev_fragmentation_ratio` |
| `health` | `zfs_*_state`, `zfs_*_errors_total`, `zfs_pool_data_errors` |
| `scrub` | `zfs_pool_scrub_*` |
| `resilver` | `zfs_pool_resilver_*` |
| `poll` | `zfs_command_*`, `zfs_poll_*` |

A reading is on the page when neither its level nor its kind is left out. `zfs_disk_info` is the disks' alone, and `zfs_up` is always there.

```bash
zfs-exporter serve --skip request-sizes                       # every level, without the size histograms
zfs-exporter serve --metrics disks,latency                     # how long each disk takes, and nothing else
zfs-exporter serve --metrics health,scrub,resilver,slow-ios    # zpool status alone: zpool_influxdb is not run
```

`--metrics` names the only parts to ask about. Naming a level leaves out the other levels; naming a kind leaves out the other kinds. `--skip` then leaves parts out of those.

- With no latency or size on the page, `zpool_influxdb` is not asked for its histograms.
- Fragmentation is `zpool_influxdb`'s, so with `space` on the page it is run, without its histograms if nothing else wants them.
- With nothing it says on the page, it is not run at all.
- `zpool status` is always run: it is what says which disk is which.

## How many there are

On the TrueNAS machine this was written for, with three pools and 62 disks, the page holds 11,190 readings. A histogram is a reading for each of its buckets, its sum and its count: 29 for a latency, 18 for a size.

| Part | Readings | Of which sizes |
|---|---|---|
| each pool, with a finished scrub | 613 | 252 |
| each top-level vdev | 588 | 252 |
| each disk | 69 | |
| each disk, with every disk timing | 586 | 252 |

`--skip request-sizes` takes the page to 8,166. `--disk-timings` takes it to 41,176, or 23,536 without sizes. A scan that is running has up to two readings more than a finished one.
