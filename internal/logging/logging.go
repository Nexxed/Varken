// Package logging writes Varken's log in the format it has always used
// ("2006-01-02 15:04:05 : LEVEL : module : message") to the console and to logs/varken.log, rotated at 5 MB with
// five backups, with secrets masked.
package logging

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// LevelCritical is Python's CRITICAL.
const LevelCritical = slog.Level(12)

const (
	// FileName, MaxSize and Backups are the Python RotatingFileHandler settings.
	FileName  = "varken.log"
	MaxSize   = 5_000_000
	Backups   = 5
	LogFolder = "logs"
)

// Redactor masks secrets in log lines: secrets completely, hosts except their last five characters (as the
// Python filter did for every value).
type Redactor struct{ replacer *strings.Replacer }

// NewRedactor builds a redactor. Values shorter than four characters are ignored: masking them would mangle
// every log line.
func NewRedactor(secrets, hosts []string) *Redactor {
	type pair struct{ old, new string }
	var pairs []pair
	seen := map[string]bool{}
	for _, s := range secrets {
		if len([]rune(s)) >= 4 && !seen[s] {
			seen[s] = true
			pairs = append(pairs, pair{s, "********"})
		}
	}
	for _, h := range hosts {
		r := []rune(h)
		if len(r) >= 4 && strings.ContainsAny(h, ".") && !seen[h] {
			seen[h] = true
			tail := r
			if len(r) > 5 {
				tail = r[len(r)-5:]
			}
			pairs = append(pairs, pair{h, "********" + string(tail)})
		}
	}
	if len(pairs) == 0 {
		return nil
	}
	sort.SliceStable(pairs, func(i, j int) bool { return len(pairs[i].old) > len(pairs[j].old) })
	args := make([]string, 0, 2*len(pairs))
	for _, p := range pairs {
		args = append(args, p.old, p.new)
	}
	return &Redactor{replacer: strings.NewReplacer(args...)}
}

// Redact masks s.
func (r *Redactor) Redact(s string) string {
	if r == nil {
		return s
	}
	return r.replacer.Replace(s)
}

type output struct {
	w     io.Writer
	level *slog.LevelVar
}

type shared struct {
	mu       sync.Mutex
	outputs  []output
	redactor atomic.Pointer[Redactor]
	now      func() time.Time
}

// Handler is a slog.Handler producing Varken's line format.
type Handler struct {
	s      *shared
	module string
	attrs  string
}

// Logger is the application logger and its outputs.
type Logger struct {
	*slog.Logger
	h     *Handler
	level *slog.LevelVar
	file  *RotatingFile
}

// New returns a logger writing to console at DEBUG (Python's temporary logger) until Configure is called.
func New(console io.Writer) *Logger {
	level := &slog.LevelVar{}
	level.Set(slog.LevelDebug)
	s := &shared{now: time.Now, outputs: []output{{w: console, level: level}}}
	h := &Handler{s: s, module: "varken"}
	return &Logger{Logger: slog.New(h), h: h, level: level}
}

// Configure sets the level and adds the rotating log file in <dataFolder>/logs. If the file cannot be opened,
// logging continues on the console and the error is returned.
func (l *Logger) Configure(dataFolder string, debug bool) error {
	if debug {
		l.level.Set(slog.LevelDebug)
	} else {
		l.level.Set(slog.LevelInfo)
	}
	dir := filepath.Join(dataFolder, LogFolder)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("could not create folder %s: %w", dir, err)
	}
	f, err := OpenRotating(filepath.Join(dir, FileName), MaxSize, Backups)
	if err != nil {
		return err
	}
	l.file = f
	l.h.s.mu.Lock()
	l.h.s.outputs = append(l.h.s.outputs, output{w: f, level: l.level})
	l.h.s.mu.Unlock()
	return nil
}

// SetRedactor installs the secret filter.
func (l *Logger) SetRedactor(r *Redactor) { l.h.s.redactor.Store(r) }

// Close closes the log file.
func (l *Logger) Close() error {
	if l.file == nil {
		return nil
	}
	return l.file.Close()
}

// Module returns a logger whose lines name the given module (the Python module name, e.g. "sonarr").
func (l *Logger) Module(name string) *slog.Logger { return l.Logger.With("module", name) }

// Enabled implements slog.Handler.
func (h *Handler) Enabled(_ context.Context, level slog.Level) bool {
	for _, o := range h.s.outputs {
		if level >= o.level.Level() {
			return true
		}
	}
	return false
}

func levelName(l slog.Level) string {
	switch {
	case l >= LevelCritical:
		return "CRITICAL"
	case l >= slog.LevelError:
		return "ERROR"
	case l >= slog.LevelWarn:
		return "WARNING"
	case l >= slog.LevelInfo:
		return "INFO"
	}
	return "DEBUG"
}

// Handle implements slog.Handler.
func (h *Handler) Handle(_ context.Context, r slog.Record) error {
	module := h.module
	var extra strings.Builder
	extra.WriteString(h.attrs)
	r.Attrs(func(a slog.Attr) bool {
		if a.Key == "module" {
			module = a.Value.String()
			return true
		}
		fmt.Fprintf(&extra, " %s=%v", a.Key, a.Value.Any())
		return true
	})
	ts := r.Time
	if ts.IsZero() {
		ts = h.s.now()
	}
	line := fmt.Sprintf("%s : %s : %s : %s%s\n", ts.Format("2006-01-02 15:04:05"), levelName(r.Level), module,
		r.Message, extra.String())
	if red := h.s.redactor.Load(); red != nil {
		line = red.Redact(line)
	}
	h.s.mu.Lock()
	defer h.s.mu.Unlock()
	var errs []error
	for _, o := range h.s.outputs {
		if r.Level >= o.level.Level() {
			if _, err := io.WriteString(o.w, line); err != nil {
				errs = append(errs, err)
			}
		}
	}
	return errors.Join(errs...)
}

// WithAttrs implements slog.Handler.
func (h *Handler) WithAttrs(attrs []slog.Attr) slog.Handler {
	c := *h
	var b strings.Builder
	b.WriteString(h.attrs)
	for _, a := range attrs {
		if a.Key == "module" {
			c.module = a.Value.String()
			continue
		}
		fmt.Fprintf(&b, " %s=%v", a.Key, a.Value.Any())
	}
	c.attrs = b.String()
	return &c
}

// WithGroup implements slog.Handler (groups are flattened).
func (h *Handler) WithGroup(string) slog.Handler { return h }

// RotatingFile is Python's RotatingFileHandler: before a write that would reach maxBytes, varken.log becomes
// varken.log.1, .1 becomes .2 and so on, keeping backups files.
type RotatingFile struct {
	mu      sync.Mutex
	path    string
	max     int64
	backups int
	f       *os.File
	size    int64
}

// OpenRotating opens (appending to) the log file.
func OpenRotating(path string, maxBytes int64, backups int) (*RotatingFile, error) {
	r := &RotatingFile{path: path, max: maxBytes, backups: backups}
	if err := r.open(); err != nil {
		return nil, err
	}
	return r, nil
}

func (r *RotatingFile) open() error {
	f, err := os.OpenFile(r.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return err
	}
	r.f, r.size = f, info.Size()
	return nil
}

// Write implements io.Writer.
func (r *RotatingFile) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.max > 0 && r.size+int64(len(p)) >= r.max {
		if err := r.rotate(); err != nil {
			return 0, err
		}
	}
	n, err := r.f.Write(p)
	r.size += int64(n)
	return n, err
}

func (r *RotatingFile) rotate() error {
	if err := r.f.Close(); err != nil {
		return err
	}
	if r.backups > 0 {
		for i := r.backups - 1; i >= 1; i-- {
			src := fmt.Sprintf("%s.%d", r.path, i)
			if _, err := os.Stat(src); err == nil {
				_ = os.Rename(src, fmt.Sprintf("%s.%d", r.path, i+1))
			}
		}
		_ = os.Rename(r.path, r.path+".1")
	} else {
		_ = os.Truncate(r.path, 0)
	}
	return r.open()
}

// Close closes the file.
func (r *RotatingFile) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.f.Close()
}

// DebugEnabled reproduces Varken.py's debug switch: debug is on by default; when DEBUG is set, it is on only if
// one of debug, Debug or DEBUG is exactly True, true or yes; otherwise -ND/--no_debug turns it off.
func DebugEnabled(getenv func(string) (string, bool), noDebug bool) bool {
	if _, ok := getenv("DEBUG"); ok {
		for _, name := range []string{"debug", "Debug", "DEBUG"} {
			if v, ok := getenv(name); ok && (v == "True" || v == "true" || v == "yes") {
				return true
			}
		}
		return false
	}
	return !noDebug
}
