package host_test

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/katbyte/zfs-exporter/lib/host"
	"github.com/katbyte/zfs-exporter/lib/host/hosttest"
	"github.com/katbyte/zfs-exporter/lib/poll"
)

// The pool disk the tests look at most closely: sdaa, in slot 1 of an enclosure behind the first card's second expander.
const (
	poolDisk = "/dev/disk/by-partuuid/4a591c08-dac9-44c9-8523-c294b3a86af5"
	scsiDir  = "sys/devices/pci0000:00/0000:00:1c.0/0000:01:00.0/host1/port-1:0/expander-1:0/port-1:0:25/expander-1:1/port-1:1:1/end_device-1:1:1/target1:0:26/1:0:26:0"
)

// Each disk on the real machine is found from the path ZFS has for it, with what it is and where it sits, named as sas-exporter names them.
func TestADiskIsFoundFromItsPath(t *testing.T) {
	t.Parallel()

	h := host.New(hosttest.Machine(t, hosttest.Data))

	for _, tc := range []struct {
		name, path string
		want       poll.Disk
	}{
		{"a SAS disk in a slot", poolDisk, poll.Disk{Name: "sdaa", Partition: "sdaa1", Serial: "Z0000001", Model: "WUH722020BL5204", WWN: "5000cca000000001", Enclosure: "1:0:35:0", Slot: "1"}},
		{"a cache disk named without a link", "/dev/nvme0n1p1", poll.Disk{Name: "nvme0n1", Partition: "nvme0n1p1", Serial: "Z0000000000060", Model: "SAMSUNG MZQL27T6HBLA-00A07"}},
		{"a virtual disk with nothing to say of itself", "/dev/sda3", poll.Disk{Name: "sda", Partition: "sda3", Model: "QEMU HARDDISK"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, err := h.Disk(tc.path)
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Errorf("got %+v, want %+v", got, tc.want)
			}
		})
	}
}

// Every disk in a pool on the real machine is found, and every SAS one has a serial number and a slot.
func TestEveryDiskOnTheRealMachineIsFound(t *testing.T) {
	t.Parallel()

	root := hosttest.Machine(t, hosttest.Data)
	h := host.New(root)

	links, err := os.ReadDir(filepath.Join(root, "dev", "disk", "by-partuuid"))
	if err != nil {
		t.Fatal(err)
	}

	slotted := 0
	for _, link := range links {
		d, err := h.Disk("/dev/disk/by-partuuid/" + link.Name())
		if err != nil {
			t.Errorf("%s: %v", link.Name(), err)
			continue
		}

		if d.Enclosure != "" {
			slotted++
			if d.Serial == "" || d.Slot == "" || d.WWN == "" {
				t.Errorf("%s is %+v, which lacks a serial number, a slot or a world wide name", link.Name(), d)
			}
		}
	}

	if slotted != 58 {
		t.Errorf("%d disks were found in a slot, want the machine's 58", slotted)
	}
}

// The disk behind a path changes as soon as the link does: a disk swapped for another under the same name, or renamed, is right on the next look.
func TestADiskIsWorkedOutAfreshEachTime(t *testing.T) {
	t.Parallel()

	root := hosttest.Machine(t, hosttest.Data)
	h := host.New(root)

	link := filepath.Join(root, poolDisk)
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../../sdab1", link); err != nil {
		t.Fatal(err)
	}

	got, err := h.Disk(poolDisk)
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "sdab" || got.Serial == "Z0000001" {
		t.Errorf("the path now leads to sdab, and was found to be %+v", got)
	}
}

// A path that leads nowhere, as a disk that has gone leaves, is an error, and so is one that leads to something the kernel has no disk for.
func TestAPathWithNoDisk(t *testing.T) {
	t.Parallel()

	root := hosttest.Machine(t, hosttest.Data+"l\tdev/disk/by-partuuid/stray\t../../ghost1\nb\tdev/ghost1\n")
	h := host.New(root)

	if _, err := h.Disk("/dev/disk/by-partuuid/gone"); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("a path that is not there gave %v", err)
	}
	if _, err := h.Disk("/dev/disk/by-partuuid/stray"); err == nil {
		t.Error("a path to a device the kernel has no disk for gave no error")
	}
}

// A disk whose enclosure link leads nowhere is still found, without a seat.
func TestADiskWhoseSlotCannotBeFollowed(t *testing.T) {
	t.Parallel()

	root := hosttest.Machine(t, hosttest.Data)
	if err := os.RemoveAll(filepath.Join(root, "sys", "devices", "pci0000:00", "0000:00:1c.0", "0000:01:00.0", "host1", "port-1:0", "expander-1:0", "port-1:0:25", "expander-1:1", "port-1:1:10")); err != nil {
		t.Fatal(err)
	}

	got, err := host.New(root).Disk(poolDisk)
	if err != nil {
		t.Fatal(err)
	}
	if got.Serial != "Z0000001" || got.Enclosure != "" || got.Slot != "" {
		t.Errorf("got %+v, want the disk without a seat", got)
	}
}

// A slot the enclosure gives no number is named as the enclosure names it, as sas-exporter names it.
func TestASlotWithNoNumber(t *testing.T) {
	t.Parallel()

	tree := hosttest.Machine(t, `l	dev/disk/by-partuuid/p	../../sdb1
b	dev/sdb1
l	sys/class/block/sdb1	../../devices/d/block/sdb/sdb1
f	sys/devices/d/block/sdb/sdb1/partition	1
l	sys/devices/d/block/sdb/device	../../../d
f	sys/devices/d/model	ST4000NM0035
l	sys/devices/d/enclosure_device:Disk 7	../e/enclosure/2:0:9:0/Disk 7
f	sys/devices/e/enclosure/2:0:9:0/Disk 7/slot	-1
`)

	got, err := host.New(tree).Disk("/dev/disk/by-partuuid/p")
	if err != nil {
		t.Fatal(err)
	}
	if got.Enclosure != "2:0:9:0" || got.Slot != "Disk 7" {
		t.Errorf("got %+v, want slot Disk 7 of 2:0:9:0", got)
	}
}

// A link that starts at / starts at the machine's own /, wherever that is mounted, and none leads out of it.
func TestPathsStayOnTheMachine(t *testing.T) {
	t.Parallel()

	root := hosttest.Machine(t, `l	lib64/ld-linux-x86-64.so.2	/lib/x86_64-linux-gnu/ld-linux-x86-64.so.2
l	lib	usr/lib
f	usr/lib/x86_64-linux-gnu/ld-linux-x86-64.so.2	ELF
l	escape	../../../../../../etc
l	round	./round
`)
	h := host.New(root)

	got, err := h.Path("/lib64/ld-linux-x86-64.so.2")
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(root, "usr", "lib", "x86_64-linux-gnu", "ld-linux-x86-64.so.2"); got != want {
		t.Errorf("the loader is at %s, want %s", got, want)
	}

	if got, err := h.Path("/escape"); err == nil && got != root {
		t.Errorf("a link out of the machine led to %s", got)
	}
	if _, err := h.Path("/round"); err == nil {
		t.Error("a link to itself gave no error")
	}
}

// How slow a request is before ZFS counts it is read from the machine, and is nothing when the machine does not say.
func TestSlowAfter(t *testing.T) {
	t.Parallel()

	if got := host.New(hosttest.Machine(t, "f\tsys/module/zfs/parameters/zio_slow_io_ms\t30000\n")).SlowAfter(); got != 30*time.Second {
		t.Errorf("got %s, want 30s", got)
	}
	if got := host.New(t.TempDir()).SlowAfter(); got != 0 {
		t.Errorf("a machine with no ZFS gave %s", got)
	}
}

// A program is found at the first of its places the machine has, and one it has at none of them says where it was looked for.
func TestProgram(t *testing.T) {
	t.Parallel()

	root := hosttest.Machine(t, "f\tusr/lib/zfs-linux/zpool_influxdb\tELF\nl\tsbin\tusr/sbin\nf\tusr/sbin/zpool\tELF\n")
	h := host.New(root)

	got, err := h.Program("zpool_influxdb", "/usr/libexec/zfs/zpool_influxdb", "/usr/lib/zfs-linux/zpool_influxdb")
	if err != nil || got != filepath.Join(root, "usr", "lib", "zfs-linux", "zpool_influxdb") {
		t.Errorf("got %s (%v)", got, err)
	}

	got, err = h.Program("zpool", "/sbin/zpool")
	if err != nil || got != filepath.Join(root, "usr", "sbin", "zpool") {
		t.Errorf("got %s (%v)", got, err)
	}

	if _, err := h.Program("zdb", "/usr/sbin/zdb"); err == nil {
		t.Error("a program that is not there was found")
	}
}
