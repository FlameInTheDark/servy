// Package httpapi exposes the full REST API under /api/v1 (Fiber), the
// websocket endpoint, metrics endpoints, public status and the embedded SPA.
package httpapi

import (
        "encoding/json"
        "fmt"
        "io/fs"
        "net/http"
        "os"
        "runtime"
        "strings"

        "github.com/fasthttp/websocket"
        "github.com/valyala/fasthttp"

        "github.com/gofiber/fiber/v3"
        "github.com/gofiber/fiber/v3/middleware/cors"
        "github.com/gofiber/fiber/v3/middleware/static"
        "github.com/gofiber/fiber/v3/middleware/recover"

        "github.com/FlameInTheDark/servy/internal/auth"
        "github.com/FlameInTheDark/servy/internal/authz"
        "github.com/FlameInTheDark/servy/internal/backups"
        "github.com/FlameInTheDark/servy/internal/bigbucket"
        "github.com/FlameInTheDark/servy/internal/cluster"
        "github.com/FlameInTheDark/servy/internal/config"
        "github.com/FlameInTheDark/servy/internal/creation"
        "github.com/FlameInTheDark/servy/internal/database"
        "github.com/FlameInTheDark/servy/internal/dockerx"
        "github.com/FlameInTheDark/servy/internal/events"
        "github.com/FlameInTheDark/servy/internal/files"
        "github.com/FlameInTheDark/servy/internal/i18n"
        "github.com/FlameInTheDark/servy/internal/logging"
        "github.com/FlameInTheDark/servy/internal/metrics"
        "github.com/FlameInTheDark/servy/internal/realtime"
        "github.com/FlameInTheDark/servy/internal/scheduler"
        "github.com/FlameInTheDark/servy/internal/servers"
        "github.com/FlameInTheDark/servy/internal/steamcmd"
        "github.com/FlameInTheDark/servy/internal/version"
)

// App bundles every service the API needs.
type App struct {
        Cfg         *config.Manager
        Log         *logging.Manager
        DB          *database.Queries
        Bus         *events.Bus
        Auth        *auth.Service
        Registry    *servers.Registry
        Files       *files.Manager
        Uploads     *files.UploadStore
        Backups     *backups.Service
        Creation    *creation.Service
        Scheduler   *scheduler.Engine
        Realtime    *realtime.Hub
        Metrics     *metrics.Collector
        HostStats   *servers.HostStats
        Cluster     *cluster.Client
        Nodes       *cluster.NodeRegistry
        Tickets     *cluster.TicketStore
        Bucket      *bigbucket.Client
        Steam       *steamcmd.Manager
        SteamCatalog *steamcmd.Catalog
        Docker      *dockerx.Manager // nil = daemon unreachable
        Updater     *servers.UpdateManager
        ImportDir   string
        ServersRoot string
        BackupRoot  string
        DataDir     string
}

// ok writes a success envelope.
func ok(c fiber.Ctx, data interface{}) error {
        return c.JSON(fiber.Map{"status": "ok", "data": data})
}

// fail writes an error envelope with i18n error_data.
func fail(c fiber.Ctx, status int, code string) error {
        lang := c.Locals("lang")
        langStr := "en"
        if l, ok := lang.(string); ok {
                langStr = l
        }
        return c.Status(status).JSON(fiber.Map{
                "status":     "error",
                "error":      code,
                "error_data": i18n.Translate(langStr, code),
        })
}

// failMsg writes an error envelope with a custom message.
func failMsg(c fiber.Ctx, status int, code, msg string) error {
        return c.Status(status).JSON(fiber.Map{
                "status": "error", "error": code, "error_data": msg,
        })
}

// parseBody decodes JSON into v (INVALID_JSON / INVALID_JSON_SCHEMA on error).
func parseBody(c fiber.Ctx, v interface{}) error {
        if len(c.Body()) == 0 {
                return failMsg(c, 400, "INVALID_JSON", "empty body")
        }
        if err := json.Unmarshal(c.Body(), v); err != nil {
                return failMsg(c, 400, "INVALID_JSON", err.Error())
        }
        return nil
}

// currentUser resolves the authenticated principal (nil when anonymous).
func (a *App) currentUser(c fiber.Ctx) *authz.Principal {
        if p, ok := c.Locals("principal").(*authz.Principal); ok && p != nil {
                return p
        }
        token := c.Query("token")
        if token == "" {
                h := c.Get("Authorization")
                if strings.HasPrefix(h, "Bearer ") {
                        token = strings.TrimPrefix(h, "Bearer ")
                }
        }
        if token == "" {
                token = c.Cookies("token")
        }
        if token == "" {
                return nil
        }
        p, err := a.Auth.PrincipalForBearer(token)
        if err != nil {
                return nil
        }
        c.Locals("principal", p)
        return p
}

// requireAuth returns the principal or writes 401.
func (a *App) requireAuth(c fiber.Ctx) *authz.Principal {
        p := a.currentUser(c)
        if p == nil || !p.Enabled {
                _ = fail(c, 401, "INVALID_TOKEN")
                return nil
        }
        return p
}

// requireSuperuser gates admin routes.
func (a *App) requireSuperuser(c fiber.Ctx) *authz.Principal {
        p := a.requireAuth(c)
        if p == nil {
                return nil
        }
        if !p.Superuser {
                _ = fail(c, 403, "NOT_AUTHORIZED")
                return nil
        }
        return p
}

// requireServerAccess loads the server id param and checks access + perm bit.
// Returns (serverID, supervisor-or-nil-for-remote, ok). Writes the error
// response on failure.
func (a *App) requireServer(c fiber.Ctx, perm int) (string, interface{}, bool) {
        p := a.requireAuth(c)
        if p == nil {
                return "", nil, false
        }
        serverID := c.Params("id")
        if serverID == "" {
                serverID = c.Params("server_id")
        }
        if !p.HasServerAccess(serverID) {
                _ = fail(c, 403, "NOT_AUTHORIZED")
                return "", nil, false
        }
        if perm >= 0 && !p.CanServer(serverID, perm) {
                _ = fail(c, 403, "NOT_AUTHORIZED")
                return "", nil, false
        }
        return serverID, nil, true
}

// Router builds the fiber app with every route group.
func (a *App) Router() *fiber.App {
        app := fiber.New(fiber.Config{
                AppName:   "Servy",
                BodyLimit: 512 << 20, // 512MB for chunked uploads
                ErrorHandler: func(c fiber.Ctx, err error) error {
                        code := http.StatusInternalServerError
                        if e, ok := err.(*fiber.Error); ok {
                                code = e.Code
                        }
                        if code == 500 {
                                buf := make([]byte, 1<<16)
                                n := runtime.Stack(buf, true)
                                fmt.Fprintf(os.Stderr, "PANIC in %s %s: %v\n%s\n", c.Method(), c.Path(), err, buf[:n])
                        }
                        return c.Status(code).JSON(fiber.Map{"status": "error", "error": "SERVER_ERROR", "error_data": err.Error()})
                },
        })
        app.Use(recover.New(recover.Config{
                EnableStackTrace: true,
                StackTraceHandler: func(c fiber.Ctx, e interface{}) {
                        buf := make([]byte, 1<<16)
                        n := runtime.Stack(buf, false)
                        fmt.Fprintf(os.Stderr, "PANIC ORIGIN %s %s: %v\n%s\n", c.Method(), c.Path(), e, buf[:n])
                },
        }))
        app.Use(cors.New(cors.Config{
                AllowOrigins: []string{"*"},
                AllowHeaders: []string{"Authorization", "Content-Type", "X-Api-Key"},
                AllowMethods: []string{"GET", "POST", "PATCH", "PUT", "DELETE", "OPTIONS"},
        }))

        api := app.Group("/api/v1")
        a.mountAuth(api)
        a.mountUsers(api)
        a.mountServers(api)
        a.mountFiles(api)
        a.mountBackups(api)
        a.mountPanel(api)

        // API index
        api.Get("/", func(c fiber.Ctx) error {
                return ok(c, fiber.Map{
                        "name":    "Servy",
                        "version": version.Version,
                        "api":     "v1",
                        "docs":    "/api/v1/openapi.json",
                })
        })
        api.Get("/openapi.json", a.handleOpenAPI)

        // websocket: auth middleware then upgrade (fasthttp/websocket — Fiber
        // v3 has no contrib websocket middleware; upgrade on the raw ctx)
        app.Get("/ws", a.wsUpgrade, a.handleWSUpgrade)
        app.Get("/ws/", a.wsUpgrade, a.handleWSUpgrade)

        // public status (uptime widgets)
        app.Get("/status", a.handlePublicStatus)

        // metrics
        app.Get("/metrics", func(c fiber.Ctx) error {
                c.Set(fiber.HeaderContentType, "text/plain; charset=utf-8")
                return c.SendString(a.Metrics.Gather(""))
        })
        app.Get("/metrics/servers/:id", func(c fiber.Ctx) error {
                c.Set(fiber.HeaderContentType, "text/plain; charset=utf-8")
                return c.SendString(a.Metrics.Gather(c.Params("id")))
        })

        // announcements are public-ish (fetched for login screen)
        app.Get("/api/v1/public/announcements", a.handlePublicAnnouncements)

        // SPA static (embedded frontend build), served by Fiber's static
        // middleware — the documented pattern for go:embed'ed UIs.
        mountSPA(app)
        return app
}

// wsFastHTTPUpgrader upgrades panel websocket connections.
var wsFastHTTPUpgrader = websocket.FastHTTPUpgrader{
        CheckOrigin: func(ctx *fasthttp.RequestCtx) bool { return true },
}

// handleWSUpgrade bridges the Fiber v3 handler to the fasthttp websocket
// upgrader and hands the live connection to the realtime hub.
func (a *App) handleWSUpgrade(c fiber.Ctx) error {
        principal, _ := c.Locals("principal").(*authz.Principal)
        return wsFastHTTPUpgrader.Upgrade(c.RequestCtx(), func(conn *websocket.Conn) {
                a.Realtime.HandleWS(conn, principal)
        })
}

// mountSPA serves the embedded React build with an index.html fallback for
// client-side routes, following the Fiber static middleware docs for
// go:embed'ed UIs: FS serves the embedded assets, NotFoundHandler serves the
// SPA shell for deep links. Hashed build assets get immutable cache headers.
func mountSPA(app *fiber.App) {
	// Vite output is content-hashed → safe to cache forever.
	app.Use("/assets", func(c fiber.Ctx) error {
		c.Set(fiber.HeaderCacheControl, "public, max-age=31536000, immutable")
		return c.Next()
	})
	app.Use("/", static.New("", static.Config{
		FS:         spaFS{}, // lazy: reads the SPAFiles global per request
		IndexNames: []string{"index.html"},
		Next: func(c fiber.Ctx) bool {
			// never shadow API/metrics/ws/status
			p := c.Path()
			return strings.HasPrefix(p, "/api/") || strings.HasPrefix(p, "/metrics") ||
				p == "/status" || strings.HasPrefix(p, "/ws")
		},
		// SPA fallback: unknown paths get the app shell (client-side router
		// takes over). Headless/agent builds (no embedded UI) keep a clean 404.
		NotFoundHandler: func(c fiber.Ctx) error {
			if SPAFiles == nil {
				return fiber.ErrNotFound
			}
			return c.SendFile("index.html", fiber.SendFile{FS: spaFS{}})
		},
	}))
}

// Embed hook: cmd sets SPAFiles to the embedded frontend filesystem.
var SPAFiles fs.FS

// spaFS adapts the SPAFiles global to fs.FS lazily so the frontend can be
// wired before or after the router is built (and unset entirely in
// headless/agent deployments).
type spaFS struct{}

// Open implements fs.FS.
func (spaFS) Open(name string) (fs.File, error) {
	src := SPAFiles
	if src == nil {
		return nil, fs.ErrNotExist
	}
	return src.Open(name)
}

var _ = json.Marshal
