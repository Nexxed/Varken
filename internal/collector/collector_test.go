package collector

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/Nexxed/Varken/internal/influx"
	py "github.com/Nexxed/Varken/internal/pycompat"
)

type recorder struct{ points []influx.Point }

func (r *recorder) Write(_ context.Context, p []influx.Point) error {
	r.points = append(r.points, p...)
	return nil
}

func TestDepsAndWrite(t *testing.T) {
	d := Deps{}.WithDefaults()
	if d.Log == nil || d.Now == nil || d.Retention() != 0 {
		t.Fatal("defaults")
	}
	var buf bytes.Buffer
	rec := &recorder{}
	d = Deps{Writer: rec, Log: slog.New(slog.NewTextHandler(&buf, nil))}.WithDefaults()
	if err := Write(context.Background(), d, nil, NoData("sonarr", "queue")); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "No data to send to influx for sonarr-queue instance, discarding.") {
		t.Errorf("log = %s", buf.String())
	}
	if err := Write(context.Background(), d, nil, ""); err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 11, 18, 10, 30, 0, 123456789, time.FixedZone("", -6*3600))
	p := Point("Sonarr", M{"type": py.String("Queue")}, M{"hash": py.String("x")}, influx.At(at))
	if err := Write(context.Background(), d, []influx.Point{p}, "unused"); err != nil || len(rec.points) != 1 {
		t.Fatal("write")
	}
	d.Now = func() time.Time { return at }
	if ns, _, _ := NowTime(d).NS(); ns != 1795019400123456000 {
		t.Errorf("NowTime = %d", ns)
	}
	if got := DateString(Date(at)); got != "2026-11-18" || Date(at).Hour() != 0 {
		t.Errorf("Date = %v", Date(at))
	}
}

func TestJobs(t *testing.T) {
	run := func(context.Context) error { return nil }
	if j := Every("x", 30, run); j.Interval != 30*time.Second || j.Once {
		t.Error("Every")
	}
	if j := Once("y", run); !j.Once || j.Interval != 0 {
		t.Error("Once")
	}
}
