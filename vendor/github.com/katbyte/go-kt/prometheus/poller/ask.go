package poller

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

// Schedule is a set of questions asked of each target together, every so often.
type Schedule[T any] struct {
	p     *Poller[T]
	name  string
	every time.Duration

	mu        sync.Mutex
	questions []func(ctx context.Context, t Target[T]) error
}

// add puts one more question in the schedule.
func (s *Schedule[T]) add(q func(ctx context.Context, t Target[T]) error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.questions = append(s.questions, q)
}

// ask puts every question about one target at once, and gives back what failed, and whether no question had anything it could ask.
func (s *Schedule[T]) ask(ctx context.Context, t Target[T]) (bool, error) {
	s.mu.Lock()
	questions := s.questions
	s.mu.Unlock()

	failed := make([]error, len(questions))

	var wg sync.WaitGroup
	for i, q := range questions {
		wg.Go(func() { failed[i] = q(ctx, t) })
	}
	wg.Wait()

	// a question with nothing it could ask is left out of the round, and a round left with no question at all is no round
	var asked int
	for i, err := range failed {
		if errors.Is(err, ErrNothing) {
			failed[i] = nil
			continue
		}

		asked++
	}

	return asked == 0, errors.Join(failed...)
}

// Answer is the last good answer to one question, and when it was given.
type Answer[V any] struct {
	Value V
	// At is when it was given, and the zero time for a question never answered.
	At time.Time
}

// Known says whether there is an answer to serve.
func (a Answer[V]) Known() bool { return !a.At.IsZero() }

// Kept is where a question's last good answer about each target is kept.
type Kept[V any] struct {
	every time.Duration

	mu      sync.RWMutex
	answers map[string]Answer[V]
}

// For is the last good answer about one target, or none once it is older than three of its schedule's intervals: whatever was asked has stopped
// answering, and what it last said is no longer worth showing as current. A nil Kept, for a schedule a tool left out, has none.
func (k *Kept[V]) For(key string) Answer[V] {
	// a schedule a tool left out has no Kept, and no answer
	if k == nil {
		return Answer[V]{}
	}

	k.mu.RLock()
	a := k.answers[key]
	k.mu.RUnlock()

	if a.Known() && time.Since(a.At) > staleAfter*k.every {
		return Answer[V]{}
	}

	return a
}

// Answer is For the one thing a tool with no list of targets asks about.
func (k *Kept[V]) Answer() Answer[V] {
	return k.For("")
}

// Set keeps an answer about one target as given: for a test's made-up state.
func (k *Kept[V]) Set(key string, a Answer[V]) {
	k.mu.Lock()
	defer k.mu.Unlock()

	k.answers[key] = a
}

// partial is an error that comes with what could be read before it.
type partial struct {
	err error
}

func (p partial) Error() string { return p.err.Error() }

func (p partial) Unwrap() error { return p.err }

// Partial marks an error as coming with an answer worth keeping: what could be read, one phy of many that would not say. The answer is kept and the
// round still fails. Nil is nil.
func Partial(err error) error {
	if err == nil {
		return nil
	}

	return partial{err: err}
}

// Ask adds a question to the schedule, put to each target every round, and is where its last good answer is kept. An answer that comes with an error
// is not kept, unless the error is Partial. What names the question in an error.
func Ask[T, V any](s *Schedule[T], what string, question func(ctx context.Context, target T) (V, error)) *Kept[V] {
	k := &Kept[V]{every: s.every, answers: map[string]Answer[V]{}}

	s.add(func(ctx context.Context, t Target[T]) error {
		v, err := question(ctx, t.Value)
		if _, kept := errors.AsType[partial](err); err == nil || kept {
			k.Set(t.Key, Answer[V]{Value: v, At: time.Now()})
		}

		if err != nil {
			return fmt.Errorf("%s: %w", what, err)
		}

		return nil
	})

	return k
}

// AskOne is Ask for a question that needs no target, in a tool that asks about one thing.
func AskOne[T, V any](s *Schedule[T], what string, question func(ctx context.Context) (V, error)) *Kept[V] {
	return Ask(s, what, func(ctx context.Context, _ T) (V, error) { return question(ctx) })
}
