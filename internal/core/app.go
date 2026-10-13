// Package core wires every service together: config, database, auth, registry,
// scheduler, backups, creation, realtime, metrics, cluster — the App container.
package core

import (
        "fmt"
        "os"
        "path/filepath"
        "time"

        "github.com/FlameInTheDark/servy/internal/auth"
        "github.com/FlameInTheDark/servy/internal/backups"
        "github.com/FlameInTheDark/servy/internal/bigbucket"
        "github.com/FlameInTheDark/servy/internal/cluster"
        "github.com/FlameInTheDark/servy/internal/config"
        "github.com/FlameInTheDark/servy/internal/creation"
        "github.com/FlameInTheDark/servy/internal/database"
        "github.com/FlameInTheDark/servy/internal/dockerx"
        "github.com/FlameInTheDark/servy/internal/events"
        "github.com/FlameInTheDark/servy/internal/files"
        "github.com/FlameInTheDark/servy/internal/httpapi"
        "github.com/FlameInTheDark/servy/internal/logging"
        "github.com/FlameInTheDark/servy/internal/metrics"
        "github.com/FlameInTheDark/servy/internal/models"
        "github.com/FlameInTheDark/servy/internal/ports"
        "github.com/FlameInTheDark/servy/internal/realtime"
        "github.com/FlameInTheDark/servy/internal/scheduler"
        "github.com/FlameInTheDark/servy/internal/servers"
        "github.com/FlameInTheDark/servy/internal/steamcmd"
        "github.com/FlameInTheDark/servy/internal/webhooks"
)

// Paths resolves the on-disk layout (layout-compatible with legacy imports).
type Paths struct {
        DataDir     string // base
        ConfigDir   string // <data>/config
        DBPath      string // <data>/config/db/servy.sqlite
        ServersDir  string // <data>/servers
        BackupsDir  string // <data>/backups
        LogsDir     string // <data>/logs
        ImportDir   string // <data>/import/upload
        UploadDir   string // <data>/import/upload (chunks)
        SteamDir    string // <data>/steamcmd
        BucketCache string // <data>/config/bigbucket
}

// ResolvePaths computes the layout from a base dir ("" = cwd).
func ResolvePaths(base string) Paths {
        if base == "" {
                base, _ = os.Getwd()
        }
        abs := func(p string) string {
                if filepath.IsAbs(p) {
                        return p
                }
                return filepath.Join(base, p)
        }
        return Paths{
                DataDir: base, ConfigDir: abs("config"),
                DBPath: abs("config/db/servy.sqlite"), ServersDir: abs("servers"),
                BackupsDir: abs("backups"), LogsDir: abs("logs"),
                ImportDir: abs("import/upload"), UploadDir: abs("import/upload"),
                SteamDir: abs("steamcmd"), BucketCache: abs("config/bigbucket"),
        }
}

// EnsureDirs creates the layout.
func (p Paths) EnsureDirs() error {
        for _, d := range []string{p.ConfigDir, filepath.Dir(p.DBPath), p.ServersDir, p.BackupsDir, p.LogsDir, p.ImportDir, p.SteamDir, p.BucketCache} {
                if err := os.MkdirAll(d, 0o755); err != nil {
                        return err
                }
        }
        return nil
}

// App is the full application container.
type App struct {
        Cfg       *config.Manager
        Log       *logging.Manager
        DB        *database.Queries
        Bus       *events.Bus
        Auth      *auth.Service
        Registry  *servers.Registry
        Files     *files.Manager
        Uploads   *files.UploadStore
        Backups   *backups.Service
        Creation  *creation.Service
        Scheduler *scheduler.Engine
        Webhooks  *webhooks.Service
        Realtime  *realtime.Hub
        Metrics   *metrics.Collector
        HostStats *servers.HostStats
        Cluster   *cluster.Client
        Bucket    *bigbucket.Client
        Steam     *steamcmd.Manager
        SteamCatalog *steamcmd.Catalog
        Updater   *servers.UpdateManager
        Docker    *dockerx.Manager // nil when the daemon is unreachable
        API       *httpapi.App
        Paths     Paths
        Secret    func() string

        grpcServer *cluster.Service
        stopFns    []func()
}

// Options tweaks startup.
type Options struct {
        DataDir       string
        SkipAutostart bool
}

// New builds and wires the application (does NOT start servers).
func New(cfg *config.Manager, opts Options) (*App, error) {
        paths := ResolvePaths(opts.DataDir)
        if err := paths.EnsureDirs(); err != nil {
                return nil, err
        }
        db, err := database.Open(paths.DBPath)
        if err != nil {
                return nil, fmt.Errorf("database: %w", err)
        }
        q := database.NewQueries(db)
        log, err := logging.NewManager(paths.LogsDir, cfg.Get().MaxLogLines)
        if err != nil {
                return nil, err
        }
        bus := events.NewBus()

        // docker execution backend (nil when the daemon is unreachable)
        dockerMgr, derr := dockerx.New()
        if derr != nil {
                log.Get("server").Warning("docker execution backend unavailable (game servers will run as host processes): %v", derr)
        } else {
                log.Get("server").Info("docker execution backend ready (%s)", dockerMgr.Host())
        }
        // JVM detection hints from config
        servers.SetExtraJavaPaths(cfg.Get().JavaExtraPaths)

        // panel_settings bootstrap (cookie secret for JWT signing)
        st, err := q.GetSettings()
        if err != nil || st.CookieSecret == nil || *st.CookieSecret == "" || cfg.Get().ResetSecretsOnNextBoot {
                secret := auth.RandomToken(32)
                apiKey := auth.RandomToken(32)
                q.UpsertSettings(&apiKey, &secret, nil, 1.0, nil, nil, 0, nil, nil, nil, nil, nil)
                st, _ = q.GetSettings()
        }
        secretFn := func() string {
                if st, err := q.GetSettings(); err == nil && st.CookieSecret != nil {
                        return *st.CookieSecret
                }
                return "servy-dev-secret"
        }

        authSvc := auth.NewService(q, log, cfg, secretFn)
        // remove leftover anti-lockout users from previous boot
        authSvc.RemoveAntiLockoutUsers()

        // Steam catalog is built first: supervisors consult it for per-game
        // metadata (query ports, save/backup dirs) when pinging servers.
	catalog, err := steamcmd.LoadCatalog(filepath.Join(paths.ConfigDir, "steamgames.json"))
	if err != nil {
		return nil, fmt.Errorf("Steam game catalog: %w", err)
	}

        // Host port allocator for containerized servers: detects servy-vs-servy
        // port clashes before `docker run` fails with an opaque daemon error.
        deps := servers.Deps{DB: q, Bus: bus, Log: log, Cfg: cfg, Docker: dockerMgr,
                Catalog: catalog, Ports: ports.NewAllocator()}
        registry, err := servers.NewRegistry(deps, paths.ServersDir, paths.BackupsDir)
        if err != nil {
                return nil, err
        }

        filesSvc := files.New()
        uploads, err := files.NewUploadStore(paths.UploadDir)
        if err != nil {
                return nil, err
        }

        backupSvc := backups.New(q, log, bus, registry)
        bucket := bigbucket.New(cfg.Get().BigBucketRepo, paths.BucketCache)
        steam := steamcmd.NewManager(paths.SteamDir)
        steam.SetDocker(dockerMgr, cfg.Get().DockerSteamImage)
        creationSvc := creation.New(q, log, bus, registry, bucket, steam, catalog, backupSvc, paths.ServersDir, paths.ImportDir)

        executor := func(serverID, action, actionID, command string, userID int64) error {
                sup, err := registry.Get(serverID)
                if err != nil {
                        return err
                }
                switch action {
                case "start":
                        return sup.Start(userID)
                case "stop":
                        return sup.Stop("schedule", userID)
                case "restart":
                        return sup.Restart(userID, nil)
                case "kill":
                        return sup.Kill(userID)
                case "backup":
                        bid := actionID
                        if bid == "" {
                                if b, err := backupSvc.DefaultBackup(serverID); err == nil {
                                        bid = b.BackupID
                                }
                        }
                        if bid == "" {
                                return fmt.Errorf("no backup config for server")
                        }
                        return backupSvc.Run(bid, userID)
                case "command":
                        return sup.SendCommand(command, userID)
                }
                return fmt.Errorf("unknown schedule action %q", action)
        }
        engine := scheduler.New(q, log, cfg, bus, executor)
        hookSvc := webhooks.New(q, log, bus)
        hub := realtime.NewHub(authSvc, log, bus)
        collector := metrics.New()
        hostStats := servers.NewHostStats(deps)
        clusterClient := cluster.NewClient(cfg.Get().ClusterToken)
        updater := servers.NewUpdateManager(deps, registry)

        a := &App{
                Cfg: cfg, Log: log, DB: q, Bus: bus, Auth: authSvc, Registry: registry,
                Files: filesSvc, Uploads: uploads, Backups: backupSvc, Creation: creationSvc,
                Scheduler: engine, Webhooks: hookSvc, Realtime: hub, Metrics: collector,
                HostStats: hostStats, Cluster: clusterClient, Bucket: bucket, Steam: steam,
                SteamCatalog: catalog,
                Updater: updater, Paths: paths, Secret: secretFn, Docker: dockerMgr,
        }

        a.API = &httpapi.App{
                Cfg: cfg, Log: log, DB: q, Bus: bus, Auth: authSvc, Registry: registry,
                Files: filesSvc, Uploads: uploads, Backups: backupSvc, Creation: creationSvc,
                Scheduler: engine, Realtime: hub, Metrics: collector, HostStats: hostStats,
                Cluster: clusterClient, Bucket: bucket, Steam: steam, SteamCatalog: catalog, Updater: updater,
                ImportDir: paths.ImportDir, ServersRoot: paths.ServersDir,
                BackupRoot: paths.BackupsDir, DataDir: paths.DataDir, Docker: dockerMgr,
        }
        return a, nil
}

// Start boots the background services (stats, scheduler, webhooks, autostart).
func (a *App) Start() error {
        a.Metrics.SetHost(a.HostStats)
        a.HostStats.Start()
        a.stopFns = append(a.stopFns, a.HostStats.Stop)
        if err := a.Scheduler.Start(); err != nil {
                return err
        }
        a.stopFns = append(a.stopFns, a.Scheduler.Stop)
        a.Webhooks.Start()
        a.stopFns = append(a.stopFns, a.Webhooks.Stop)

        // metrics feed: subscribe to server stats events
        ch, cancel := a.Bus.Subscribe("*")
        go func() {
                for ev := range ch {
                        switch ev.Type {
                        case events.EvServerStats:
                                if rt, ok := ev.Data.(modelsServerRuntime); ok {
                                        a.Metrics.UpdateServer(ev.ServerID, rt)
                                }
                        }
                }
        }()
        a.stopFns = append(a.stopFns, cancel)

        // retention sweeps: stats history + audit (hourly)
        go func() {
                tick := time.NewTicker(time.Hour)
                defer tick.Stop()
                for range tick.C {
                        days := a.Cfg.Get().HistoryMaxAge
                        if days > 0 {
                                cutoff := time.Now().UTC().AddDate(0, 0, -days).Format(time.RFC3339)
                                a.DB.DeleteServerStatsOlder(cutoff)
                                a.DB.DeleteHostStatsOlder(cutoff)
                        }
                        if a.Cfg.Get().PanelLogsDeleteAfterDays > 0 {
                                cutoff := time.Now().UTC().AddDate(0, 0, -a.Cfg.Get().PanelLogsDeleteAfterDays).Format(time.RFC3339)
                                a.DB.DeleteAuditOlder(cutoff)
                        }
                }
        }()

        // autostart
        a.Registry.AutostartAll()
        a.Log.Get("server").Info("servy services started (scheduler, stats, webhooks, realtime)")
        return nil
}

// Stop gracefully shuts everything down (stops servers).
func (a *App) Stop() {
        a.Registry.StopAll()
        for _, fn := range a.stopFns {
                fn()
        }
}

// StartGRPC launches the cluster gRPC server (panel or agent role).
func (a *App) StartGRPC(addr string) error {
        svc := cluster.NewService(servers.Deps{DB: a.DB, Bus: a.Bus, Log: a.Log, Cfg: a.Cfg, Docker: a.Docker, Catalog: a.SteamCatalog}, a.Registry, a.Cfg.Get().ClusterToken, a.Log, a.Bus)
        a.grpcServer = svc
        return cluster.ServeGRPC(svc, addr)
}

// modelsServerRuntime aliases the runtime type (wiring convenience).
type modelsServerRuntime = models.ServerRuntime
