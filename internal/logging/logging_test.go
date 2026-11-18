package logging

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func fixedNow() time.Time { return time.Date(2026, 11, 18, 10, 30, 5, 0, time.Local) }

func TestLineFormat(t *testing.T) {
	var buf bytes.Buffer
	l := New(&buf)
	l.h.s.now = fixedNow
	log := slog.New(l.h)
	rec := slog.NewRecord(time.Time{}, slog.LevelWarn, "No data to send", 0)
	rec.AddAttrs(slog.String("module", "sonarr"), slog.Int("n", 2))
	if err := log.Handler().Handle(context.Background(), rec); err != nil {
		t.Fatal(err)
	}
	want := "2026-11-18 10:30:05 : WARNING : sonarr : No data to send n=2\n"
	if buf.String() != want {
		t.Errorf("got %q, want %q", buf.String(), want)
	}
	buf.Reset()
	l.Module("tautulli").With("server", 1).Log(context.Background(), LevelCritical, "boom")
	if !strings.Contains(buf.String(), " : CRITICAL : tautulli : boom server=1\n") {
		t.Errorf("got %q", buf.String())
	}
	buf.Reset()
	l.WithGroup("g").Debug("dbg")
	if !strings.Contains(buf.String(), " : DEBUG : varken : dbg\n") {
		t.Errorf("got %q", buf.String())
	}
	for level, name := range map[slog.Level]string{slog.LevelInfo: "INFO", slog.LevelError: "ERROR"} {
		if levelName(level) != name {
			t.Errorf("levelName(%v)", level)
		}
	}
}

func TestLevelsAndFile(t *testing.T) {
	var console bytes.Buffer
	l := New(&console)
	dir := t.TempDir()
	if err := l.Configure(dir, false); err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	l.Debug("hidden")
	l.Info("shown")
	data, err := os.ReadFile(filepath.Join(dir, "logs", "varken.log"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "hidden") || !strings.Contains(string(data), "shown") {
		t.Errorf("file = %q", data)
	}
	if strings.Contains(console.String(), "hidden") {
		t.Error("debug reached the console at INFO level")
	}
	if l.Enabled(context.Background(), slog.LevelDebug) {
		t.Error("debug should be disabled")
	}
}

func TestConfigureFailsOnUnwritableFolder(t *testing.T) {
	file := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(file, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	l := New(&bytes.Buffer{})
	if err := l.Configure(file, true); err == nil {
		t.Error("expected an error")
	}
	if err := l.Close(); err != nil {
		t.Error(err)
	}
}

func TestRedaction(t *testing.T) {
	var buf bytes.Buffer
	l := New(&buf)
	l.SetRedactor(NewRedactor([]string{"abcdefgh1234", "pw", "abcdefgh123"},
		[]string{"sonarr.example.com:8989", "sonarr.example.com", "sonarr", "1.2"}))
	l.Info("GET http://sonarr.example.com:8989/api?apikey=abcdefgh1234 failed for pw sonarr")
	got := buf.String()
	if strings.Contains(got, "abcdefgh") || strings.Contains(got, "sonarr.example") {
		t.Errorf("secret leaked: %q", got)
	}
	if !strings.Contains(got, "http://********:8989/api?apikey=********") &&
		!strings.Contains(got, "http://********.com:8989/api?apikey=********") &&
		!strings.Contains(got, "********:8989/api?apikey=********") {
		t.Errorf("got %q", got)
	}
	if !strings.Contains(got, " pw sonarr") {
		t.Errorf("short values and dot-less names must not be masked: %q", got)
	}
	if NewRedactor(nil, nil) != nil {
		t.Error("empty redactor should be nil")
	}
	var nilRedactor *Redactor
	if nilRedactor.Redact("x") != "x" {
		t.Error("nil redactor")
	}
}

func TestRotation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "varken.log")
	r, err := OpenRotating(path, 100, 2)
	if err != nil {
		t.Fatal(err)
	}
	line := []byte(strings.Repeat("x", 39) + "\n")
	for i := 0; i < 10; i++ {
		if _, err := r.Write(line); err != nil {
			t.Fatal(err)
		}
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"varken.log", "varken.log.1", "varken.log.2"} {
		info, err := os.Stat(filepath.Join(filepath.Dir(path), name))
		if err != nil || info.Size() >= 100 {
			t.Errorf("%s: %v %v", name, info, err)
		}
	}
	if _, err := os.Stat(path + ".3"); err == nil {
		t.Error("only two backups should be kept")
	}
	// Reopening appends.
	r, err = OpenRotating(path, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	before, _ := os.Stat(path)
	_, _ = r.Write(line)
	after, _ := os.Stat(path)
	if after.Size() != before.Size()+int64(len(line)) {
		t.Error("reopen should append")
	}
	_ = r.Close()
	noBackups, err := OpenRotating(filepath.Join(t.TempDir(), "x.log"), 50, 0)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		_, _ = noBackups.Write(line)
	}
	_ = noBackups.Close()
}

func TestDebugEnabled(t *testing.T) {
	cases := []struct {
		env     map[string]string
		noDebug bool
		want    bool
	}{
		{nil, false, true},
		{nil, true, false},
		{map[string]string{"DEBUG": "True"}, true, true},
		{map[string]string{"DEBUG": "true"}, false, true},
		{map[string]string{"DEBUG": "yes"}, false, true},
		{map[string]string{"DEBUG": "False"}, false, false},
		{map[string]string{"DEBUG": "1"}, false, false},
		{map[string]string{"DEBUG": "no", "debug": "true"}, false, true},
		{map[string]string{"debug": "true"}, true, false},
	}
	for _, c := range cases {
		get := func(k string) (string, bool) { v, ok := c.env[k]; return v, ok }
		if got := DebugEnabled(get, c.noDebug); got != c.want {
			t.Errorf("env=%v noDebug=%v: got %v", c.env, c.noDebug, got)
		}
	}
}
