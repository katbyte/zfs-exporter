package poller

import (
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"
)

// ParseIntervals reads how often to ask each schedule from a setting written name=duration with commas between them, as --intervals sessions=15s
// is: every schedule in defaults at its default unless the setting names it. A name that is not a schedule, or a length of time that cannot be read
// or is nothing, or a schedule named twice, is an error that says which.
func ParseIntervals(setting string, defaults map[string]time.Duration) (map[string]time.Duration, error) {
	every := maps.Clone(defaults)
	if every == nil {
		every = map[string]time.Duration{}
	}

	given := map[string]bool{}
	for pair := range strings.SplitSeq(setting, ",") {
		if strings.TrimSpace(pair) == "" {
			continue
		}

		name, length, found := strings.Cut(pair, "=")
		name = strings.ToLower(strings.TrimSpace(name))
		if _, known := defaults[name]; !found || !known {
			return nil, fmt.Errorf("intervals are written name=duration with commas between them, a name being one of %s, and not %q", strings.Join(slices.Sorted(maps.Keys(defaults)), ", "), strings.TrimSpace(pair))
		}

		if given[name] {
			return nil, fmt.Errorf("how often %s is asked is given twice", name)
		}
		given[name] = true

		d, err := time.ParseDuration(strings.TrimSpace(length))
		if err != nil || d <= 0 {
			return nil, fmt.Errorf("how often %s is asked is written as 30s or 10m, and not %q", name, strings.TrimSpace(length))
		}

		every[name] = d
	}

	return every, nil
}
