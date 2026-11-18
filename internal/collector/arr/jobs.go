package arr

import (
	"context"
	"fmt"

	"github.com/Nexxed/Varken/internal/collector"
	"github.com/Nexxed/Varken/internal/config"
)

// StatsJobs is Varken.py's schedule_arr_stats plus the history-event, task and library jobs, with the Python
// schedule tags as names. The history backfill runs once at startup when history_events_backfill_days is larger
// than history_events_days.
func StatsJobs(service string, id int, s config.ArrStats, b *Base) []collector.Job {
	name := func(job string) string { return fmt.Sprintf("%s-%d-%s", service, id, job) }
	var jobs []collector.Job
	if s.GetHealth {
		jobs = append(jobs, collector.Every(name("get_health"), s.GetHealthRunSeconds, b.Health))
	}
	if s.GetWanted {
		jobs = append(jobs, collector.Every(name("get_wanted"), s.GetWantedRunSeconds, b.Wanted))
	}
	if s.GetHistoryTotals {
		jobs = append(jobs, collector.Every(name("get_history_totals"), s.GetHistoryTotalsRunSeconds, b.HistoryTotals))
	}
	if s.GetDisk {
		jobs = append(jobs, collector.Every(name("get_disk"), s.GetDiskRunSeconds, b.Disk))
	}
	if s.GetHistoryEvents {
		days, backfill := s.HistoryEventsDays, s.HistoryEventsBackfillDays
		jobs = append(jobs, collector.Every(name("history_events"), s.GetHistoryEventsRunSeconds,
			func(ctx context.Context) error { return b.HistoryEvents(ctx, days) }))
		if backfill > days {
			jobs = append(jobs, collector.Once(name("history_events_backfill"),
				func(ctx context.Context) error { return b.HistoryEvents(ctx, backfill) }))
		}
	}
	if s.GetTasks {
		jobs = append(jobs, collector.Every(name("tasks"), s.GetTasksRunSeconds, b.Tasks))
	}
	if s.GetLibrary {
		jobs = append(jobs, collector.Every(name("library"), s.GetLibraryRunSeconds, b.Library))
	}
	return jobs
}
