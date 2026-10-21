// Package command runs the machine's ZFS programs one at a time, each within a time limit. A ZFS program waits on a lock of each pool it reads, and
// one waiting on a pool that is stuck can wait for good and not even stop when killed, so one that runs past its limit is given up on, and no other
// is started until it has ended.
package command

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/katbyte/go-kt/clog"
	"github.com/katbyte/zfs-exporter/lib/poll"
)

// grace is how long a program that was killed is given to end before it is taken to be stuck.
const grace = 2 * time.Second

// env is all a program is given of the environment: a machine with no /dev/zfs says so at once, rather than ZFS waiting ten seconds for one to
// appear.
var env = []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin", "LC_ALL=C", "ZFS_MODULE_TIMEOUT=0"}

// process is a program that has been started.
type process interface {
	// Wait waits for it to end, and says how it ended.
	Wait() error
	Kill() error
}

// starter starts a program, writing what it says to out and errOut.
type starter func(argv []string, out, errOut io.Writer) (process, error)

// Runner runs programs one at a time.
type Runner struct {
	limit time.Duration
	// through is what each program is run through: the machine's own loader and where its libraries are, for a machine whose / is mounted
	// somewhere else. Empty runs each as it is.
	through []string
	start   starter
	turn    chan struct{}

	mu       sync.Mutex
	names    []string
	timeouts map[string]int
	// stuck is the program given up on that has not ended, and when it was started
	stuck      string
	stuckSince time.Time
}

// New is a runner that gives each program limit to finish, running it through those arguments first when there are any.
func New(limit time.Duration, through ...string) *Runner {
	return &Runner{limit: limit, through: through, start: startProgram, turn: make(chan struct{}, 1), timeouts: map[string]int{}}
}

// startProgram starts a program on this machine, in a group of its own so that killing it kills anything it started too.
func startProgram(argv []string, out, errOut io.Writer) (process, error) {
	//nolint:gosec,noctx // G204: the programs are ZFS's own, found on the machine; Run kills it, and goes on without it if it will not end, where
	// CommandContext would wait for it
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Env, cmd.Stdout, cmd.Stderr = env, out, errOut
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	// once the program has ended, what it started is not waited for to stop writing
	cmd.WaitDelay = grace / 2
	if err := cmd.Start(); err != nil {
		return nil, err
	}

	return running{cmd}, nil
}

// running is a program started on this machine.
type running struct {
	cmd *exec.Cmd
}

func (r running) Wait() error { return r.cmd.Wait() }

func (r running) Kill() error { return syscall.Kill(-r.cmd.Process.Pid, syscall.SIGKILL) }

// Run runs a program, after the one before it has ended, and gives back what it wrote. Name is what it is called in errors and on the page. A program
// that fails says what it wrote of why; one that runs past the limit is killed and counted, and one that will not end even then stops any other being
// run until it does.
func (r *Runner) Run(ctx context.Context, name, program string, args ...string) ([]byte, error) {
	select {
	case r.turn <- struct{}{}:
	case <-ctx.Done():
		return nil, fmt.Errorf("%s waited for the program before it to end: %w", name, ctx.Err())
	}
	defer func() { <-r.turn }()

	r.mu.Lock()
	if !slices.Contains(r.names, name) {
		r.names = append(r.names, name)
	}
	stuck, since := r.stuck, r.stuckSince
	r.mu.Unlock()

	if stuck != "" {
		return nil, fmt.Errorf("%s is not run: %s, started %s ago, ran past its time limit and has still not ended, which is ZFS stuck; nothing more is run until it ends", name, stuck, time.Since(since).Round(time.Second))
	}

	var out, errOut bytes.Buffer
	started := time.Now()
	p, err := r.start(slices.Concat(r.through, []string{program}, args), &out, &limited{w: &errOut, n: 4096})
	if err != nil {
		return nil, fmt.Errorf("starting %s: %w", name, err)
	}

	done := make(chan error, 1)
	go func() { done <- p.Wait() }()

	within, cancel := context.WithTimeout(ctx, r.limit)
	defer cancel()

	select {
	case err := <-done:
		if err != nil {
			return nil, fmt.Errorf("%s: %w%s", name, err, said(errOut.String()))
		}

		return out.Bytes(), nil
	case <-within.Done():
	}

	took := time.Since(started)
	_ = p.Kill()
	select {
	case <-done:
	case <-time.After(grace):
		r.stick(name, started, done)
	}

	// a program stopped because this one is stopping took no longer than it should
	if ctx.Err() != nil && !errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return nil, fmt.Errorf("%s: %w", name, ctx.Err())
	}

	r.mu.Lock()
	r.timeouts[name]++
	r.mu.Unlock()

	return nil, fmt.Errorf("%s did not finish within %s, and was stopped", name, took.Round(100*time.Millisecond))
}

// stick notes a program that would not end when killed, and waits apart for it to end, after which others may run again.
func (r *Runner) stick(name string, started time.Time, done <-chan error) {
	r.mu.Lock()
	r.stuck, r.stuckSince = name, started
	r.mu.Unlock()

	clog.Log.Warnf("%s did not end when killed, which is ZFS stuck; nothing more is run until it ends", name)

	go func() {
		<-done

		r.mu.Lock()
		r.stuck, r.stuckSince = "", time.Time{}
		r.mu.Unlock()

		clog.Log.Infof("%s has ended, %s after it was started, and ZFS is asked again", name, time.Since(started).Round(time.Second))
	}()
}

// Commands is how each program run so far has been going, in the order first run.
func (r *Runner) Commands() []poll.Command {
	r.mu.Lock()
	defer r.mu.Unlock()

	out := make([]poll.Command, 0, len(r.names))
	for _, name := range r.names {
		out = append(out, poll.Command{Name: name, Timeouts: r.timeouts[name], Stuck: r.stuck == name})
	}

	return out
}

// said is what a program wrote of why it failed, on one line, as the end of an error.
func said(errOut string) string {
	lines := strings.Fields(strings.Join(strings.Split(strings.TrimSpace(errOut), "\n"), " "))
	if len(lines) == 0 {
		return ""
	}

	return ": " + strings.Join(lines, " ")
}

// limited writes up to n bytes and drops the rest, so a program that writes without end of why it failed cannot fill the memory.
type limited struct {
	w io.Writer
	n int
}

func (l *limited) Write(p []byte) (int, error) {
	if l.n <= 0 {
		return len(p), nil
	}

	keep := p
	if len(keep) > l.n {
		keep = keep[:l.n]
	}
	l.n -= len(keep)

	if _, err := l.w.Write(keep); err != nil {
		return 0, err
	}

	return len(p), nil
}
