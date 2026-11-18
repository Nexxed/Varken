// Command varken aggregates data from the Plex ecosystem into InfluxDB.
//
//	varken [-d DATA_FOLDER] [-ND]           run the collectors (default)
//	varken import-tautulli [-d DIR] [-D N]  import N days of Tautulli play history
//	varken grafana-build [flags]            create the Grafana datasource and import the dashboard
//	varken version                          print the version
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	_ "time/tzdata" // TZ works without the host's zoneinfo (e.g. in a minimal container)

	"github.com/Nexxed/Varken/internal/app"
	"github.com/Nexxed/Varken/internal/config"
	"github.com/Nexxed/Varken/internal/grafana"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := run(ctx, os.Args[1:], os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	command := "run"
	if len(args) > 0 && len(args[0]) > 0 && args[0][0] != '-' {
		command, args = args[0], args[1:]
	}
	switch command {
	case "run":
		return runCollectors(ctx, args, stderr)
	case "import-tautulli":
		return importTautulli(ctx, args, stderr)
	case "grafana-build":
		return grafanaBuild(ctx, args, stdout, stderr)
	case "version":
		fmt.Fprintf(stdout, "Varken v%s-%s %s\n", app.Version, app.Branch, app.BuildDate)
		return 0
	default:
		fmt.Fprintf(stderr, "unknown command %q (run, import-tautulli, grafana-build, version)\n", command)
		return 2
	}
}

func newFlags(name string, stderr io.Writer, usage string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		fmt.Fprintf(stderr, "usage: %s\n\n", usage)
		fs.PrintDefaults()
	}
	return fs
}

func runCollectors(ctx context.Context, args []string, stderr io.Writer) int {
	fs := newFlags("varken", stderr, "varken [-d DATA_FOLDER] [-ND]\n\nAggregates data from the Plex ecosystem into InfluxDB")
	var folder string
	var debug, noDebug bool
	fs.StringVar(&folder, "d", "", "Define an alternate data folder location")
	fs.StringVar(&folder, "data-folder", "", "Define an alternate data folder location")
	fs.BoolVar(&debug, "D", false, "Use to enable DEBUG logging. (Deprecated: debug is on by default)")
	fs.BoolVar(&debug, "debug", false, "Use to enable DEBUG logging. (Deprecated)")
	fs.BoolVar(&noDebug, "ND", false, "Use to disable DEBUG logging")
	fs.BoolVar(&noDebug, "no_debug", false, "Use to disable DEBUG logging")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	return app.Run(ctx, app.Options{
		DataFolder: app.ResolveDataFolder(config.OSEnv, folder),
		NoDebug:    noDebug,
		Console:    stderr,
	})
}

func importTautulli(ctx context.Context, args []string, stderr io.Writer) int {
	fs := newFlags("import-tautulli", stderr, "varken import-tautulli [-d DATA_FOLDER] [-D DAYS]\n\n"+
		"Imports Tautulli play history into InfluxDB")
	var folder string
	var days int
	fs.StringVar(&folder, "d", "", "Define an alternate data folder location")
	fs.StringVar(&folder, "data-folder", "", "Define an alternate data folder location")
	fs.IntVar(&days, "D", 365, "Days of play history to import")
	fs.IntVar(&days, "days", 365, "Days of play history to import")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	return app.ImportTautulli(ctx, app.Options{DataFolder: app.ResolveDataFolder(config.OSEnv, folder),
		Console: stderr}, days)
}

func grafanaBuild(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	o := grafana.Defaults()
	fs := newFlags("grafana-build", stderr, "varken grafana-build [flags]\n\n"+
		"Creates the Varken InfluxDB datasource in Grafana and imports the Varken dashboard")
	fs.StringVar(&o.GrafanaURL, "grafana-url", o.GrafanaURL, "Grafana URL")
	fs.StringVar(&o.Username, "username", o.Username, "Grafana username")
	fs.StringVar(&o.Password, "password", o.Password, "Grafana password")
	fs.BoolVar(&o.Verify, "verify", o.Verify, "Verify Grafana's TLS certificate")
	fs.BoolVar(&o.Docker, "docker", o.Docker, "InfluxDB runs in Docker as 'influxdb'; false uses -host-ip")
	fs.StringVar(&o.HostIP, "host-ip", o.HostIP, "InfluxDB host when -docker=false")
	fs.StringVar(&o.Database, "database", o.Database, "InfluxDB database")
	fs.StringVar(&o.MoviesLibrary, "movies-library", o.MoviesLibrary, "Movies library name")
	fs.StringVar(&o.Movies4KLibrary, "movies-4k-library", o.Movies4KLibrary, "4K movies library name")
	fs.StringVar(&o.TVLibrary, "tv-library", o.TVLibrary, "TV library name")
	fs.StringVar(&o.TV4KLibrary, "tv-4k-library", o.TV4KLibrary, "4K TV library name")
	fs.StringVar(&o.MusicLibrary, "music-library", o.MusicLibrary, "Music library name")
	fs.StringVar(&o.USGName, "usg-name", o.USGName, "UniFi USG name")
	fs.StringVar(&o.OmbiURL, "ombi-url", o.OmbiURL, "Ombi URL for dashboard links")
	fs.StringVar(&o.TautulliURL, "tautulli-url", o.TautulliURL, "Tautulli URL for dashboard links")
	fs.StringVar(&o.SonarrURL, "sonarr-url", o.SonarrURL, "Sonarr URL for dashboard links")
	fs.StringVar(&o.RadarrURL, "radarr-url", o.RadarrURL, "Radarr URL for dashboard links")
	fs.StringVar(&o.SickChillURL, "sickchill-url", o.SickChillURL, "SickChill URL for dashboard links")
	fs.StringVar(&o.LidarrURL, "lidarr-url", o.LidarrURL, "Lidarr URL for dashboard links")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	o.Out = stdout
	if err := grafana.Build(ctx, o); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return 0
}
