package influx

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"
)

// PointWriter is what collectors write to.
type PointWriter interface {
	Write(ctx context.Context, points []Point) error
}

// Writer encodes points and sends them in batches, retrying transient failures instead of dropping the data the
// way the Python version did.
type Writer struct {
	client    *Client
	log       *slog.Logger
	batchSize int
	attempts  int
	backoff   time.Duration
	sleep     func(context.Context, time.Duration) error

	mu        sync.Mutex
	retention time.Duration
}

// NewWriter returns a writer for client.
func NewWriter(client *Client, log *slog.Logger) *Writer {
	return &Writer{client: client, log: log, batchSize: 5000, attempts: 4, backoff: time.Second, sleep: sleepCtx}
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// Connect pings InfluxDB, retrying while it starts up, then creates the database when it is missing. It gives up
// after attempts tries.
func (w *Writer) Connect(ctx context.Context, attempts int, wait time.Duration) error {
	var err error
	for i := 0; i < attempts; i++ {
		var version string
		if version, err = w.client.Ping(ctx); err == nil {
			w.log.Info("Influxdb version: " + version)
			break
		}
		if i < attempts-1 {
			w.log.Warn(fmt.Sprintf("InfluxDB is not reachable yet (%v). Retrying in %s", err, wait))
			if serr := w.sleep(ctx, wait); serr != nil {
				return serr
			}
		}
	}
	if err != nil {
		return fmt.Errorf("error testing connection to InfluxDB, check your url/hostname: %w", err)
	}
	if err := w.client.EnsureDatabase(ctx); err != nil {
		return fmt.Errorf("could not check or create the %s database: %w", w.client.Database(), err)
	}
	if retention, err := w.client.DefaultRetention(ctx); err == nil {
		w.mu.Lock()
		w.retention = retention
		w.mu.Unlock()
	} else {
		w.log.Debug(fmt.Sprintf("Could not read the retention policy: %v", err))
	}
	return nil
}

// Retention is the default retention policy's duration found by Connect (0: forever or unknown). Backfills use it
// to skip data InfluxDB would reject anyway.
func (w *Writer) Retention() time.Duration {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.retention
}

// Write encodes and sends points. Points the line protocol cannot represent are left out with a warning; the
// rest are still written.
func (w *Writer) Write(ctx context.Context, points []Point) error {
	if len(points) == 0 {
		return nil
	}
	var errs []error
	for start := 0; start < len(points); start += w.batchSize {
		end := min(start+w.batchSize, len(points))
		body, skipped := Encode(points[start:end])
		for _, err := range skipped {
			w.log.Warn(err.Error())
		}
		if len(body) == 0 {
			continue
		}
		if w.log.Enabled(ctx, slog.LevelDebug) {
			// One log entry per point keeps the log one line per entry.
			for _, line := range bytes.Split(bytes.TrimRight(body, "\n"), []byte("\n")) {
				w.log.Debug("Writing Data to InfluxDB " + string(line))
			}
		}
		if err := w.send(ctx, body); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (w *Writer) send(ctx context.Context, body []byte) error {
	delay := w.backoff
	for attempt := 1; ; attempt++ {
		err := w.client.WriteBody(ctx, body)
		if err == nil {
			return nil
		}
		var werr *WriteError
		if errors.As(err, &werr) {
			if werr.BeyondRetention() {
				w.log.Warn("Some points were older than the retention policy and were dropped: " + werr.Body)
				return nil
			}
			if !werr.Retryable() {
				w.log.Error("InfluxDB rejected the write. Dropping this set of data. Error: " + werr.Error())
				return err
			}
		}
		if ctx.Err() != nil || attempt >= w.attempts {
			w.log.Error(fmt.Sprintf("Error writing data to influxdb. Dropping this set of data. "+
				"Check your database! Error: %v", err))
			return err
		}
		w.log.Warn(fmt.Sprintf("Writing to InfluxDB failed (%v). Retrying in %s", err, delay))
		if serr := w.sleep(ctx, delay); serr != nil {
			return err
		}
		delay *= 2
	}
}
