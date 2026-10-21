package machine

import (
	"strings"
	"testing"

	"github.com/katbyte/zfs-exporter/lib/host"
	"github.com/katbyte/zfs-exporter/lib/host/hosttest"
)

// debian is the loader and libraries as Debian, and so TrueNAS, lays them out: everything under /usr, and /lib and /lib64 links to it, the loader's
// own link leading off from /.
const debian = `l	lib	usr/lib
l	lib64	usr/lib64
l	usr/lib64/ld-linux-x86-64.so.2	/lib/x86_64-linux-gnu/ld-linux-x86-64.so.2
f	usr/lib/x86_64-linux-gnu/ld-linux-x86-64.so.2	ELF
f	usr/lib/x86_64-linux-gnu/libzfs.so.6	ELF
f	usr/lib/aarch64-linux-gnu/ld-linux-aarch64.so.1	ELF
`

// The machine's loader is found through the links the machine has, and its libraries named once each, whether the whole of its / is mounted or
// only its /usr; and on the machine itself nothing is run through anything.
func TestTheLoader(t *testing.T) {
	t.Parallel()

	whole := hosttest.Machine(t, debian)
	usr := hosttest.Machine(t, strings.Join(without(strings.Split(debian, "\n"), "l\tlib\t", "l\tlib64\t"), "\n"))

	for _, tc := range []struct {
		name, root, goarch, want string
	}{
		{"the whole of / for amd64", whole, "amd64", whole + "/usr/lib/x86_64-linux-gnu/ld-linux-x86-64.so.2 --library-path " + whole + "/usr/lib/x86_64-linux-gnu:" + whole + "/usr/lib"},
		{"only /usr for amd64", usr, "amd64", usr + "/usr/lib/x86_64-linux-gnu/ld-linux-x86-64.so.2 --library-path " + usr + "/usr/lib/x86_64-linux-gnu:" + usr + "/usr/lib"},
		{"the whole of / for arm64", whole, "arm64", whole + "/usr/lib/aarch64-linux-gnu/ld-linux-aarch64.so.1 --library-path " + whole + "/usr/lib/aarch64-linux-gnu:" + whole + "/usr/lib"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, err := (&Machine{host: host.New(tc.root)}).loader(tc.goarch)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Join(got, " ") != tc.want {
				t.Errorf("runs through %q, want %q", strings.Join(got, " "), tc.want)
			}
		})
	}

	if got, err := (&Machine{host: host.New("/")}).loader("amd64"); err != nil || got != nil {
		t.Errorf("on the machine itself it runs through %q (%v), want nothing", got, err)
	}
	if _, err := (&Machine{host: host.New(whole)}).loader("riscv64"); err == nil {
		t.Error("a build whose loader is not known gave no error")
	}
}

// without is the lines less those that start with any of these.
func without(lines []string, starts ...string) []string {
	var out []string
	for _, line := range lines {
		keep := true
		for _, start := range starts {
			keep = keep && !strings.HasPrefix(line, start)
		}
		if keep {
			out = append(out, line)
		}
	}

	return out
}
