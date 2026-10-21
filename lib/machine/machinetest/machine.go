// Package machinetest lays out a made-up machine for a test: the real machine data's disks, with scripts in place of its program loader, zpool and
// zpool_influxdb that print what those printed on data. A test can make either program fail or hang, and see what each was asked.
package machinetest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/katbyte/zfs-exporter/lib/host/hosttest"
	"github.com/katbyte/zfs-exporter/lib/zpool/zpooltest"
)

// Where the made-up machine keeps its programs, as TrueNAS does.
const (
	Zpool  = "usr/sbin/zpool"
	Influx = "usr/lib/zfs-linux/zpool_influxdb"
)

// program prints what it is set to print, unless the test has made it fail or hang, and writes down what it was asked when it can: in a container
// the machine is mounted read-only. Each file it reads is beside it, named after it.
const program = `#!/bin/sh
echo "$*" >> "$0.asked" 2>/dev/null
if [ -e "$0.hang" ]; then sleep 3600; fi
if [ -e "$0.fail" ]; then cat "$0.fail" >&2; exit 1; fi
cat "$0.out"
`

// loader runs a program as the machine's own loader would: the libraries it is told of are the scripts' own business.
const loader = `#!/bin/sh
if [ "$1" = --library-path ]; then shift 2; fi
exec "$@"
`

// Data is where the made-up machine's / is: data's disks, and zpool and zpool_influxdb printing what they printed on data.
func Data(t *testing.T) string {
	t.Helper()

	return Machine(t, hosttest.Data+"f\tsys/module/zfs/parameters/zio_slow_io_ms\t30000\n", string(zpooltest.Status), string(zpooltest.Stats))
}

// Machine is where a made-up machine's / is: the lines laid out as hosttest lays them, with programs that print status and stats, and a loader for
// each kind of machine.
func Machine(t *testing.T, lines, status, stats string) string {
	t.Helper()

	root := hosttest.Machine(t, lines+"d\tusr/lib/x86_64-linux-gnu\nd\tusr/lib/aarch64-linux-gnu\nl\tlib\tusr/lib\nl\tlib64\tusr/lib64\n")
	write(t, root, Zpool, program, 0o755)
	write(t, root, Zpool+".out", status, 0o644)
	write(t, root, Influx, program, 0o755)
	write(t, root, Influx+".out", stats, 0o644)
	write(t, root, "usr/lib64/ld-linux-x86-64.so.2", loader, 0o755)
	write(t, root, "usr/lib/ld-linux-aarch64.so.1", loader, 0o755)

	return root
}

// write writes one file of the machine's.
func write(t *testing.T, root, name, text string, mode os.FileMode) {
	t.Helper()

	at := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Dir(at), 0o755); err != nil { //nolint:gosec // G301: a container's user has to be able to read the machine
		t.Fatal(err)
	}
	if err := os.WriteFile(at, []byte(text), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(at, mode); err != nil {
		t.Fatal(err)
	}
}

// Fail makes a program fail from now on, saying why as given.
func Fail(t *testing.T, root, program, why string) {
	t.Helper()

	write(t, root, program+".fail", why+"\n", 0o644)
}

// Hang makes a program hang from now on.
func Hang(t *testing.T, root, program string) {
	t.Helper()

	write(t, root, program+".hang", "", 0o644)
}

// Mend undoes Fail and Hang.
func Mend(t *testing.T, root, program string) {
	t.Helper()

	for _, ending := range []string{".fail", ".hang"} {
		if err := os.Remove(filepath.Join(root, program+ending)); err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
	}
}

// Asked is what a program was asked each time it ran, one line each.
func Asked(t *testing.T, root, program string) []string {
	t.Helper()

	b, err := os.ReadFile(filepath.Join(root, program+".asked")) //nolint:gosec // G304: the file is the test's own made-up machine
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}

	return strings.Split(strings.TrimSpace(string(b)), "\n")
}
