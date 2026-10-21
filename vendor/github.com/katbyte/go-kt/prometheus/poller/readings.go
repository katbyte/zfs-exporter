package poller

import (
	"github.com/prometheus/client_golang/prometheus"

	"github.com/katbyte/go-kt/prometheus/metrics"
)

// readings is what the poller says of its own asking, for each target and schedule.
type readings struct {
	list                                    *metrics.List
	interval, duration, failures, succeeded *prometheus.Desc
}

func newReadings(namespace, label string) readings {
	var labels []string
	if label != "" {
		labels = []string{label}
	}

	list := metrics.NewList(namespace, labels...)

	return readings{
		list:      list,
		interval:  list.Reading("poll_interval_seconds", "How often each schedule is asked.", "poll"),
		duration:  list.Reading("poll_duration_seconds", "How long the last round of each schedule took.", "poll"),
		failures:  list.Reading("poll_failures_total", "How many rounds of each schedule have failed since the tool started.", "poll"),
		succeeded: list.Reading("poll_last_success_timestamp_seconds", "When each schedule was last answered in full, in seconds since 1970.", "poll"),
	}
}

// Describe names the readings the poller writes of its own asking.
func (p *Poller[T]) Describe(ch chan<- *prometheus.Desc) {
	p.readings.list.Describe(ch)
}

// Collect writes how the asking is going: for each target and schedule that has finished a round, how often it is asked, how long the last round
// took, how many have failed, and when one last succeeded. A schedule that has not finished a round has none, so a tool just started does not show a
// round that took nothing or a success in 1970.
func (p *Poller[T]) Collect(ch chan<- prometheus.Metric) {
	names := p.Names()

	for _, key := range p.Targets() {
		var w *metrics.Writer
		if p.o.Label != "" {
			w = metrics.NewWriter(ch, names[key])
		} else {
			w = metrics.NewWriter(ch)
		}

		for _, s := range p.scheduled() {
			r := p.Round(key, s.name)
			if !r.Ran {
				continue
			}

			w.Gauge(p.readings.interval, s.every.Seconds(), s.name)
			w.Gauge(p.readings.duration, r.Took.Seconds(), s.name)
			w.Total(p.readings.failures, r.Failures, s.name)
			w.At(p.readings.succeeded, r.LastSuccess, s.name)
		}
	}
}
