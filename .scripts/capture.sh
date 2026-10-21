#!/bin/sh
# Writes down what the machine says of the disks behind its pools, as text: the links that lead from each path ZFS has for a disk to the disk, and
# what the files zfs-exporter reads of each disk hold. It only reads, needs no root and leaves nothing behind. The output names disks by serial
# number, so look before posting it, or run it through anonymize.sh.
#
#   .scripts/capture.sh > capture.txt                      # on the machine with the pools
#   ssh nas 'sh -s' < .scripts/capture.sh > capture.txt    # or from another, with nothing copied over
#
# Each line is a kind, a path from / and for most kinds a value, with a tab between them:
#   l  a link, and where it points
#   f  a file zfs-exporter reads, and its first line
#   x  the same for a file that is not text, as hex
#   !  a file zfs-exporter reads that could not be read, and why
#   b  a device, by name only: it is never opened
set -u

root="${1:-/}"
root="${root%/}"

# rel is a path as it is printed: from the root, with no slash before it.
rel() {
	printf '%s' "${1#"$root"/}"
}

# link writes the line for a link.
link() {
	printf 'l\t%s\t%s\n' "$(rel "$1")" "$(readlink "$1")"
}

# file writes the line for a file zfs-exporter reads, when it is there: its first line, or as hex for a page that is not text.
file() {
	[ -e "$1" ] || return 0

	case "${1##*/}" in
	vpd_pg80)
		if value=$(od -An -v -tx1 "$1" 2>&1); then
			printf 'x\t%s\t%s\n' "$(rel "$1")" "$(printf '%s' "$value" | tr -d ' \n')"
		else
			printf '!\t%s\t%s\n' "$(rel "$1")" "$value"
		fi
		;;
	*)
		if value=$(head -n 1 "$1" 2>&1); then
			printf 'f\t%s\t%s\n' "$(rel "$1")" "$value"
		else
			printf '!\t%s\t%s\n' "$(rel "$1")" "$value"
		fi
		;;
	esac
}

# real is where a directory really is, every link on the way followed.
real() {
	(cd "$1" 2>/dev/null && pwd -P)
}

file "$root/sys/module/zfs/parameters/zio_slow_io_ms"

# the paths ZFS has for its disks, which on TrueNAS are all here; zpool status names any that are elsewhere, when it is there to ask
for path in "$root"/dev/disk/by-partuuid/*; do
	[ -L "$path" ] && link "$path"
done
if [ "$root" = "" ] && command -v zpool >/dev/null 2>&1; then
	zpool status -P 2>/dev/null | awk '$1 ~ /^\/dev\/disk\// && $1 !~ /by-partuuid/ { print $1 }' | while IFS= read -r path; do
		[ -L "$path" ] && link "$path"
	done
fi

# every block device: the link that says where it is, whether it is part of a disk, and its device file by name
for class in "$root"/sys/class/block/*; do
	[ -L "$class" ] || continue
	name=${class##*/}

	link "$class"
	dir=$(real "$class") || continue
	file "$dir/partition"
	[ -b "$root/dev/$name" ] && printf 'b\t%s\n' "$(rel "$root/dev/$name")"
done

# every disk: what it is, and the slot it sits in
for block in "$root"/sys/block/*; do
	[ -L "$block" ] || continue

	link "$block"
	dir=$(real "$block") || continue
	file "$dir/wwid"

	[ -L "$dir/device" ] || continue
	link "$dir/device"
	device=$(real "$dir/device") || continue
	for name in vendor model rev serial wwid vpd_pg80; do
		file "$device/$name"
	done

	for seat in "$device"/enclosure_device:*; do
		[ -L "$seat" ] || continue
		link "$seat"
		slot=$(real "$seat") && file "$slot/slot"
	done
done
