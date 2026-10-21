package skip_test

import (
	"testing"

	"github.com/katbyte/zfs-exporter/lib/skip"
)

// A list is read whatever its spacing and case, and a name that is no part is refused, naming every part there is.
func TestParse(t *testing.T) {
	t.Parallel()

	s, err := skip.Parse(" Disks , request-sizes,,")
	if err != nil || !s.Has(skip.Disks) || !s.Has(skip.RequestSizes) || s.Has(skip.Pools) {
		t.Errorf("got %v (%v)", s, err)
	}
	if got := s.String(); got != "disks, request-sizes" {
		t.Errorf("it reads back as %q", got)
	}

	if _, err := skip.Parse("disks,histograms"); err == nil {
		t.Error("a name that is no part gave no error")
	}
}

// Naming the only parts to ask about leaves out the rest of each group named, and nothing of a group not named.
func TestAllBut(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		list  string
		shows map[[2]skip.Part]bool
	}{
		{"", map[[2]skip.Part]bool{{skip.Disks, skip.SlowIOs}: true, {skip.Pools, skip.Queues}: true}},
		{"latency", map[[2]skip.Part]bool{{skip.Disks, skip.Latency}: true, {skip.Pools, skip.Latency}: true, {skip.Disks, skip.SlowIOs}: false, {skip.Pools, skip.Poll}: false}},
		{"disks", map[[2]skip.Part]bool{{skip.Disks, skip.Latency}: true, {skip.Disks, skip.SlowIOs}: true, {skip.Pools, skip.Latency}: false, {skip.Vdevs, skip.Operations}: false}},
		{"disks,latency", map[[2]skip.Part]bool{{skip.Disks, skip.Latency}: true, {skip.Disks, skip.SlowIOs}: false, {skip.Vdevs, skip.Latency}: false}},
		{"health,scrub", map[[2]skip.Part]bool{{skip.Disks, skip.Health}: true, {skip.Pools, skip.Scrub}: true, {skip.Pools, skip.Resilver}: false, {skip.Vdevs, skip.Space}: false}},
	} {
		s, err := skip.AllBut(tc.list)
		if err != nil {
			t.Fatal(err)
		}

		for pair, want := range tc.shows {
			if got := s.Shows(pair[0], pair[1]); got != want {
				t.Errorf("--metrics %q shows the %s of the %s: %t, want %t", tc.list, pair[1], pair[0], got, want)
			}
		}
	}
}
