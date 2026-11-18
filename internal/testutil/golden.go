package testutil

import (
	"context"
	"testing"
	"time"

	"github.com/Nexxed/Varken/internal/collector"
)

// Env is what a golden case runs against.
type Env struct {
	// URL is the fixture server's URL plus the case's base_path: use it as the server's url setting.
	URL     string
	Deps    collector.Deps
	Fixture *Fixture
	Capture *Capture
	Ctx     context.Context
}

// RunGolden runs every fixture case of service. run builds the collector from the case's server.json (with
// env.URL as its url) and calls the job named by c.Job. The written line protocol and the requests made must
// match the expected files the Python version produced.
func RunGolden(t *testing.T, service string, run func(t *testing.T, c Case, env Env)) {
	for _, c := range LoadCases(t, service) {
		t.Run(c.Name, func(t *testing.T) {
			fixture := StartFixture(t, c.Routes)
			capture := &Capture{}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			env := Env{
				URL:     fixture.URL + c.BasePath,
				Fixture: fixture,
				Capture: capture,
				Ctx:     ctx,
				Deps: collector.Deps{
					Writer: capture,
					Log:    Logger(t),
					Now:    c.Now(t),
				},
			}
			run(t, c, env)
			CheckGolden(t, c, capture.Lines(), fixture.Requests())
		})
	}
}
