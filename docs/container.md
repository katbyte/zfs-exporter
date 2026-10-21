# Running it in a container

TrueNAS cannot install anything, so on TrueNAS the program runs in a container. It still runs the machine's own `zpool` and `zpool_influxdb`, so they always match the ZFS the machine runs, after an update too. An image with ZFS's programs of its own would stop working the first time the machine's ZFS was updated past them.

## What it is given

[`docker-compose.yml`](../docker-compose.yml) gives the container two things, and no privilege:

```yaml
volumes:
  - /:/host:ro
devices:
  - /dev/zfs:/dev/zfs
```

- **The machine's `/`, read-only, at `/host`.** The programs are read from there, and run through the machine's own program loader with the machine's own libraries, since the image's are not ones they were built for. `/host/dev` and `/host/sys` say which disk is behind each path ZFS has for one.
- **`/dev/zfs`.** Every ZFS program asks through it. ZFS lets anyone read the pools' state through it: it is mode `0666`, and both programs work as an ordinary user.
- The image runs as its own user, not root.
- `ZFS_EXPORTER_ROOT=/host` is set in the image. Run outside a container, the program reads `/`.

## Mounting less than the whole of /

The program reads only `/usr`, `/dev` and `/sys`. Mounting just those works as well:

```yaml
volumes:
  - /usr:/host/usr:ro
  - /dev:/host/dev:ro
  - /sys:/host/sys:ro
```

On Debian, and so TrueNAS, everything the programs need is under `/usr`, and `/lib` and `/lib64` are links into it. The program looks for the loader there when the links are not mounted.

## How a program is run

On a machine whose `/` is at `/host`, `zpool status` is run as:

```bash
/host/usr/lib/x86_64-linux-gnu/ld-linux-x86-64.so.2 --library-path /host/usr/lib/x86_64-linux-gnu:/host/usr/lib /host/usr/sbin/zpool status -s -p -j --json-int
```

- The loader and the programs are found by following the machine's own links under `/host`, a link to `/lib` leading to `/host/lib`.
- `zpool_influxdb` is looked for where OpenZFS puts it, `/usr/libexec/zfs`, and where Debian moves it, `/usr/lib/zfs-linux`.
- A program is given a bare environment: a path, the C locale, and `ZFS_MODULE_TIMEOUT=0`, so a container without `/dev/zfs` says so at once rather than ZFS waiting ten seconds for one to appear.
- A program and anything it starts is killed together when it runs past its limit.

## What has been checked, and what has not

- The same command, run through the loader as above but on the machine itself, gives the same answer as `zpool` run directly. That was checked on the TrueNAS machine this was written for, as an ordinary user.
- The image's own tests run it as it ships, with a made-up machine copied in at `/host` and scripts in place of the loader and ZFS's programs.
- **Not yet checked: the image on a TrueNAS machine.** Before relying on it, run `print` once and read what it says:

```bash
docker run --rm -v /:/host:ro --device /dev/zfs ghcr.io/katbyte/zfs-exporter:latest print
```

A page with `zfs_up 1` and a `zfs_disk_info` for every disk is the image working. If it fails, the error says which command failed and what it said.

## When it cannot run

- Without `/host` mounted it stops at once, saying so and naming `ZFS_EXPORTER_ROOT`.
- Without ZFS's programs under `/host` it stops at once, saying where it looked.
- Without `/dev/zfs` every round fails, and the log and `zfs_up` say so, with what `zpool` said of it.
