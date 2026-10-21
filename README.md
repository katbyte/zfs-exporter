# zfs-exporter

[![GitHub release](https://img.shields.io/github/v/release/katbyte/zfs-exporter?color=blueviolet)](https://github.com/katbyte/zfs-exporter/releases/latest)
[![Go Version](https://img.shields.io/github/go-mod/go-version/katbyte/zfs-exporter?color=00ADD8)](https://github.com/katbyte/zfs-exporter/blob/main/go.mod)
[![License](https://img.shields.io/github/license/katbyte/zfs-exporter?color=blue)](https://github.com/katbyte/zfs-exporter/blob/main/LICENSE)
![build](https://github.com/katbyte/zfs-exporter/actions/workflows/build.yaml/badge.svg)
![tests](https://github.com/katbyte/zfs-exporter/actions/workflows/pr-tests.yaml/badge.svg)
![acceptance](https://github.com/katbyte/zfs-exporter/actions/workflows/pr-acceptance.yaml/badge.svg)
![lint](https://github.com/katbyte/zfs-exporter/actions/workflows/pr-golangci-lint.yaml/badge.svg)
[![coverage](https://img.shields.io/endpoint?url=https://raw.githubusercontent.com/katbyte/zfs-exporter/badges/coverage.json)](https://github.com/katbyte/zfs-exporter/actions/workflows/coverage.yaml)

Reports how long a Linux machine's ZFS pools, vdevs and disks take to answer as a [Prometheus](https://prometheus.io) metrics page.

It is there to show a disk going bad before it gives errors. A failing disk is often slow on a few requests first, and in a raidz vdev every stripe waits for its slowest disk. So every disk's own wait times are on the page, labelled by serial number, to set beside the other disks in its vdev and beside what other exporters say of the same disk.

Each pool's, vdev's and disk's health, errors and space, and each pool's last scrub or resilver, are there too.

**How it behaves.** It only ever reads. It runs the machine's own `zpool` and `zpool_influxdb`, so they always match the machine's ZFS. A scrape is answered from memory and never waits on ZFS. A command that hangs is given up on, and none is started while one is stuck.

## Install

```bash
docker pull ghcr.io/katbyte/zfs-exporter:latest     # or
go install github.com/katbyte/zfs-exporter@latest   # or
brew install katbyte/tap/zfs-exporter               # Homebrew on Linux
```

or a ready-built program from the [releases](https://github.com/katbyte/zfs-exporter/releases/latest). Linux only: it runs ZFS's own programs and reads the Linux kernel's `/sys`.

## Quick start

Run it on the machine the pools are on. It needs no root: ZFS lets anyone read the pools' state.

```bash
zfs-exporter print   # print the page once: a check that the pools can be seen
zfs-exporter serve   # serve it at :9087/metrics, with a health check at /healthz
```

With Docker, [`docker-compose.yml`](docker-compose.yml) runs the released image. It mounts the machine's `/` read-only at `/host` and gives the container `/dev/zfs`. That is how it runs on TrueNAS, which cannot install anything. See [running it in a container](docs/container.md).

```bash
docker compose up -d
curl -s http://127.0.0.1:9087/metrics
```

## What it reports

| Readings | What they say |
|---|---|
| [`zfs_pool_*`](docs/readings.md#pools) | each pool: its health, errors and space; how long requests took and how big they were, reads, writes and bytes, and requests queued |
| [`zfs_pool_scrub_*`, `zfs_pool_resilver_*`](docs/readings.md#scrubs-and-resilvers) | each pool's last scrub or resilver: whether it is running, how far along, how fast, how long left, and what it found |
| [`zfs_vdev_*`](docs/readings.md#vdevs) | each top-level vdev: the same as a pool, apart from the queues, and how broken up its free space is |
| [`zfs_disk_*`](docs/readings.md#disks) | each disk: its health and errors, how long each request took at the disk, requests slower than ZFS's limit, and what and where the disk is. With `--disk-timings`, all its vdev has |
| [`zfs_up`, `zfs_command_*`, `zfs_poll_*`](docs/readings.md#whether-zfs-is-answering) | whether ZFS is answering, and how the program's own asking is going |

How long is a histogram, so a disk's slowest 1% can be set beside the others in its vdev:

```promql
histogram_quantile(0.99, sum by (pool, vdev, serial, le) (rate(zfs_disk_latency_seconds_bucket{op="read"}[15m])))
```

Every disk has the labels truenas-exporter gives it: `pool`, `class`, `vdev`, `disk` and `serial`. `zfs_disk_info` adds its enclosure and slot as sas-exporter names them, so the three [join](docs/queries.md#joining-the-other-exporters) on `serial`.

## Settings

All are optional. Each can be a flag, an environment variable, or a line in a settings file.

| Variable | Flag | Default | What it sets |
|---|---|---|---|
| `ZFS_EXPORTER_ROOT` | `--root` | `/`, and `/host` in the image | where the machine's own `/` is |
| `ZFS_EXPORTER_LISTEN` | `--listen` | `:9087` | the address the page is served on |
| `ZFS_EXPORTER_LABELS` | `--labels` | none | labels for every reading, as `name=value,name=value` |
| `ZFS_EXPORTER_INTERVALS` | `--intervals` | `status=1m,stats=1m` | how often `zpool status` and `zpool_influxdb` are run |
| `ZFS_EXPORTER_TIMEOUT` | `--timeout` | `30s` | how long each is given before it is stopped |
| `ZFS_EXPORTER_METRICS` | `--metrics` | all | the only [parts](docs/readings.md#leaving-parts-out) to ask about: `disks,latency` |
| `ZFS_EXPORTER_SKIP` | `--skip` | none | [parts to leave out](docs/readings.md#leaving-parts-out), which are then not asked for: `request-sizes` |
| `ZFS_EXPORTER_DISK_TIMINGS` | `--disk-timings` | off | give each disk [every timing](docs/readings.md#every-disk-timing), size and count its vdev has: about four times the readings |
| `ZFS_EXPORTER_LOG` | | `WARN` | log level: `INFO` or `DEBUG` say more |
| `ZFS_EXPORTER_CONFIG` | `--config` | | the one settings file to read |

The settings file is `./.zfs-exporter` or `~/.zfs-exporter`. Its keys are the variable names without `ZFS_EXPORTER_`:

```env
ROOT=/host
INTERVALS=status=2m,stats=30s
```

A flag or an environment variable wins over the file.

## Scraping it

```yaml
scrape_configs:
  - job_name: zfs
    scrape_interval: 30s
    static_configs:
      - targets: ['127.0.0.1:9087']
```

A few things to ask of it:

```promql
zfs_up == 0                                        # ZFS is not answering, or a command failed
zfs_command_stuck == 1                             # a ZFS command will not end: ZFS is stuck on a pool
increase(zfs_disk_slow_ios_total[1h]) > 0          # a disk took longer than ZFS's limit to answer
zfs_pool_state{state="ONLINE"} == 0                # a pool that is not healthy
time() - zfs_pool_scrub_end_timestamp_seconds > 35 * 86400   # a pool not scrubbed in five weeks
```

[More queries](docs/queries.md), and the same scrape for Grafana Alloy.

## How this is built

- **Tested against a real machine.** The tests read what `zpool status` and `zpool_influxdb` printed on a TrueNAS machine with three pools and 62 disks, and that machine's disks as its `/dev` and `/sys` describe them.
- **Checked against ZFS's own source.** How `zpool_influxdb` names vdevs and labels its buckets was read from OpenZFS, and is tested against that machine's output.
- **The image is tested as it ships.** Live tests start the Docker image as its own user, hand it that machine with scripts in place of ZFS's programs, and scrape it. They make a command hang and see it given up on. CI fails if they fail.
- **What no test reaches.** ZFS itself. Whether the machine's own programs run from inside the container has to be seen on a real machine; see [running it in a container](docs/container.md).
- **Checks CI runs.** Unit tests under the race detector, and the live tests. golangci-lint, CodeQL, govulncheck and a scan of the built program. Linters for the workflows, shell scripts, YAML, markdown and spelling.

## More

- [Every reading and label](docs/readings.md)
- [Running it in a container](docs/container.md)
- [More queries](docs/queries.md)
- [Development, tests and bug reports](docs/development.md)
- [Contributing and releases](CONTRIBUTING.md)
