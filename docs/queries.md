# Queries

## Scraping

Prometheus:

```yaml
scrape_configs:
  - job_name: zfs
    scrape_interval: 30s
    static_configs:
      - targets: ['127.0.0.1:9087']
```

Grafana Alloy:

```alloy
prometheus.scrape "zfs" {
  targets         = [{"__address__" = "127.0.0.1:9087"}]
  job_name        = "zfs"
  scrape_interval = "30s"
  forward_to      = [prometheus.remote_write.default.receiver]
}
```

ZFS is asked once a minute, so a scrape more often than that reads the same answer again.

## Whether ZFS is answering

```promql
zfs_up == 0                                                  # ZFS is not answering, or a command failed
zfs_command_stuck == 1                                       # a ZFS command will not end: ZFS is stuck on a pool
increase(zfs_command_timeouts_total[1h]) > 0                 # a command ran past its time limit
time() - zfs_poll_last_success_timestamp_seconds > 300       # nothing new from ZFS in five minutes
```

## Disks

Each disk's slowest 1% of reads over the last 15 minutes:

```promql
histogram_quantile(0.99, sum by (pool, vdev, serial, le) (rate(zfs_disk_latency_seconds_bucket{op="read"}[15m])))
```

A disk whose slowest 1% is three times its vdev's middle disk's, which is the one that holds up every stripe it is in:

```promql
  histogram_quantile(0.99, sum by (pool, vdev, serial, le) (rate(zfs_disk_latency_seconds_bucket{op="read"}[15m])))
/ on (pool, vdev) group_left ()
  quantile by (pool, vdev) (0.5, histogram_quantile(0.99, sum by (pool, vdev, serial, le) (rate(zfs_disk_latency_seconds_bucket{op="read"}[15m]))))
> 3
```

```promql
increase(zfs_disk_slow_ios_total[1h]) > 0                    # a disk took longer than ZFS's limit to answer
sum by (pool, vdev, serial) (rate(zfs_disk_latency_seconds_bucket{le="+Inf"}[5m]))  # each disk's requests a second
```

A quiet disk has few requests to work its slowest 1% from, so look at a vdev while it is busy: during a scrub every disk is read.

## Pools and vdevs

```promql
histogram_quantile(0.99, sum by (pool, wait, op, le) (rate(zfs_pool_latency_seconds_bucket[5m])))     # each pool's slowest 1%, by wait
histogram_quantile(0.5, sum by (pool, vdev, op, le) (rate(zfs_vdev_latency_seconds_bucket{wait="total"}[5m])))  # each vdev's middle request
rate(zfs_pool_latency_seconds_sum{wait="total"}[5m]) / rate(zfs_pool_latency_seconds_count{wait="total"}[5m])   # each pool's average, worked out as zpool iostat -l does
rate(zfs_vdev_bytes_total[5m]) / rate(zfs_vdev_operations_total[5m])                                   # each vdev's average request size, exact
zfs_pool_pending_requests > 0                                                                         # requests waiting their turn
```

A pool's time at the disks against its time waiting in queues says which is holding it up:

```promql
histogram_quantile(0.99, sum by (pool, wait, le) (rate(zfs_pool_latency_seconds_bucket{op="read", wait=~"disk|sync_queue|async_queue"}[5m])))
```

## Health

```promql
zfs_pool_state{state="ONLINE"} == 0                          # a pool that is not healthy
zfs_disk_state{class!="spare", state="ONLINE"} == 0          # a disk that is not online
zfs_disk_state{state="INUSE"} == 1                           # a spare standing in for a disk
increase(zfs_disk_errors_total[1h]) > 0                      # a disk with new errors
zfs_pool_data_errors > 0                                     # damaged files: zpool status -v names them
```

## Space

```promql
zfs_pool_allocated_bytes / zfs_pool_size_bytes > 0.8                                     # a pool more than 80% full
zfs_pool_size_bytes - zfs_pool_allocated_bytes                                           # what is free, as zpool list shows it
predict_linear(zfs_pool_allocated_bytes[7d], 30 * 86400) > zfs_pool_size_bytes           # full within a month at this week's rate
zfs_vdev_allocated_bytes{class="special"} / zfs_vdev_size_bytes > 0.75                   # a special vdev past the point ZFS stops putting small blocks on it
```

Each pool's `FRAG`, about as `zpool list` works it out from its vdevs:

```promql
sum by (pool) (zfs_vdev_fragmentation_ratio{class="data"} * on (pool, class, vdev) zfs_vdev_size_bytes) / sum by (pool) (zfs_vdev_size_bytes{class="data"})
```

## Scrubs and resilvers

```promql
time() - zfs_pool_scrub_end_timestamp_seconds > 35 * 86400      # not scrubbed in five weeks
zfs_pool_scrub_errors > 0 or zfs_pool_scrub_repaired_bytes > 0  # the last scrub found something
zfs_pool_scrub_state{state="SCANNING"} == 1                     # a scrub running now
zfs_pool_scrub_done_ratio                                       # how far along it is
zfs_pool_resilver_remaining_seconds / 3600                      # hours a resilver has left
```

ZFS keeps only a pool's last scrub or resilver, so a pool whose last was a resilver has no scrub readings, and the first query above says nothing of it. This finds a pool with no finished scrub on record:

```promql
zfs_pool_state{state="ONLINE"} unless on (pool) zfs_pool_scrub_state{state="FINISHED"} == 1
```

## Joining the other exporters

Every disk reading has a `serial`, which never changes. `zfs_disk_info` has the rest of what is known of the disk.

Where each slow disk sits, from this exporter alone:

```promql
increase(zfs_disk_slow_ios_total[1d]) * on (pool, serial) group_left (enclosure, slot, model) zfs_disk_info > 0
```

The card each disk hangs off, from sas-exporter, beside its slowest 1%:

```promql
  histogram_quantile(0.99, sum by (pool, vdev, serial, le) (rate(zfs_disk_latency_seconds_bucket{op="read"}[15m])))
* on (serial) group_left (card, enclosure, slot) sas_device_info
```

The errors truenas-exporter counts on the same disk, which has the same `pool`, `class`, `vdev`, `disk` and `serial`:

```promql
truenas_pool_device_read_errors * on (pool, serial) group_left () (increase(zfs_disk_slow_ios_total[1d]) > 0)
```

A disk's `disk` label, `sdf`, can change at a restart, and the node exporter knows a disk only by that, as `device`:

```promql
rate(node_disk_io_time_seconds_total[5m]) * on (device) group_left (pool, vdev, serial) label_replace(zfs_disk_info, "device", "$1", "disk", "(.+)")
```
