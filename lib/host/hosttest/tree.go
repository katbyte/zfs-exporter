// Package hosttest lays out a made-up machine's files for a test, from the lines .scripts/capture.sh writes: the real machine data's in
// testdata/data.txt, or a few written by hand.
package hosttest

import (
	"bufio"
	_ "embed"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Data is the lines capture.sh wrote of the machine data, with every serial number and world wide name made up.
//
//go:embed testdata/data.txt
var Data string

// Machine lays the lines out in a new directory and gives back where the machine's / is. A line is a kind, a path from / and a value, with a tab
// between them: l a link and where it points, f a file and its first line, x a file as hex, ! a file that cannot be read, b a device. A line that
// starts with # says what the rest is.
func Machine(t *testing.T, lines string) string {
	t.Helper()

	root := t.TempDir()
	if err := lay(root, lines); err != nil {
		t.Fatal(err)
	}

	return root
}

// lay lays the lines out under root.
func lay(root, lines string) error {
	scanner := bufio.NewScanner(strings.NewReader(lines))
	for number := 1; scanner.Scan(); number++ {
		text := scanner.Text()
		if strings.TrimSpace(text) == "" || strings.HasPrefix(text, "#") {
			continue
		}

		fields := strings.SplitN(text, "\t", 3)
		if len(fields) < 2 {
			return fmt.Errorf("line %d is not a kind and a path: %q", number, text)
		}

		value := ""
		if len(fields) == 3 {
			value = fields[2]
		}

		if err := layOne(filepath.Join(root, fields[1]), fields[0], value); err != nil {
			return fmt.Errorf("line %d: %w", number, err)
		}
	}

	return scanner.Err()
}

// layOne lays out one line's path.
func layOne(at, kind, value string) error {
	if err := os.MkdirAll(filepath.Dir(at), 0o750); err != nil {
		return err
	}

	switch kind {
	case "l":
		return os.Symlink(value, at)
	case "f":
		return os.WriteFile(at, []byte(value+"\n"), 0o600)
	case "x":
		b, err := hex.DecodeString(value)
		if err != nil {
			return err
		}

		return os.WriteFile(at, b, 0o600)
	case "!", "d":
		// a directory where a file should be fails to be read, as a file the kernel refuses to read does
		return os.MkdirAll(at, 0o750)
	case "b", "-":
		return os.WriteFile(at, nil, 0o600)
	}

	return fmt.Errorf("%q is not a kind of line", kind)
}
