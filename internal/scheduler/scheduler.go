// Package scheduler runs the collectors' jobs: every job once at startup and then on its interval, like the
// Python schedule loop, but a job never overlaps itself, a panic never takes the process down, and shutdown waits
// for running jobs.
package scheduler

import (
	"context"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"runtime/debug"
	"sync"
	"time"

	"github.com/Nexxed/Varken/internal/collector"
)

// Clock is the time source (replaced in tests).
type Clock interface {
	Now() time.Time
	After(d time.Duration) <-chan time.Time
}

type realClock struct{}

func (realClock) Now() time.Time                         { return time.Now() }
func (realClock) After(d time.Duration) <-chan time.Time { return time.After(d) }

// Scheduler runs jobs.
type Scheduler struct {
	log   *slog.Logger
	clock Clock
	jobs  []entry
	wg    sync.WaitGroup
	// ShutdownTimeout bounds how long Run waits for running jobs after its context ends.
	ShutdownTimeout time.Duration
}

type entry struct {
	job      collector.Job
	min, max time.Duration // a random interval in [min, max] when max > 0
}

// New returns a scheduler.
func New(log *slog.Logger, clock Clock) *Scheduler {
	if clock == nil {
		clock = realClock{}
	}
	return &Scheduler{log: log, clock: clock, ShutdownTimeout: 30 * time.Second}
}

// Add registers a job. A recurring job with a zero or negative interval is rejected with an error in the log:
// Python's schedule ran it on every pass of its one-second loop.
func (s *Scheduler) Add(job collector.Job) {
	if !job.Once && job.Interval <= 0 {
		s.log.Error(fmt.Sprintf("%s has an interval of %s; it must be at least one second. Job not scheduled.",
			job.Name, job.Interval))
		return
	}
	s.jobs = append(s.jobs, entry{job: job})
}

// AddRandom registers a job whose interval is drawn between min and max before each wait
// (Python's schedule.every(12).to(24).hours).
func (s *Scheduler) AddRandom(name string, min, max time.Duration, run func(context.Context) error) {
	s.jobs = append(s.jobs, entry{job: collector.Job{Name: name, Interval: min, Run: run}, min: min, max: max})
}

// Len is the number of registered jobs.
func (s *Scheduler) Len() int { return len(s.jobs) }

// Names lists the registered job names.
func (s *Scheduler) Names() []string {
	out := make([]string, len(s.jobs))
	for i, e := range s.jobs {
		out[i] = e.job.Name
	}
	return out
}

// Run starts every job (once-only jobs first), then keeps the recurring ones on their intervals until ctx ends.
// It then waits up to ShutdownTimeout for running jobs.
func (s *Scheduler) Run(ctx context.Context) {
	for _, e := range s.jobs {
		if e.job.Once {
			s.wg.Add(1)
			go func(j collector.Job) {
				defer s.wg.Done()
				s.runOnce(ctx, j)
			}(e.job)
		}
	}
	for _, e := range s.jobs {
		if !e.job.Once {
			s.wg.Add(1)
			go func(e entry) {
				defer s.wg.Done()
				s.loop(ctx, e)
			}(e)
		}
	}
	<-ctx.Done()
	done := make(chan struct{})
	go func() {
		s.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(s.ShutdownTimeout):
		s.log.Warn("Some jobs were still running at shutdown")
	}
}

func (s *Scheduler) interval(e entry) time.Duration {
	if e.max > e.min {
		return e.min + time.Duration(rand.Int64N(int64(e.max-e.min)+1))
	}
	return e.job.Interval
}

func (s *Scheduler) loop(ctx context.Context, e entry) {
	for {
		start := s.clock.Now()
		s.runOnce(ctx, e.job)
		interval := s.interval(e)
		next := start.Add(interval)
		now := s.clock.Now()
		if !next.After(now) {
			missed := int(now.Sub(next)/interval) + 1
			s.log.Debug(fmt.Sprintf("%s took %s, longer than its %s interval; skipping %d run(s)",
				e.job.Name, now.Sub(start).Round(time.Millisecond), interval, missed))
			next = next.Add(time.Duration(missed) * interval)
		}
		select {
		case <-ctx.Done():
			return
		case <-s.clock.After(next.Sub(now)):
		}
	}
}

func (s *Scheduler) runOnce(ctx context.Context, job collector.Job) {
	if ctx.Err() != nil {
		return
	}
	defer func() {
		if r := recover(); r != nil {
			s.log.Error(fmt.Sprintf("Job %s crashed: %v\n%s", job.Name, r, debug.Stack()))
		}
	}()
	if err := job.Run(ctx); err != nil && ctx.Err() == nil {
		s.log.Debug(fmt.Sprintf("Job %s: %v", job.Name, err))
	}
}
