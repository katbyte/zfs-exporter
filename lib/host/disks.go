package host

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/katbyte/zfs-exporter/lib/poll"
)

// seatPrefix starts the name of the link from a disk to the enclosure slot it sits in, which the kernel adds when the enclosure says so.
const seatPrefix = "enclosure_device:"

// Disk is the disk behind a path ZFS has for one, /dev/disk/by-partuuid/<uuid>, as the machine knows it now: worked out each time it is asked, so a
// disk that was swapped or renamed is right at once. A path that leads to no disk is an error.
func (h Host) Disk(devPath string) (poll.Disk, error) {
	dev, err := h.Path(devPath)
	if err != nil {
		return poll.Disk{}, err
	}

	name := filepath.Base(dev)
	block, err := h.Path("/sys/class/block/" + name)
	if err != nil {
		return poll.Disk{}, fmt.Errorf("%s leads to %s: %w", devPath, name, errNoDisk)
	}

	// a partition sits in its disk's directory
	d := poll.Disk{Name: name, Partition: name}
	if _, err := os.Stat(filepath.Join(block, "partition")); err == nil {
		d.Name = filepath.Base(filepath.Dir(block))
		block = filepath.Dir(block)
	}

	device, err := h.Path(strings.TrimPrefix(block, h.root) + "/device")
	if errors.Is(err, fs.ErrNotExist) {
		return d, nil
	}
	if err != nil {
		return poll.Disk{}, err
	}

	// a SCSI or SATA disk keeps its serial number in a page of its own description, and an NVMe one beside its model. A virtual disk with neither goes
	// by the name its maker gives it in the page of its names, drive-scsi0, which is what udev, and so lsblk, calls its serial number
	d.Serial = serial(read(filepath.Join(device, "vpd_pg80")))
	if d.Serial == "" {
		d.Serial = text(filepath.Join(device, "serial"))
	}
	if d.Serial == "" {
		d.Serial = makersName(read(filepath.Join(device, "vpd_pg83")))
	}
	d.Model = text(filepath.Join(device, "model"))

	// a SCSI disk keeps its identifier with the device, an NVMe one with the namespace that is the disk
	d.WWN = wwn(text(filepath.Join(device, "wwid")))
	if d.WWN == "" {
		d.WWN = wwn(text(filepath.Join(block, "wwid")))
	}

	d.Enclosure, d.Slot, err = h.seat(device)

	return d, err
}

// seat is the enclosure and the slot a disk sits in, as the enclosure tells the kernel, which then links the disk to its slot: named as sas-exporter
// names them, so the two join. A disk no enclosure claims has no such link and no seat.
func (h Host) seat(device string) (enclosure, slot string, err error) {
	entries, err := os.ReadDir(device)
	if err != nil {
		return "", "", err
	}

	for _, entry := range entries {
		named, found := strings.CutPrefix(entry.Name(), seatPrefix)
		if !found {
			continue
		}

		at, err := h.Path(strings.TrimPrefix(filepath.Join(device, entry.Name()), h.root))
		if err != nil {
			return "", "", nil //nolint:nilerr // a slot the kernel links to but cannot show is no seat, and the disk is still the disk
		}

		return filepath.Base(filepath.Dir(at)), slotNumber(text(filepath.Join(at, "slot")), named), nil
	}

	return "", "", nil
}

// slotNumber is a slot's number as the enclosure gives it, or the name the enclosure calls the slot by when it gives no number.
func slotNumber(given, named string) string {
	if given == "" || strings.HasPrefix(given, "-") {
		return named
	}

	return given
}

// read is a file as it is, and nothing when it cannot be read.
func read(file string) []byte {
	b, err := os.ReadFile(file) //nolint:gosec // G304: the file is one of the kernel's under the machine's /sys
	if err != nil {
		return nil
	}

	return b
}

// wwn is a disk's world wide name as hex with nothing before it, from the kernel's own identifier for the disk: naa.5000cca2912345ab. The kernel
// names a disk with no such name some other way, a maker's and a model's name strung together say, and that is not one.
func wwn(id string) string {
	id = strings.ToLower(id)
	for _, prefix := range []string{"naa.", "eui."} {
		if rest, found := strings.CutPrefix(id, prefix); found {
			return rest
		}
	}

	return ""
}

// The page of a disk's own description that holds its serial number, as the kernel hands it over: four bytes saying what it is and how long, and
// then the number.
const (
	serialPage       = 0x80
	serialPageHeader = 4
)

// serial is the serial number in that page, less the spaces it is padded with, and nothing when the page is not the one expected.
func serial(page []byte) string {
	if len(page) < serialPageHeader || page[1] != serialPage {
		return ""
	}

	body := page[serialPageHeader:]
	if length := int(page[2])<<8 | int(page[3]); length < len(body) {
		body = body[:length]
	}

	return clean(string(body))
}

// The page of a disk's own description that lists the names it goes by, after the same four bytes: each name is four bytes saying what kind of name
// it is and how long, and then the name.
const (
	namesPage  = 0x83
	nameHeader = 4
	nameText   = 0x2 // the low half of a name's first byte when the name is text
	nameMakers = 0x0 // the low six bits of its second when the name is one the maker made up for the disk itself, not for a port or a group of them
)

// makersName is the first name in that page the maker made up for the disk itself, written as text, and nothing when there is none. A virtual disk
// with no page of its serial number is known by it.
func makersName(page []byte) string {
	if len(page) < serialPageHeader || page[1] != namesPage {
		return ""
	}

	names := page[serialPageHeader:]
	if length := int(page[2])<<8 | int(page[3]); length < len(names) {
		names = names[:length]
	}

	for len(names) >= nameHeader {
		end := nameHeader + int(names[3])
		if end > len(names) {
			return ""
		}

		if names[0]&0x0f == nameText && names[1]&0x3f == nameMakers {
			if name := clean(string(names[nameHeader:end])); name != "" {
				return name
			}
		}
		names = names[end:]
	}

	return ""
}
