# Development

```bash
make            # format and build
make test       # the tests, under the race detector
make testacc    # the live tests: the image, as docker runs it
make cover      # the tests with coverage
make check-all  # build, tests, live tests, every linter and the dependency check
make tools      # install the pinned dev tools into .tools/bin
```

Dependencies are vendored. The dev tools are pinned in `.tools/go.mod` and the makefile.

## Where things are

| Path | What it does |
|---|---|
| `lib/command` | runs ZFS's programs one at a time, each with a time limit, and holds back the rest while one is stuck |
| `lib/zpool` | reads what `zpool status` and `zpool_influxdb` print |
| `lib/host` | finds each disk from the path ZFS has for it, through the machine's own `/dev` and `/sys` |
| `lib/machine` | puts those together: the programs, found and run through the machine's loader, and each disk found |
| `lib/poll` | says what is asked and how often |
| `lib/readings` | writes the answers as readings, each disk and vdev where `zpool status` says it is |
| `cli` | the commands and settings |
| `acceptance` | the live tests of the image |

The rest is shared with the other exporters, from [go-kt](https://github.com/katbyte/go-kt)'s `prometheus` packages:

- `metrics`: the page, `/metrics`, `/healthz`, the labels, and the list and writer of readings.
- `poller`: asking on a schedule, keeping the last answers, and the `zfs_poll_*` readings.
- `exporter`: what `print` and `serve` run.
- `metricstest`: the saved-page check, the naming check, and the live tests' helpers.

## The tests

`make test` needs no ZFS.

- What `zpool status` and `zpool_influxdb` printed on a TrueNAS machine with three pools and 62 disks is in [`lib/zpool/zpooltest/testdata`](../lib/zpool/zpooltest/testdata). `zpool_influxdb`'s is cut down to that machine's first two raidz2 vdevs.
- That machine's disks, as its `/dev` and `/sys` describe them, are in [`data.txt`](../lib/host/hosttest/testdata/data.txt), with every serial number and world wide name made up.
- `lib/machine/machinetest` lays the two out as a machine, with scripts in place of its loader, `zpool` and `zpool_influxdb`. A test can make either program fail or hang.
- So the commands' tests run the program whole against that machine, as a container runs it.
- The whole page for a made-up pool is kept in [`page.txt`](../lib/readings/testdata/page.txt), so a renamed reading shows up as a difference. `go test ./lib/readings -update` rewrites it once the change is meant.

## The live tests

`make testacc` starts the image `make docker` builds and scrapes it over HTTP. It needs `docker` and takes a few seconds.

- Handed the saved machine at `/host`, the image runs the scripts through the machine's loader and serves every disk by its serial number.
- It passes its own health check and does not run as root.
- `zpool_influxdb` is then made to hang. The page shows it given up on while `zpool status` goes on, and ZFS up again once it answers.
- Given nothing, it stops at once and names the setting.

## What the tests cannot reach

- **ZFS itself.** What its programs print when a pool is stuck, or a disk is pulled, is only seen on a machine with pools. Those paths are tested with programs made to fail or hang.
- **The image on TrueNAS.** Whether the machine's own programs run from inside the container. See [running it in a container](container.md).

## When a reading looks wrong

[`capture.sh`](../.scripts/capture.sh) writes down what the machine says of the disks behind its pools, as text: the links from each path ZFS has to the disk, and what each disk says of itself. It only reads and needs no root.

```bash
.scripts/capture.sh > capture.txt                       # on the machine with the pools
ssh nas 'sh -s' < .scripts/capture.sh > capture.txt     # or from another, with nothing copied over
.scripts/anonymize.sh < capture.txt > shareable.txt     # before showing it to anyone
zpool status -s -p -j --json-int > status.json           # and what the two programs print
/usr/lib/zfs-linux/zpool_influxdb --sum-histogram-buckets > stats.txt
```

- A capture names every disk by serial number and world wide name. [`anonymize.sh`](../.scripts/anonymize.sh) swaps each for a made-up one, the same one each time it appears.
- What `zpool status` and `zpool_influxdb` print names disks only by partition UUID and ZFS's own GUID.
- `zpool_influxdb` is in `/usr/libexec/zfs` where OpenZFS puts it, and in `/usr/lib/zfs-linux` on Debian and TrueNAS.
