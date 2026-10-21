// Package host reads what the machine the pools are on says of itself, through its own files: where each disk is and what it is, how slow a request
// has to be for ZFS to count it, and where its ZFS programs are. In a container the machine's own / is mounted somewhere, /host say, and every path
// is read under that, with each link followed as the machine itself would follow it.
package host

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// maxLinks is how many links one path may pass through before it is taken to go round in a circle, as the kernel takes it.
const maxLinks = 40

// Host is the machine, as its files are seen from where the program runs.
type Host struct {
	root string
}

// New is the machine whose own / is at root: / when the program runs on it directly.
func New(root string) Host {
	if root == "" {
		root = "/"
	}

	return Host{root: filepath.Clean(root)}
}

// Root is where the machine's own / is.
func (h Host) Root() string { return h.root }

// Path is where a path on the machine is from here, every link on the way followed: a link that starts with / starts at the machine's /, and none
// leads out of it. A path that is not there is an error.
func (h Host) Path(p string) (string, error) {
	parts := strings.Split(p, "/")
	done := ""

	for links := 0; len(parts) > 0; {
		part := parts[0]
		parts = parts[1:]

		switch part {
		case "", ".":
			continue
		case "..":
			// the machine's / is as far up as a path goes
			if done = path.Dir(done); done == "." || done == "/" {
				done = ""
			}

			continue
		}

		next := path.Join(done, part)
		full := filepath.Join(h.root, next)

		info, err := os.Lstat(full)
		if err != nil {
			return "", err
		}
		if info.Mode()&fs.ModeSymlink == 0 {
			done = next
			continue
		}

		links++
		if links > maxLinks {
			return "", fmt.Errorf("%s: too many links", p)
		}

		target, err := os.Readlink(full)
		if err != nil {
			return "", err
		}
		if strings.HasPrefix(target, "/") {
			done = ""
		}
		parts = append(strings.Split(target, "/"), parts...)
	}

	return filepath.Join(h.root, done), nil
}

// text is the first line of a file, less the spaces around it, and nothing when it cannot be read: a disk that does not have it, or a file the kernel
// refuses to read for this disk, as it refuses a virtual disk's wwid.
func text(file string) string {
	b, err := os.ReadFile(file) //nolint:gosec // G304: the file is one of the kernel's under the machine's /sys
	if err != nil {
		return ""
	}

	first, _, _ := strings.Cut(string(b), "\n")

	return clean(first)
}

// clean is what the kernel wrote less the padding around it, and less anything a metrics store would refuse as a label.
func clean(s string) string {
	return strings.Trim(strings.ToValidUTF8(s, ""), " \t\r\n\x00")
}

// slowAfterFile is where ZFS says how many milliseconds a request takes before it is counted as slow.
const slowAfterFile = "/sys/module/zfs/parameters/zio_slow_io_ms"

// SlowAfter is how long a request takes before ZFS counts it as slow, and 0 when the machine does not say.
func (h Host) SlowAfter() time.Duration {
	ms, err := strconv.ParseUint(text(filepath.Join(h.root, slowAfterFile)), 10, 32)
	if err != nil {
		return 0
	}

	return time.Duration(ms) * time.Millisecond
}

// Program is where the first of these programs the machine has is, from here; an error says where it looked.
func (h Host) Program(name string, places ...string) (string, error) {
	for _, place := range places {
		at, err := h.Path(place)
		if err != nil {
			continue
		}

		if info, err := os.Stat(at); err == nil && info.Mode().IsRegular() {
			return at, nil
		}
	}

	return "", fmt.Errorf("the machine under %s has no %s at %s", h.root, name, strings.Join(places, " or "))
}

// errNoDisk is a path that leads to no disk the kernel has.
var errNoDisk = errors.New("the kernel has no such disk")
