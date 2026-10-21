// Package exporter runs a metrics exporter: a poller asking what the tool reports on, and the metrics page written from what it last heard. It is the
// whole of a tool's print and serve commands but their settings.
package exporter

import (
	"context"
	"errors"
	"io"
	"sync"

	"github.com/katbyte/go-kt/prometheus/metrics"
)

// Poller is what an exporter asks with: a poller.Poller, whatever its targets are.
type Poller interface {
	Run(ctx context.Context)
	PollAll(ctx context.Context) error
}

// Print asks everything once, writes the page to out whatever happened, and then says what failed: a page from a thing that did not answer is still
// worth seeing.
func Print(ctx context.Context, out io.Writer, p Poller, page *metrics.Page) error {
	polled := p.PollAll(ctx)

	return errors.Join(page.Write(out), polled)
}

// Serve asks on schedule and serves the page until the context ends, or until serving stops by itself, on a port already taken say; the asking stops
// with it. Whoever calls it ends the context on a signal.
func Serve(ctx context.Context, listen string, p Poller, page *metrics.Page) error {
	// the asking is told to stop and then waited for, in that order, however serving ends
	var wg sync.WaitGroup
	defer wg.Wait()

	ctx, stop := context.WithCancel(ctx)
	defer stop()

	wg.Go(func() { p.Run(ctx) })

	return metrics.Serve(ctx, listen, page.Handler())
}
