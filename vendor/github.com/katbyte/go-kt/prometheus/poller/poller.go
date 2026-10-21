// Package poller asks what a tool reports on about its state, on schedules of the tool's own, and keeps the last good answer to each question, so a
// metrics page is written from memory however slow or gone the thing asked is. It says how the asking goes as readings of its own.
package poller

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/katbyte/go-kt/clog"
	"github.com/katbyte/go-kt/outside"
)

// staleAfter is how many of its schedule's intervals an answer is served for once the asking stops working: long enough to ride out a failed round
// or two, short enough that something gone for an hour is not still shown as it was.
const staleAfter = 3

// ErrNothing is what a question gives back when there was nothing it could ask, sensors only root may read say. A round in which every question had
// nothing to ask is counted neither as a success nor as a failure.
var ErrNothing = errors.New("nothing could be asked")

// errGone is why a target the listing no longer names fails its rounds.
var errGone = errors.New("no longer listed")

// errStopping is a listing cut short because the program is stopping.
var errStopping = errors.New("the program is stopping")

// Round is how the last round of one schedule went for one target.
type Round struct {
	// Ran says a round has finished since the start; until one has, the rest says nothing.
	Ran bool
	// OK says every question of the last round was answered.
	OK   bool
	Took time.Duration
	// LastSuccess is the zero time until a round succeeds.
	LastSuccess time.Time
	// Failures counts the rounds that failed since the start.
	Failures int
}

// Target is one thing asked about: its key, what the last listing said of it, whether the latest listing named it, and when it was last named.
type Target[T any] struct {
	Key    string
	Value  T
	Listed bool
	At     time.Time
}

// Options says what is asked about and how it is named.
type Options[T any] struct {
	// Namespace starts the name of each reading the poller writes: "sas" gives sas_poll_duration_seconds.
	Namespace string
	// Label is the label the poller's readings name each target under: "card". Empty for a tool that asks about one thing.
	Label string
	// Kind is what a target is, for the log: "SAS card", or "NAS" for a tool that asks about one.
	Kind string
	// Targets lists the things to ask about, at the start of each round. Fixed is a list that never changes; nil is one thing, with the key "".
	Targets func(ctx context.Context) ([]T, error)
	// Key tells targets apart: a card's name in the kernel. A target that is a string is its own key.
	Key func(T) string
	// Names is what each target is called on the page and in the log, given every target ever seen, gone ones too. Nil calls each by its key.
	Names func(targets map[string]Target[T]) map[string]string
}

// Fixed is a list of targets that never changes: the servers the settings name, say.
func Fixed(names ...string) func(context.Context) ([]string, error) {
	return func(context.Context) ([]string, error) { return names, nil }
}

// place is one schedule's rounds for one target.
type place struct {
	key, schedule string
}

// Poller asks about each target on each schedule, and remembers the last good answer to each question and how each round went.
type Poller[T any] struct {
	o        Options[T]
	key      func(T) string
	readings readings

	mu        sync.RWMutex
	schedules []*Schedule[T]
	targets   map[string]Target[T]
	rounds    map[place]Round
	// answered is the targets whose first answer has been logged
	answered map[string]bool
	// looked, found and unlisted say how the last listing went, so it is logged when that changes and not every round
	looked, found, unlisted bool
}

// New is a poller that asks nothing until Run or Poll, and has no schedules until Schedule adds them.
func New[T any](o Options[T]) (*Poller[T], error) {
	key := o.Key
	if key == nil {
		if _, isString := any(*new(T)).(string); o.Targets != nil && !isString {
			return nil, errors.New("targets that are not strings need a Key to tell them apart")
		}

		key = func(v T) string { return fmt.Sprint(v) }
	}

	p := &Poller[T]{o: o, key: key, readings: newReadings(o.Namespace, o.Label), targets: map[string]Target[T]{}, rounds: map[place]Round{}, answered: map[string]bool{}}
	if o.Targets == nil {
		p.targets[""] = Target[T]{Listed: true}
	}

	return p, nil
}

// Schedule adds a schedule: questions asked together every so often, which is also how long a round is given to finish. Ask adds its questions,
// before the poller runs. An interval of nothing or a name used twice is a mistake in the tool, and panics; ParseIntervals refuses one in a setting.
func (p *Poller[T]) Schedule(name string, every time.Duration) *Schedule[T] {
	p.mu.Lock()
	defer p.mu.Unlock()

	if every <= 0 || slices.ContainsFunc(p.schedules, func(s *Schedule[T]) bool { return s.name == name }) {
		panic(fmt.Sprintf("poller: the %s schedule is added twice or with an interval of nothing (%s)", name, every))
	}

	s := &Schedule[T]{p: p, name: name, every: every}
	p.schedules = append(p.schedules, s)

	return s
}

// scheduled is every schedule, in the order they were added.
func (p *Poller[T]) scheduled() []*Schedule[T] {
	p.mu.RLock()
	defer p.mu.RUnlock()

	return slices.Clone(p.schedules)
}

// Run polls until the context ends: every schedule at once on start, then each on its own interval.
func (p *Poller[T]) Run(ctx context.Context) {
	var wg sync.WaitGroup
	for _, s := range p.scheduled() {
		wg.Go(func() { p.loop(ctx, s) })
	}
	wg.Wait()
}

// loop polls one schedule now and then every interval, until the context ends.
func (p *Poller[T]) loop(ctx context.Context, s *Schedule[T]) {
	ticker := time.NewTicker(s.every)
	defer ticker.Stop()

	for {
		_ = p.pollSchedule(ctx, s)

		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// PollAll polls every schedule once, all at once, and says which rounds failed, target by target: for a tool that prints its page once. Why each
// failed is in the log, one line each, so it is not said twice.
func (p *Poller[T]) PollAll(ctx context.Context) error {
	schedules := p.scheduled()
	failed := make([]error, len(schedules))

	var wg sync.WaitGroup
	for i, s := range schedules {
		wg.Go(func() { failed[i] = p.pollSchedule(ctx, s) })
	}
	wg.Wait()

	return summed(errors.Join(failed...))
}

// roundError is one target's round of one schedule that failed, and why.
type roundError struct {
	who, schedule string
	err           error
}

func (f *roundError) Error() string {
	if f.who == "" {
		return "the " + f.schedule + " round: " + f.err.Error()
	}

	return f.who + ": the " + f.schedule + " round: " + f.err.Error()
}

func (f *roundError) Unwrap() error { return f.err }

// summaryError is which rounds failed, target by target, with the whole of why behind it for errors.Is and errors.As.
type summaryError struct {
	text string
	err  error
}

func (s summaryError) Error() string { return s.text }

func (s summaryError) Unwrap() error { return s.err }

// summed is the failed rounds in err as a few words for each target: "the NAS: the system, pools, disks and 15 other rounds failed".
func summed(err error) error {
	if err == nil {
		return nil
	}

	var order []string
	schedules := map[string][]string{}
	for _, f := range rounds(err) {
		if _, seen := schedules[f.who]; !seen {
			order = append(order, f.who)
		}
		schedules[f.who] = append(schedules[f.who], f.schedule)
	}

	if len(order) == 0 {
		return err
	}

	said := make([]string, 0, len(order))
	for _, who := range order {
		line := listed(schedules[who]) + " failed"
		if who != "" {
			line = who + ": " + line
		}
		said = append(said, line)
	}

	return summaryError{text: strings.Join(said, "; ") + "; the log says why", err: err}
}

// rounds is every failed round in err, in the order joined.
func rounds(err error) []*roundError {
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		out := make([]*roundError, 0, len(joined.Unwrap()))
		for _, e := range joined.Unwrap() {
			out = append(out, rounds(e)...)
		}

		return out
	}

	if r, ok := errors.AsType[*roundError](err); ok {
		return []*roundError{r}
	}

	return nil
}

// listed is schedules as a few words: "the links round", "the system and pools rounds", "the system, pools, disks and 15 other rounds".
func listed(names []string) string {
	switch {
	case len(names) == 1:
		return "the " + names[0] + " round"
	case len(names) <= 3:
		return "the " + strings.Join(names[:len(names)-1], ", ") + " and " + names[len(names)-1] + " rounds"
	}

	return fmt.Sprintf("the %s and %d other rounds", strings.Join(names[:3], ", "), len(names)-3)
}

// Poll polls one schedule once, and says what failed and why: for a test of what that schedule asks.
func (p *Poller[T]) Poll(ctx context.Context, schedule string) error {
	for _, s := range p.scheduled() {
		if s.name == schedule {
			return p.pollSchedule(ctx, s)
		}
	}

	return fmt.Errorf("there is no %s schedule", schedule)
}

// pollSchedule runs one round of a schedule: the targets are listed, then each is asked at the same time, so one that is slow holds up no other.
func (p *Poller[T]) pollSchedule(ctx context.Context, s *Schedule[T]) error {
	err := p.list(ctx, s)
	// a round cut short because the program is stopping says nothing about the targets
	if errors.Is(err, errStopping) {
		return nil
	}

	if err != nil {
		// with no saying which targets there are, the round has failed for every one known
		keys := p.Targets()
		failed := make([]error, 0, len(keys))
		for _, key := range keys {
			failed = append(failed, p.note(s, key, 0, err))
		}

		return errors.Join(failed...)
	}

	keys := p.Targets()
	failed := make([]error, len(keys))

	var wg sync.WaitGroup
	for i, key := range keys {
		wg.Go(func() { failed[i] = p.pollTarget(ctx, s, key) })
	}
	wg.Wait()

	return errors.Join(failed...)
}

// list asks which targets there are, and keeps what it says of each. A tool with one target has nothing to list.
func (p *Poller[T]) list(ctx context.Context, s *Schedule[T]) error {
	if p.o.Targets == nil {
		return nil
	}

	within, cancel := context.WithTimeout(ctx, s.every)
	defer cancel()

	values, err := p.o.Targets(within)
	if err != nil && ctx.Err() != nil {
		return errStopping
	}

	p.mu.Lock()
	failedBefore := p.unlisted
	p.unlisted = err != nil
	p.mu.Unlock()

	switch {
	case err != nil && !failedBefore:
		clog.Log.Warnf("listing the %s failed, and is tried again each round: %s", p.kinds(), said(err))
	case err != nil:
		clog.Log.Debugf("listing the %s failed again: %s", p.kinds(), said(err))
	case failedBefore:
		clog.Log.Infof("listing the %s works again", p.kinds())
	}

	if err != nil {
		return fmt.Errorf("listing: %w", err)
	}

	p.see(values)

	return nil
}

// see keeps what the listing said of each target it named, and marks the ones it no longer names.
func (p *Poller[T]) see(values []T) {
	now := time.Now()

	p.mu.Lock()
	for key, t := range p.targets {
		t.Listed = false
		p.targets[key] = t
	}

	var fresh []string
	for _, v := range values {
		key := p.key(v)
		if _, known := p.targets[key]; !known {
			fresh = append(fresh, key)
		}

		p.targets[key] = Target[T]{Key: key, Value: v, Listed: true, At: now}
	}

	found := len(values) > 0
	changed := !p.looked || p.found != found
	p.looked, p.found = true, found
	p.mu.Unlock()

	names := p.Names()
	for _, key := range fresh {
		clog.Log.Infof("reporting on %s", p.who(key, names))
	}

	// a listing that names nothing looks the same every round, so it is said once and again when that changes
	if changed && !found {
		clog.Log.Warnf("no %s is listed, and the listing is asked again each round", p.kind())
	}
}

// pollTarget asks every question of one schedule about one target, and records how it went. A target the listing no longer names has failed the round
// without being asked anything.
func (p *Poller[T]) pollTarget(ctx context.Context, s *Schedule[T], key string) error {
	t := p.Target(key)
	if !t.Listed {
		return p.note(s, key, 0, errGone)
	}

	within, cancel := context.WithTimeout(ctx, s.every)
	defer cancel()

	start := time.Now()
	nothing, err := s.ask(within, t)
	// a round cut short because the program is stopping says nothing about the target, and neither does one that had nothing it could ask
	if nothing || (err != nil && ctx.Err() != nil) {
		return nil
	}

	return p.note(s, key, time.Since(start), err)
}

// note records how a round went for one target, logs it when that is a change, and gives back the error naming the target and the schedule.
func (p *Poller[T]) note(s *Schedule[T], key string, took time.Duration, err error) error {
	p.mu.Lock()
	r := p.rounds[place{key, s.name}]
	before := r
	r.Ran, r.OK, r.Took = true, err == nil, took
	if err != nil {
		r.Failures++
	} else {
		r.LastSuccess = time.Now()
	}
	p.rounds[place{key, s.name}] = r

	first := err == nil && !p.answered[key]
	if first {
		p.answered[key] = true
	}
	p.mu.Unlock()

	who := p.who(key, p.Names())
	lead := ""
	if who != "" {
		lead = who + ": "
	}

	switch {
	case err != nil && (before.OK || !before.Ran):
		clog.Log.Warnf("%sthe %s round failed, and will be tried again every %s: %s", lead, s.name, s.every, said(err))
	case err != nil:
		clog.Log.Debugf("%sthe %s round failed again: %s", lead, s.name, said(err))
	case before.Ran && !before.OK:
		clog.Log.Infof("%sthe %s round is answering again", lead, s.name)
	case first && who != "":
		clog.Log.Infof("%s answered", who)
	case first:
		clog.Log.Infof("the first round was answered")
	}

	if err != nil {
		return &roundError{who: who, schedule: s.name, err: err}
	}

	return nil
}

// Names is what each target is called on the page, by its key: for the label on a tool's own readings, the same as on the poller's.
func (p *Poller[T]) Names() map[string]string {
	p.mu.RLock()
	targets := maps.Clone(p.targets)
	p.mu.RUnlock()

	names := map[string]string{}
	if p.o.Names != nil {
		names = p.o.Names(targets)
	}

	for key := range targets {
		if names[key] == "" {
			names[key] = key
		}
	}

	return names
}

// who is a target as the log and errors name it: "card 9500-16i (host1)", or "the NAS" for a tool that asks about one thing.
func (p *Poller[T]) who(key string, names map[string]string) string {
	if p.o.Targets == nil {
		if p.o.Kind == "" {
			return ""
		}

		return "the " + p.o.Kind
	}

	lead := p.o.Label
	if lead == "" {
		lead = p.o.Kind
	}

	who := strings.TrimSpace(lead + " " + names[key])
	if names[key] != key {
		who += " (" + key + ")"
	}

	return who
}

// kind is what a target is, for the log.
func (p *Poller[T]) kind() string {
	if p.o.Kind == "" {
		return "target"
	}

	return p.o.Kind
}

// kinds is kind for more than one.
func (p *Poller[T]) kinds() string {
	return p.kind() + "s"
}

// said is what went wrong as one line of a log: a round's failures come one to a line, and a server's own words for one may quote an address with a
// key on it.
func said(err error) string {
	return strings.ReplaceAll(outside.BlankAddresses(err.Error()), "\n", "; ")
}

// Targets is the key of every target seen since the start, gone ones too, in order.
func (p *Poller[T]) Targets() []string {
	p.mu.RLock()
	defer p.mu.RUnlock()

	return slices.Sorted(maps.Keys(p.targets))
}

// Target is what is known of one target, and the zero Target for one never seen.
func (p *Poller[T]) Target(key string) Target[T] {
	p.mu.RLock()
	defer p.mu.RUnlock()

	return p.targets[key]
}

// SetTarget makes a target known as given, Key included: for a test's made-up state.
func (p *Poller[T]) SetTarget(t Target[T]) {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.targets[t.Key] = t
}

// Round is how the last round of one schedule went for one target.
func (p *Poller[T]) Round(key, schedule string) Round {
	p.mu.RLock()
	defer p.mu.RUnlock()

	return p.rounds[place{key, schedule}]
}

// SetRound records a round of one schedule for one target as given, Ran included: for a test's made-up state.
func (p *Poller[T]) SetRound(key, schedule string, r Round) {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.rounds[place{key, schedule}] = r
}
