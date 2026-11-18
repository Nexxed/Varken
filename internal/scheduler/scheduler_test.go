package scheduler

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Nexxed/Varken/internal/collector"
)

// fakeClock advances only when told to.
type fakeClock struct {
	mu      sync.Mutex
	now     time.Time
	waiters []waiter
}

type waiter struct {
	at time.Time
	ch chan time.Time
}

func newFakeClock() *fakeClock { return &fakeClock{now: time.Date(2026, 11, 18, 0, 0, 0, 0, time.UTC)} }

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) After(d time.Duration) <-chan time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	ch := make(chan time.Time, 1)
	c.waiters = append(c.waiters, waiter{c.now.Add(d), ch})
	return ch
}

// Advance moves the clock and fires due timers.
func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	var keep []waiter
	for _, w := range c.waiters {
		if !w.at.After(c.now) {
			w.ch <- c.now
		} else {
			keep = append(keep, w)
		}
	}
	c.waiters = keep
	c.mu.Unlock()
}

func (c *fakeClock) pending() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.waiters)
}

// waitFor polls a condition; the scheduler's goroutines run on real time.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(time.Millisecond)
	}
}

func quietLog(buf *bytes.Buffer) *slog.Logger {
	return slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

func TestRunsOnStartThenOnInterval(t *testing.T) {
	clock := newFakeClock()
	var buf bytes.Buffer
	s := New(quietLog(&buf), clock)
	var runs, once atomic.Int32
	s.Add(collector.Every("sonarr-1-get_queue", 30, func(context.Context) error { runs.Add(1); return nil }))
	s.Add(collector.Once("sonarr-1-history_events_backfill", func(context.Context) error { once.Add(1); return nil }))
	s.Add(collector.Every("bad", 0, func(context.Context) error { return nil }))
	if s.Len() != 2 || !strings.Contains(buf.String(), "bad has an interval of 0s") {
		t.Fatalf("jobs = %v, log = %s", s.Names(), buf.String())
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { s.Run(ctx); close(done) }()

	waitFor(t, "startup run", func() bool { return runs.Load() == 1 && once.Load() == 1 && clock.pending() == 1 })
	clock.Advance(29 * time.Second)
	time.Sleep(5 * time.Millisecond)
	if runs.Load() != 1 {
		t.Fatal("ran before its interval")
	}
	clock.Advance(time.Second)
	waitFor(t, "second run", func() bool { return runs.Load() == 2 && clock.pending() == 1 })
	clock.Advance(30 * time.Second)
	waitFor(t, "third run", func() bool { return runs.Load() == 3 })
	cancel()
	<-done
	if once.Load() != 1 {
		t.Error("once job must run exactly once")
	}
}

func TestSlowJobDoesNotOverlap(t *testing.T) {
	clock := newFakeClock()
	var buf bytes.Buffer
	s := New(quietLog(&buf), clock)
	var running, maxRunning, runs atomic.Int32
	s.Add(collector.Every("slow", 10, func(context.Context) error {
		n := running.Add(1)
		if n > maxRunning.Load() {
			maxRunning.Store(n)
		}
		runs.Add(1)
		clock.Advance(25 * time.Second) // the job itself takes 25s of a 10s interval
		running.Add(-1)
		return errors.New("upstream down")
	}))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { s.Run(ctx); close(done) }()
	waitFor(t, "first run", func() bool { return runs.Load() == 1 && clock.pending() == 1 })
	// Started at 0, ended at 25: the 10s and 20s ticks are skipped and the next run is at 30s.
	clock.Advance(4 * time.Second)
	time.Sleep(5 * time.Millisecond)
	if runs.Load() != 1 {
		t.Fatal("ran too early")
	}
	clock.Advance(time.Second)
	waitFor(t, "second run", func() bool { return runs.Load() == 2 })
	cancel()
	<-done
	if maxRunning.Load() != 1 {
		t.Error("a job overlapped itself")
	}
	if !strings.Contains(buf.String(), "skipping 2 run(s)") || !strings.Contains(buf.String(), "upstream down") {
		t.Errorf("log = %s", buf.String())
	}
}

func TestPanicIsRecovered(t *testing.T) {
	clock := newFakeClock()
	var buf bytes.Buffer
	s := New(quietLog(&buf), clock)
	var runs atomic.Int32
	s.Add(collector.Every("boom", 5, func(context.Context) error {
		runs.Add(1)
		panic("nil map")
	}))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { s.Run(ctx); close(done) }()
	waitFor(t, "first run", func() bool { return runs.Load() == 1 && clock.pending() == 1 })
	clock.Advance(5 * time.Second)
	waitFor(t, "second run after the panic", func() bool { return runs.Load() == 2 })
	cancel()
	<-done
	if !strings.Contains(buf.String(), "Job boom crashed: nil map") {
		t.Errorf("log = %s", buf.String())
	}
}

func TestShutdownWaitsForRunningJobs(t *testing.T) {
	s := New(slog.New(slog.DiscardHandler), nil)
	release := make(chan struct{})
	var finished atomic.Bool
	started := make(chan struct{})
	s.Add(collector.Once("long", func(ctx context.Context) error {
		close(started)
		<-release
		finished.Store(true)
		return nil
	}))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { s.Run(ctx); close(done) }()
	<-started
	cancel()
	select {
	case <-done:
		t.Fatal("Run returned while a job was running")
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	<-done
	if !finished.Load() {
		t.Error("job did not finish")
	}

	// A job that ignores cancellation is abandoned after the timeout.
	var buf bytes.Buffer
	s = New(quietLog(&buf), nil)
	s.ShutdownTimeout = 10 * time.Millisecond
	block := make(chan struct{})
	defer close(block)
	started2 := make(chan struct{})
	s.Add(collector.Once("stuck", func(context.Context) error { close(started2); <-block; return nil }))
	ctx, cancel = context.WithCancel(context.Background())
	go func() { <-started2; cancel() }()
	s.Run(ctx)
	if !strings.Contains(buf.String(), "still running at shutdown") {
		t.Errorf("log = %s", buf.String())
	}
}

func TestRandomInterval(t *testing.T) {
	s := New(slog.New(slog.DiscardHandler), newFakeClock())
	s.AddRandom("geoip-update", 12*time.Hour, 24*time.Hour, func(context.Context) error { return nil })
	seen := map[time.Duration]bool{}
	for i := 0; i < 200; i++ {
		d := s.interval(s.jobs[0])
		if d < 12*time.Hour || d > 24*time.Hour {
			t.Fatalf("interval %s out of range", d)
		}
		seen[d] = true
	}
	if len(seen) < 100 {
		t.Error("interval should vary")
	}
	names := s.Names()
	sort.Strings(names)
	if names[0] != "geoip-update" {
		t.Error(names)
	}
	var rc realClock
	if rc.Now().IsZero() || rc.After(0) == nil {
		t.Error("real clock")
	}
}
