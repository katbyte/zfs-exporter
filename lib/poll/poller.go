package poll

import (
	"context"
	"fmt"
	"time"

	"github.com/katbyte/go-kt/prometheus/poller"
)

// Round is one of the schedules ZFS is asked on.
type Round string

const (
	// RoundStatus is zpool status: each pool's layout, the state of each disk and how many slow requests it has had.
	RoundStatus Round = "status"
	// RoundStats is zpool_influxdb: how many requests each pool, vdev and disk has had, how big they were and how long they took.
	RoundStats Round = "stats"
)

// Intervals is how often each round runs. A round is also given that long to finish, so one never overlaps the next.
type Intervals struct {
	Status time.Duration
	Stats  time.Duration
}

// Answer is the last good answer to one question.
type Answer[T any] struct {
	Value T
	// At is when it was given, and the zero time for a question never answered.
	At time.Time
}

// Known says whether there is an answer to serve.
func (a Answer[T]) Known() bool { return !a.At.IsZero() }

// RoundState is how one round last went.
type RoundState struct {
	// Ran says a round has finished since the start; until one has, the rest says nothing.
	Ran bool
	// OK says the last round was answered in full.
	OK   bool
	Took time.Duration
	// LastSuccess is the zero time until a round succeeds.
	LastSuccess time.Time
	// Failures counts the rounds that failed since the start.
	Failures int
}

// Command is how one of the programs ZFS is asked through has been going.
type Command struct {
	// Name is the program's name: zpool, zpool_influxdb.
	Name string
	// Timeouts counts the times it ran past its time limit and was given up on.
	Timeouts int
	// Stuck says a run that was given up on has not ended yet, so no other is started: ZFS itself is likely stuck.
	Stuck bool
}

// State is what is known of the machine's pools now.
type State struct {
	Status Answer[Status]
	Stats  Answer[Stats]
	// StatsAsked says zpool_influxdb is asked at all: it is not when nothing it says is on the page.
	StatsAsked  bool
	StatusRound RoundState
	StatsRound  RoundState
	Commands    []Command
}

// Up says whether the last round of each schedule was answered in full, and is false until each has run.
func (s *State) Up() bool {
	up := s.StatusRound.Ran && s.StatusRound.OK
	if s.StatsAsked {
		up = up && s.StatsRound.Ran && s.StatsRound.OK
	}

	return up
}

// Ran says a round of each schedule has finished, so whether ZFS is up can be said.
func (s *State) Ran() bool {
	return s.StatusRound.Ran && (!s.StatsAsked || s.StatsRound.Ran)
}

// Poller asks ZFS about the pools on a schedule of its own and keeps the last good answers. go-kt's poller does the asking, the keeping and the
// readings of how it is going; this says what is asked, and gives what was heard as a State for the readings.
type Poller struct {
	inner  *poller.Poller[string]
	zfs    ZFS
	status *poller.Kept[Status]
	stats  *poller.Kept[Stats]
}

// New is a poller of one machine's pools, asking zpool_influxdb as well as zpool status when stats is set. It asks nothing until Run or Poll.
func New(zfs ZFS, every Intervals, stats bool) (*Poller, error) {
	if every.Status <= 0 || (stats && every.Stats <= 0) {
		return nil, fmt.Errorf("the intervals must be longer than nothing: status %s, stats %s", every.Status, every.Stats)
	}

	inner, err := poller.New(poller.Options[string]{Namespace: "zfs", Kind: "pools"})
	if err != nil {
		return nil, err
	}

	p := &Poller{inner: inner, zfs: zfs}
	p.status = poller.AskOne(inner.Schedule(string(RoundStatus), every.Status), "zpool status", zfs.Status)
	if stats {
		p.stats = poller.AskOne(inner.Schedule(string(RoundStats), every.Stats), "zpool_influxdb", zfs.Stats)
	}

	return p, nil
}

// Inner is go-kt's poller underneath: a collector of the readings of how the asking is going, and what exporter.Print and Serve run.
func (p *Poller) Inner() *poller.Poller[string] { return p.inner }

// Run polls until the context ends: every round at once on start, then each on its own interval.
func (p *Poller) Run(ctx context.Context) { p.inner.Run(ctx) }

// Poll runs one round now, and says what failed.
func (p *Poller) Poll(ctx context.Context, round Round) error {
	return p.inner.Poll(ctx, string(round))
}

// State is what is known of the pools now, without asking anything. An answer is left off once three of its round's intervals old, by go-kt's poller.
func (p *Poller) State() State {
	return State{
		Status:      Answer[Status](p.status.Answer()),
		Stats:       Answer[Stats](p.stats.Answer()),
		StatsAsked:  p.stats != nil,
		StatusRound: RoundState(p.inner.Round("", string(RoundStatus))),
		StatsRound:  RoundState(p.inner.Round("", string(RoundStats))),
		Commands:    p.zfs.Commands(),
	}
}
