// Package arrtest runs the golden cases of the jobs Sonarr, Radarr and Lidarr share.
package arrtest

import (
	"fmt"
	"testing"

	"github.com/Nexxed/Varken/internal/collector/arr"
	"github.com/Nexxed/Varken/internal/testutil"
)

// RunStats runs a shared job named by its Python method name. It fails for any other job.
func RunStats(t *testing.T, c testutil.Case, env testutil.Env, b *arr.Base) error {
	t.Helper()
	switch c.Job {
	case "get_health":
		return b.Health(env.Ctx)
	case "get_wanted":
		return b.Wanted(env.Ctx)
	case "get_history_totals":
		return b.HistoryTotals(env.Ctx)
	case "get_disk":
		return b.Disk(env.Ctx)
	case "get_history_events":
		var days int
		if !c.Arg(t, "days", &days) {
			return fmt.Errorf("%s: get_history_events needs a days argument", c.Name)
		}
		return b.HistoryEvents(env.Ctx, days)
	case "get_tasks":
		return b.Tasks(env.Ctx)
	case "get_library":
		return b.Library(env.Ctx)
	}
	return fmt.Errorf("%s: unknown job %s", c.Name, c.Job)
}
