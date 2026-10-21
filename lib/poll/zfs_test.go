package poll_test

import (
	"testing"
	"time"

	"github.com/katbyte/zfs-exporter/lib/poll"
)

// A scan's rate leaves out the time it spent paused, and its time left is estimated as zpool status estimates it: only once it reads at 10MiB a
// second or more and has done some of its work, which for a resilver is rebuilding something.
func TestScanRateAndRemaining(t *testing.T) {
	t.Parallel()

	now := time.Unix(1e9, 0)
	scan := func(function string, issued, processed, passIssued uint64, paused time.Duration) poll.Scan {
		return poll.Scan{Function: function, State: poll.ScanScanning, ToExamine: 1e12, Issued: issued, Processed: processed, PassStart: now.Add(-time.Hour), PassIssued: passIssued, PassPaused: paused}
	}

	for _, tc := range []struct {
		name  string
		scan  poll.Scan
		rate  float64
		left  time.Duration
		known bool
	}{
		{"a scrub under way", scan(poll.ScanScrub, 5e11, 0, 3.6e11, 0), 1e8, 5000 * time.Second, true},
		{"a scrub paused for half its pass", scan(poll.ScanScrub, 5e11, 0, 3.6e11, 30*time.Minute), 2e8, 2500 * time.Second, true},
		{"a scrub reading slowly", scan(poll.ScanScrub, 5e11, 0, 3.6e9, 0), 1e6, 0, false},
		{"a scrub that has read nothing", scan(poll.ScanScrub, 0, 0, 3.6e11, 0), 1e8, 0, false},
		{"a resilver that has rebuilt nothing", scan(poll.ScanResilver, 5e11, 0, 3.6e11, 0), 1e8, 0, false},
		{"a resilver under way", scan(poll.ScanResilver, 5e11, 1, 3.6e11, 0), 1e8, 5000 * time.Second, true},
	} {
		if rate := tc.scan.Rate(now); rate != tc.rate {
			t.Errorf("%s reads at %g bytes a second, want %g", tc.name, rate, tc.rate)
		}
		if left, known := tc.scan.Remaining(now); left != tc.left || known != tc.known {
			t.Errorf("%s has %s left (%t), want %s (%t)", tc.name, left, known, tc.left, tc.known)
		}
	}

	paused := scan(poll.ScanScrub, 5e11, 0, 3.6e11, 0)
	paused.Paused = now.Add(-time.Minute)
	if rate := paused.Rate(now); rate != 0 {
		t.Errorf("a paused scrub reads at %g bytes a second, want 0", rate)
	}
}
