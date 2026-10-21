package command

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/katbyte/zfs-exporter/lib/poll"
)

// stubborn is a program that ends only when the test lets it, killed or not, as a ZFS program stuck on a pool does.
type stubborn struct {
	end       chan struct{}
	once      sync.Once
	killed    chan struct{}
	wasKilled atomic.Bool
}

func (s *stubborn) Wait() error {
	<-s.end
	if s.wasKilled.Load() {
		return errors.New("signal: killed")
	}

	return nil
}

func (s *stubborn) Kill() error {
	s.wasKilled.Store(true)
	select {
	case s.killed <- struct{}{}:
	default:
	}

	return nil
}

func (s *stubborn) let() { s.once.Do(func() { close(s.end) }) }

// programs is a starter of stubborn programs, which counts what it was asked to start.
type programs struct {
	mu      sync.Mutex
	started [][]string
	last    *stubborn
}

func (p *programs) start(argv []string, out, _ io.Writer) (process, error) {
	_, _ = out.Write([]byte("written"))

	p.mu.Lock()
	defer p.mu.Unlock()

	p.started = append(p.started, argv)
	p.last = &stubborn{end: make(chan struct{}), killed: make(chan struct{}, 1)}

	return p.last, nil
}

func (p *programs) count() int {
	p.mu.Lock()
	defer p.mu.Unlock()

	return len(p.started)
}

// A program that ends well gives back what it wrote.
func TestAProgramThatAnswers(t *testing.T) {
	t.Parallel()

	out, err := New(time.Minute).Run(t.Context(), "sh", "/bin/sh", "-c", "echo pools")
	if err != nil || string(out) != "pools\n" {
		t.Errorf("got %q (%v)", out, err)
	}
}

// A program that fails says why, as it wrote it, and is no timeout.
func TestAProgramThatFails(t *testing.T) {
	t.Parallel()

	r := New(time.Minute)
	_, err := r.Run(t.Context(), "sh", "/bin/sh", "-c", "echo 'no pools available' >&2; exit 1")
	if err == nil || !strings.Contains(err.Error(), "no pools available") {
		t.Errorf("got %v, want what it wrote of why", err)
	}

	if got := r.Commands(); len(got) != 1 || got[0] != (poll.Command{Name: "sh"}) {
		t.Errorf("got %+v, want one program with no timeouts", got)
	}
}

// A program that runs past the limit is stopped and counted, with whatever it started, and the next is run as usual.
func TestAProgramThatRunsTooLong(t *testing.T) {
	t.Parallel()

	r := New(100 * time.Millisecond)
	if _, err := r.Run(t.Context(), "sh", "/bin/sh", "-c", "sleep 30; echo late"); err == nil || !strings.Contains(err.Error(), "did not finish") {
		t.Errorf("got %v, want it stopped", err)
	}

	if got := r.Commands(); len(got) != 1 || got[0] != (poll.Command{Name: "sh", Timeouts: 1}) {
		t.Errorf("got %+v, want one timeout and nothing stuck", got)
	}

	if out, err := r.Run(t.Context(), "sh", "/bin/sh", "-c", "echo again"); err != nil || string(out) != "again\n" {
		t.Errorf("the next run gave %q (%v)", out, err)
	}
}

// A program that will not end even when killed is given up on, and nothing more is started until it ends: then ZFS is asked again.
func TestAStuckProgramHoldsBackTheRest(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		p := &programs{}
		r := New(30 * time.Second)
		r.start = p.start

		_, err := r.Run(t.Context(), "zpool", "zpool", "status")
		if err == nil || !strings.Contains(err.Error(), "did not finish within 30s") {
			t.Fatalf("got %v, want it given up on at its limit", err)
		}

		if got := r.Commands(); len(got) != 1 || got[0] != (poll.Command{Name: "zpool", Timeouts: 1, Stuck: true}) {
			t.Errorf("got %+v, want zpool stuck", got)
		}

		time.Sleep(time.Minute)
		_, err = r.Run(t.Context(), "zpool_influxdb", "zpool_influxdb")
		if err == nil || !strings.Contains(err.Error(), "zpool, started 1m32s ago") {
			t.Errorf("got %v, want it not run while zpool is stuck", err)
		}
		if p.count() != 1 {
			t.Errorf("%d programs were started, want only the stuck one", p.count())
		}

		p.last.let()
		synctest.Wait()

		if got := r.Commands(); got[0].Stuck || got[1] != (poll.Command{Name: "zpool_influxdb"}) {
			t.Errorf("got %+v, want nothing stuck once it ended", got)
		}

		go func() {
			synctest.Wait()
			p.last.let()
		}()
		if out, err := r.Run(t.Context(), "zpool_influxdb", "zpool_influxdb"); err != nil || len(out) == 0 {
			t.Errorf("once zpool ended the next gave %q (%v)", out, err)
		}
	})
}

// Two programs asked for at once run one after the other.
func TestOneAtATime(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		p := &programs{}
		r := New(time.Minute)
		r.start = p.start

		var wg sync.WaitGroup
		for _, name := range []string{"zpool", "zpool_influxdb"} {
			wg.Go(func() { _, _ = r.Run(t.Context(), name, name) })
		}

		synctest.Wait()
		if p.count() != 1 {
			t.Errorf("%d programs were running at once", p.count())
		}

		p.last.let()
		synctest.Wait()
		if p.count() != 2 {
			t.Errorf("%d programs were started once the first ended, want the second", p.count())
		}

		p.last.let()
		wg.Wait()
	})
}

// A program stopped because this one is stopping is no timeout.
func TestStoppingIsNoTimeout(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		p := &programs{}
		r := New(time.Minute)
		r.start = p.start

		ctx, cancel := context.WithCancel(t.Context())
		go func() {
			synctest.Wait()
			cancel()
			<-p.last.killed
			p.last.let()
		}()

		if _, err := r.Run(ctx, "zpool", "zpool"); !errors.Is(err, context.Canceled) {
			t.Errorf("got %v, want it stopped with the program", err)
		}
		if got := r.Commands(); got[0].Timeouts != 0 {
			t.Errorf("got %+v, want no timeout", got)
		}
	})
}

// A program is run through what the runner was given first: the machine's loader and its libraries, for a machine mounted elsewhere.
func TestThrough(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		p := &programs{}
		r := New(time.Minute, "/host/lib64/ld-linux-x86-64.so.2", "--library-path", "/host/lib")
		r.start = p.start

		go func() {
			synctest.Wait()
			p.last.let()
		}()
		_, _ = r.Run(t.Context(), "zpool", "/host/usr/sbin/zpool", "status", "-j")

		if got := strings.Join(p.started[0], " "); got != "/host/lib64/ld-linux-x86-64.so.2 --library-path /host/lib /host/usr/sbin/zpool status -j" {
			t.Errorf("ran %s", got)
		}
	})
}
