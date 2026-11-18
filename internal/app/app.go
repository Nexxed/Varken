// Package app is Varken's startup sequence: data folder, logging, configuration, InfluxDB, then the collectors
// on the scheduler. It follows Varken.py step by step, with the same log lines.
package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/Nexxed/Varken/internal/collector"
	"github.com/Nexxed/Varken/internal/collector/cleanuparr"
	"github.com/Nexxed/Varken/internal/collector/lidarr"
	"github.com/Nexxed/Varken/internal/collector/ombi"
	"github.com/Nexxed/Varken/internal/collector/overseerr"
	"github.com/Nexxed/Varken/internal/collector/radarr"
	"github.com/Nexxed/Varken/internal/collector/sickchill"
	"github.com/Nexxed/Varken/internal/collector/sonarr"
	"github.com/Nexxed/Varken/internal/collector/tautulli"
	"github.com/Nexxed/Varken/internal/collector/unifi"
	"github.com/Nexxed/Varken/internal/config"
	"github.com/Nexxed/Varken/internal/geoip"
	"github.com/Nexxed/Varken/internal/influx"
	"github.com/Nexxed/Varken/internal/logging"
	"github.com/Nexxed/Varken/internal/scheduler"
)

// Build information, set at link time.
var (
	Version   = "0.0.0"
	Branch    = "develop"
	BuildDate = "1/1/1970"
)

// Options configure a run.
type Options struct {
	DataFolder string
	NoDebug    bool
	Getenv     config.Env
	Console    io.Writer
	// InfluxAttempts and InfluxWait control how long startup waits for InfluxDB.
	InfluxAttempts int
	InfluxWait     time.Duration
	// Clock overrides the scheduler's clock (tests).
	Clock scheduler.Clock
}

func (o *Options) defaults() {
	if o.Getenv == nil {
		o.Getenv = config.OSEnv
	}
	if o.Console == nil {
		o.Console = os.Stderr
	}
	if o.InfluxAttempts == 0 {
		o.InfluxAttempts = 10
	}
	if o.InfluxWait == 0 {
		o.InfluxWait = 6 * time.Second
	}
}

// ResolveDataFolder is Varken.py's rule: the DATA_FOLDER environment variable wins over the -d flag, which wins
// over the default (a "data" folder next to the program, or in the working directory).
func ResolveDataFolder(getenv config.Env, flag string) string {
	if v, ok := getenv("DATA_FOLDER"); ok {
		return v
	}
	if flag != "" {
		return flag
	}
	if exe, err := os.Executable(); err == nil {
		dir := filepath.Join(filepath.Dir(exe), "data")
		if info, err := os.Stat(dir); err == nil && info.IsDir() {
			return dir
		}
	}
	abs, _ := filepath.Abs("data")
	return abs
}

func checkFolder(folder string) error {
	info, err := os.Stat(folder)
	if err != nil || !info.IsDir() {
		return fmt.Errorf("%s does not exist", folder)
	}
	f, err := os.Open(folder)
	if err != nil {
		return fmt.Errorf("read permission error for %s", folder)
	}
	return f.Close()
}

func platform() string {
	text := runtime.GOOS + " " + runtime.GOARCH
	if data, err := os.ReadFile("/etc/os-release"); err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			if v, ok := strings.CutPrefix(line, "PRETTY_NAME="); ok {
				text += " - " + strings.Trim(v, `"`)
			}
		}
	}
	return text
}

// setup is shared by Run and ImportTautulli: logging, configuration and InfluxDB.
func setup(ctx context.Context, o *Options, log *logging.Logger) (*config.Config, *influx.Writer, error) {
	o.defaults()
	if err := checkFolder(o.DataFolder); err != nil {
		return nil, nil, err
	}
	if err := log.Configure(o.DataFolder, logging.DebugEnabled(o.Getenv, o.NoDebug)); err != nil {
		log.Warn(fmt.Sprintf("Cannot write the log file (%v). Logging to the console only.", err))
	}
	log.Info("Starting Varken...")
	log.Info(fmt.Sprintf("Data folder is %q", o.DataFolder))
	log.Info(platform())
	log.Info("Go " + runtime.Version())
	log.Info(fmt.Sprintf("Varken v%s-%s %s", Version, Branch, BuildDate))
	writeExample(o.DataFolder, log)

	cfg, err := config.Load(o.DataFolder, o.Getenv, log.Module("iniparser"))
	if err != nil {
		return nil, nil, err
	}
	log.SetRedactor(logging.NewRedactor(cfg.Secrets, cfg.Hosts))
	if cfg.Influx.URL == "influxdb.domain.tld" {
		return nil, nil, config.ErrNotConfigured
	}
	dblog := log.Module("dbmanager")
	client, err := influx.NewClient(cfg.Influx, dblog)
	if err != nil {
		return nil, nil, err
	}
	writer := influx.NewWriter(client, dblog)
	if err := writer.Connect(ctx, o.InfluxAttempts, o.InfluxWait); err != nil {
		return nil, nil, err
	}
	return cfg, writer, nil
}

// writeExample keeps varken.example.ini in the data folder current (the Docker image used to copy it on start).
func writeExample(folder string, log *logging.Logger) {
	path := filepath.Join(folder, "varken.example.ini")
	if current, err := os.ReadFile(path); err == nil && string(current) == config.ExampleINI {
		return
	}
	if err := os.WriteFile(path, []byte(config.ExampleINI), 0o644); err != nil {
		log.Debug(fmt.Sprintf("Could not write %s: %v", path, err))
	}
}

// Run runs Varken until ctx ends and returns the process exit code.
func Run(ctx context.Context, o Options) int {
	o.defaults()
	log := logging.New(o.Console)
	defer log.Close()
	cfg, writer, err := setup(ctx, &o, log)
	if err != nil {
		if errors.Is(err, config.ErrNotConfigured) {
			log.Log(ctx, logging.LevelCritical, "You have not configured your varken.ini. Please read Wiki page "+
				"for configuration")
		} else {
			log.Log(ctx, logging.LevelCritical, err.Error())
		}
		return 1
	}
	sched := scheduler.New(log.Module("varken"), o.Clock)
	deps := collector.Deps{Writer: writer, Now: time.Now, Retention: writer.Retention}
	Register(ctx, cfg, sched, deps, log, o.DataFolder)
	if sched.Len() == 0 {
		log.Error("All services disabled. Exiting")
		return 1
	}
	log.Info(fmt.Sprintf("Scheduled %d jobs", sched.Len()))
	sched.Run(ctx)
	log.Info("Varken stopped")
	return 0
}

// Register adds every configured server's jobs to the scheduler, plus the GeoIP update when Tautulli is used.
func Register(ctx context.Context, cfg *config.Config, sched *scheduler.Scheduler, deps collector.Deps,
	log *logging.Logger, dataFolder string) {
	var geo *geoip.Handler
	if len(cfg.Tautulli) > 0 {
		// Open logs and carries on without a database when the download fails: streams are then written with
		// the unknown-location defaults until a later update succeeds.
		geo = geoip.Open(ctx, dataFolder, cfg.Tautulli[0].MaxmindLicenseKey, log.Module("helpers"))
		sched.AddRandom("geoip-update", 12*time.Hour, 24*time.Hour, geo.Update)
	}
	for _, j := range Jobs(cfg, deps, log, geo) {
		sched.Add(j)
	}
}

// Jobs builds every configured server's jobs, in the order Varken.py scheduled them. geo may be nil.
func Jobs(cfg *config.Config, deps collector.Deps, log *logging.Logger, geo *geoip.Handler) []collector.Job {
	with := func(module string) collector.Deps {
		d := deps
		d.Log = log.Module(module)
		return d
	}
	var jobs []collector.Job
	for _, srv := range cfg.Sonarr {
		jobs = append(jobs, sonarr.New(srv, with("sonarr")).Jobs()...)
	}
	for _, srv := range cfg.Tautulli {
		jobs = append(jobs, tautulli.New(srv, with("tautulli"), geo).Jobs()...)
	}
	for _, srv := range cfg.Radarr {
		jobs = append(jobs, radarr.New(srv, with("radarr")).Jobs()...)
	}
	for _, srv := range cfg.Lidarr {
		jobs = append(jobs, lidarr.New(srv, with("lidarr")).Jobs()...)
	}
	for _, srv := range cfg.Ombi {
		jobs = append(jobs, ombi.New(srv, with("ombi")).Jobs()...)
	}
	for _, srv := range cfg.Overseerr {
		jobs = append(jobs, overseerr.New(srv, with("overseerr")).Jobs()...)
	}
	for _, srv := range cfg.SickChill {
		jobs = append(jobs, sickchill.New(srv, with("sickchill")).Jobs()...)
	}
	for _, srv := range cfg.UniFi {
		jobs = append(jobs, unifi.New(srv, with("unifi")).Jobs()...)
	}
	for _, srv := range cfg.Cleanuparr {
		jobs = append(jobs, cleanuparr.New(srv, with("cleanuparr")).Jobs()...)
	}
	return jobs
}

// ImportTautulli is utilities/historical_tautulli_import.py: it writes the last days of play history of every
// Tautulli server.
func ImportTautulli(ctx context.Context, o Options, days int) int {
	o.defaults()
	log := logging.New(o.Console)
	defer log.Close()
	cfg, writer, err := setup(ctx, &o, log)
	if err != nil {
		log.Log(ctx, logging.LevelCritical, err.Error())
		return 1
	}
	if len(cfg.Tautulli) == 0 {
		log.Error("No Tautulli servers are configured")
		return 1
	}
	deps := collector.Deps{Writer: writer, Now: time.Now, Retention: writer.Retention}
	code := 0
	for _, srv := range cfg.Tautulli {
		d := deps
		d.Log = log.Module("tautulli")
		log.Info(fmt.Sprintf("Importing %d days of play history from %s", days, srv.URL))
		if err := tautulli.New(srv, d, nil).HistoryPlays(ctx, days); err != nil {
			log.Error(fmt.Sprintf("Import from %s failed: %v", srv.URL, err))
			code = 1
		}
	}
	return code
}

// Logger is exposed for commands that log before Run (flag errors).
func Logger(w io.Writer) *slog.Logger { return logging.New(w).Logger }
