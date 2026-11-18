package config

// Server settings, one struct per service. JSON names are the Python NamedTuple field names, which the golden
// tests rely on (they load the server settings the Python job ran with).

// Common is shared by every service.
type Common struct {
	ID        int    `json:"id"`
	URL       string `json:"url"` // with scheme, e.g. http://sonarr:8989
	APIKey    string `json:"api_key"`
	VerifySSL bool   `json:"verify_ssl"`
}

// ArrStats are the health, wanted, history, disk, task and library collectors shared by Sonarr, Radarr and Lidarr.
type ArrStats struct {
	GetHealth                  bool `json:"get_health"`
	GetHealthRunSeconds        int  `json:"get_health_run_seconds"`
	GetWanted                  bool `json:"get_wanted"`
	GetWantedRunSeconds        int  `json:"get_wanted_run_seconds"`
	GetHistoryTotals           bool `json:"get_history_totals"`
	GetHistoryTotalsRunSeconds int  `json:"get_history_totals_run_seconds"`
	GetDisk                    bool `json:"get_disk"`
	GetDiskRunSeconds          int  `json:"get_disk_run_seconds"`
	GetHistoryEvents           bool `json:"get_history_events"`
	GetHistoryEventsRunSeconds int  `json:"get_history_events_run_seconds"`
	HistoryEventsDays          int  `json:"history_events_days"`
	HistoryEventsBackfillDays  int  `json:"history_events_backfill_days"`
	GetTasks                   bool `json:"get_tasks"`
	GetTasksRunSeconds         int  `json:"get_tasks_run_seconds"`
	GetLibrary                 bool `json:"get_library"`
	GetLibraryRunSeconds       int  `json:"get_library_run_seconds"`
}

// SonarrServer is a [sonarr-N] section. Lidarr sections use the same settings.
type SonarrServer struct {
	Common
	ArrStats
	MissingDays           int  `json:"missing_days"`
	MissingDaysRunSeconds int  `json:"missing_days_run_seconds"`
	FutureDays            int  `json:"future_days"`
	FutureDaysRunSeconds  int  `json:"future_days_run_seconds"`
	Queue                 bool `json:"queue"`
	QueueRunSeconds       int  `json:"queue_run_seconds"`
}

// LidarrServer is a [lidarr-N] section.
type LidarrServer = SonarrServer

// RadarrServer is a [radarr-N] section.
type RadarrServer struct {
	Common
	ArrStats
	Queue                 bool `json:"queue"`
	QueueRunSeconds       int  `json:"queue_run_seconds"`
	GetMissing            bool `json:"get_missing"`
	GetMissingRunSeconds  int  `json:"get_missing_run_seconds"`
	GetCalendar           bool `json:"get_calendar"`
	GetCalendarRunSeconds int  `json:"get_calendar_run_seconds"`
	CalendarDays          int  `json:"calendar_days"`
}

// TautulliServer is a [tautulli-N] section.
type TautulliServer struct {
	Common
	FallbackIP              string `json:"fallback_ip"`
	MaxmindLicenseKey       string `json:"maxmind_license_key"`
	GetActivity             bool   `json:"get_activity"`
	GetActivityRunSeconds   int    `json:"get_activity_run_seconds"`
	GetStats                bool   `json:"get_stats"`
	GetStatsRunSeconds      int    `json:"get_stats_run_seconds"`
	GetServer               bool   `json:"get_server"`
	GetServerRunSeconds     int    `json:"get_server_run_seconds"`
	GetHistory              bool   `json:"get_history"`
	GetHistoryRunSeconds    int    `json:"get_history_run_seconds"`
	HistoryDays             int    `json:"history_days"`
	HistoryBackfillDays     int    `json:"history_backfill_days"`
	GetDailyStats           bool   `json:"get_daily_stats"`
	GetDailyStatsRunSeconds int    `json:"get_daily_stats_run_seconds"`
}

// OmbiServer is an [ombi-N] section.
type OmbiServer struct {
	Common
	RequestTypeCounts      bool `json:"request_type_counts"`
	RequestTypeRunSeconds  int  `json:"request_type_run_seconds"`
	RequestTotalCounts     bool `json:"request_total_counts"`
	RequestTotalRunSeconds int  `json:"request_total_run_seconds"`
	IssueStatusCounts      bool `json:"issue_status_counts"`
	IssueStatusRunSeconds  int  `json:"issue_status_run_seconds"`
	// AddServerTag comes from [global] add_server_tag (Go only, default off): it adds the server tag to the
	// count points, which the Python version wrote without one.
	AddServerTag bool `json:"add_server_tag"`
}

// OverseerrServer is an [overseerr-N] section.
type OverseerrServer struct {
	Common
	GetRequestTotalCounts    bool `json:"get_request_total_counts"`
	RequestTotalRunSeconds   int  `json:"request_total_run_seconds"`
	NumLatestRequestsToFetch int  `json:"num_latest_requests_to_fetch"`
	NumLatestRequestsSeconds int  `json:"num_latest_requests_seconds"`
	// GetLatestRequests is read for completeness but, as in the Python version, does not switch the job off:
	// num_latest_requests_to_fetch = 0 does.
	GetLatestRequests bool `json:"get_latest_requests"`
	AddServerTag      bool `json:"add_server_tag"`
}

// SickChillServer is a [sickchill-N] section.
type SickChillServer struct {
	Common
	GetMissing           bool `json:"get_missing"`
	GetMissingRunSeconds int  `json:"get_missing_run_seconds"`
}

// UniFiServer is a [unifi-N] section. It has no API key.
type UniFiServer struct {
	Common
	Username              string `json:"username"`
	Password              string `json:"password"`
	Site                  string `json:"site"`
	USGName               string `json:"usg_name"`
	GetUSGStatsRunSeconds int    `json:"get_usg_stats_run_seconds"`
	AddServerTag          bool   `json:"add_server_tag"`
}

// CleanuparrServer is a [cleanuparr-N] section.
type CleanuparrServer struct {
	Common
	IncludeDryRun         bool   `json:"include_dry_run"`
	GetStats              bool   `json:"get_stats"`
	StatsHours            int    `json:"stats_hours"`
	GetStatsRunSeconds    int    `json:"get_stats_run_seconds"`
	GetTimeline           bool   `json:"get_timeline"`
	TimelineHours         int    `json:"timeline_hours"`
	GetTimelineRunSeconds int    `json:"get_timeline_run_seconds"`
	GetStrikes            bool   `json:"get_strikes"`
	NumStrikesToFetch     int    `json:"num_strikes_to_fetch"`
	GetStrikesRunSeconds  int    `json:"get_strikes_run_seconds"`
	GetEvents             bool   `json:"get_events"`
	EventsSeverity        string `json:"events_severity"`
	NumEventsToFetch      int    `json:"num_events_to_fetch"`
	GetEventsRunSeconds   int    `json:"get_events_run_seconds"`
}
