// Package cli defines the servy command-line interface (urfave/cli v3):
// serve, setup, user, migrate, import, agent, manifest, version.
package cli

import (
	"context"
	"fmt"
	"os"
	"strings"

	clipkg "github.com/urfave/cli/v3"
	"github.com/gofiber/fiber/v3"

	"github.com/FlameInTheDark/servy/internal/authz"
	"github.com/FlameInTheDark/servy/internal/cluster"
	"github.com/FlameInTheDark/servy/internal/config"
	"github.com/FlameInTheDark/servy/internal/core"
	"github.com/FlameInTheDark/servy/internal/database"
	"github.com/FlameInTheDark/servy/internal/version"
)

// Root builds the root command.
func Root() *clipkg.Command {
	return &clipkg.Command{
		Name:    "servy",
		Usage:   "Servy — game server management panel",
		Version: version.Version,
		Flags: []clipkg.Flag{
			&clipkg.StringFlag{Name: "data", Aliases: []string{"d"}, Usage: "data directory (config, db, servers, backups)", Value: ""},
		},
		Commands: []*clipkg.Command{
			serveCommand(),
			{
				Name:  "version",
				Usage: "print build information (version, commit, date)",
				Action: func(ctx context.Context, cmd *clipkg.Command) error {
					fmt.Printf("servy %s (commit %s, built %s)\n", version.Version, version.Commit, version.BuildDate)
					return nil
				},
			},
			setupCommand(),
			userCommand(),
			migrateCommand(),
			importCommand(),
			agentCommand(),
			manifestCommand(),
			nodeCommand(),
		},
	}
}

func dataDirOf(cmd *clipkg.Command) string {
	d := cmd.String("data")
	if d == "" {
		d = os.Getenv("SERVY_DATA_DIR")
	}
	return d
}

func loadConfigAndApp(cmd *clipkg.Command) (*config.Manager, *core.App, error) {
	paths := core.ResolvePaths(dataDirOf(cmd))
	cfg, err := config.Load(paths.ConfigDir + "/config.yaml")
	if err != nil {
		return nil, nil, err
	}
	app, err := core.New(cfg, core.Options{DataDir: dataDirOf(cmd)})
	if err != nil {
		return nil, nil, err
	}
	return cfg, app, nil
}

// ---------------- serve ----------------

func serveCommand() *clipkg.Command {
	return &clipkg.Command{
		Name:  "serve",
		Usage: "run the web panel + API",
		Flags: []clipkg.Flag{
			&clipkg.IntFlag{Name: "port", Usage: "override HTTP port"},
			&clipkg.BoolFlag{Name: "no-autostart", Usage: "skip autostarting servers"},
		},
		Action: func(ctx context.Context, cmd *clipkg.Command) error {
			cfg, app, err := loadConfigAndApp(cmd)
			if err != nil {
				return err
			}
			if p := cmd.Int("port"); p > 0 {
				c := cfg.Get()
				c.HTTPPort = p
				cfg.Set(c)
			}
			if err := app.Start(); err != nil {
				return err
			}
			// first-run guidance
			if app.Auth.CountUsers() == 0 {
				app.Log.Get("auth").Warning("no users found — open the setup wizard at /setup or run `servy setup`")
			}
			// optional cluster gRPC
			c := cfg.Get()
			if c.GRPCEnabled {
				go app.StartGRPC(fmt.Sprintf(":%d", c.GRPCPort))
			}
			addr := fmt.Sprintf("%s:%d", c.HTTPHost, c.HTTPPort)
			fmt.Printf("Servy %s — panel listening on http://%s\n", version.Version, addr)
			fmt.Printf("Data dir: %s | API: http://%s/api/v1 | WS: ws://%s/ws\n", app.Paths.DataDir, addr, addr)
			return app.API.Router().Listen(addr, fiber.ListenConfig{DisableStartupMessage: true})
		},
	}
}

// ---------------- setup ----------------

func setupCommand() *clipkg.Command {
	return &clipkg.Command{
		Name:  "setup",
		Usage: "initialize the panel (first admin user)",
		Flags: []clipkg.Flag{
			&clipkg.StringFlag{Name: "username", Aliases: []string{"u"}, Usage: "admin username"},
			&clipkg.StringFlag{Name: "password", Aliases: []string{"p"}, Usage: "admin password (min 6 chars)"},
			&clipkg.StringFlag{Name: "lang", Value: "en_EN", Usage: "default language"},
		},
		Action: func(ctx context.Context, cmd *clipkg.Command) error {
			_, app, err := loadConfigAndApp(cmd)
			if err != nil {
				return err
			}
			if app.Auth.CountUsers() > 0 {
				return fmt.Errorf("panel already initialized (%d users); use `servy user add`", app.Auth.CountUsers())
			}
			username := cmd.String("username")
			password := cmd.String("password")
			if username == "" || password == "" {
				username, password = promptCredentials()
			}
			u, err := app.Auth.SeedFirstSetup(username, password, cmd.String("lang"))
			if err != nil {
				return err
			}
			fmt.Printf("Created superuser %q (id %d). Run `servy serve` to start the panel.\n", u.Username, u.UserID)
			return nil
		},
	}
}

func promptCredentials() (string, string) {
	var username, password string
	fmt.Print("Admin username: ")
	fmt.Scanln(&username)
	for len(password) < 6 {
		fmt.Print("Admin password (min 6 chars): ")
		fmt.Scanln(&password)
		if len(password) < 6 {
			fmt.Println("Password too short")
		}
	}
	return username, password
}

// ---------------- user ----------------

func userCommand() *clipkg.Command {
	return &clipkg.Command{
		Name:  "user",
		Usage: "manage users",
		Commands: []*clipkg.Command{
			{
				Name:  "add",
				Usage: "add a user",
				Flags: []clipkg.Flag{
					&clipkg.StringFlag{Name: "username", Aliases: []string{"u"}, Required: true},
					&clipkg.StringFlag{Name: "password", Aliases: []string{"p"}, Required: true},
					&clipkg.BoolFlag{Name: "superuser", Aliases: []string{"s"}},
				},
				Action: func(ctx context.Context, cmd *clipkg.Command) error {
					_, app, err := loadConfigAndApp(cmd)
					if err != nil {
						return err
					}
					p := systemPrincipal()
					u, err := app.Auth.CreateUser(p, cmd.String("username"), cmd.String("password"), "", cmd.Bool("superuser"), true, "en_EN")
					if err != nil {
						return err
					}
					if cmd.Bool("superuser") {
						app.DB.UpsertUserPanel(u.UserID, "111", -1, -1, -1)
					}
					fmt.Printf("User %q created (id %d)\n", u.Username, u.UserID)
					return nil
				},
			},
			{
				Name:  "list",
				Usage: "list users",
				Action: func(ctx context.Context, cmd *clipkg.Command) error {
					_, app, err := loadConfigAndApp(cmd)
					if err != nil {
						return err
					}
					fmt.Printf("%-5s %-20s %-6s %-8s %s\n", "ID", "USERNAME", "SUPER", "ENABLED", "CREATED")
					users, _ := app.DB.ListUsers()
					for _, u := range users {
						fmt.Printf("%-5d %-20s %-6v %-8v %s\n", u.UserID, u.Username, u.Superuser == 1, u.Enabled == 1, u.Created)
					}
					return nil
				},
			},
			{
				Name:  "passwd",
				Usage: "change a password",
				Flags: []clipkg.Flag{
					&clipkg.StringFlag{Name: "username", Aliases: []string{"u"}, Required: true},
					&clipkg.StringFlag{Name: "password", Aliases: []string{"p"}, Required: true},
				},
				Action: func(ctx context.Context, cmd *clipkg.Command) error {
					_, app, err := loadConfigAndApp(cmd)
					if err != nil {
						return err
					}
					u, err := app.DB.GetUserByUsername(cmd.String("username"))
					if err != nil {
						return fmt.Errorf("user not found")
					}
					if err := app.Auth.SetPassword(nil, u.UserID, cmd.String("password")); err != nil {
						return err
					}
					fmt.Println("Password changed; tokens invalidated")
					return nil
				},
			},
		},
	}
}

// ---------------- migrate ----------------

func migrateCommand() *clipkg.Command {
	return &clipkg.Command{
		Name:  "migrate",
		Usage: "database migrations",
		Commands: []*clipkg.Command{
			{
				Name:  "status",
				Usage: "show migration state",
				Action: func(ctx context.Context, cmd *clipkg.Command) error {
					paths := core.ResolvePaths(dataDirOf(cmd))
					db, err := database.Open(paths.DBPath)
					if err != nil {
						return err
					}
					defer db.Close()
					fmt.Println("database up to date at", paths.DBPath)
					return nil
				},
			},
		},
	}
}

// ---------------- import ----------------

func importCommand() *clipkg.Command {
	return &clipkg.Command{
		Name:  "import",
		Usage: "import from a legacy panel installation",
		Flags: []clipkg.Flag{
			&clipkg.StringFlag{Name: "db", Required: true, Usage: "path to the legacy panel sqlite database"},
			&clipkg.StringFlag{Name: "config-dir", Usage: "legacy app/config directory (stats DBs)"},
		},
		Action: func(ctx context.Context, cmd *clipkg.Command) error {
			cfg, app, err := loadConfigAndApp(cmd)
			if err != nil {
				return err
			}
			_ = cfg
			report, err := database.ImportLegacyPanel(app.DB, app.Log, cmd.String("db"), cmd.String("config-dir"))
			if err != nil {
				return err
			}
			fmt.Printf("Import complete: %d users, %d roles, %d servers, %d schedules, %d backups, %d webhooks, %d totp, %d api keys\n",
				report.Users, report.Roles, report.Servers, report.Schedules, report.Backups, report.Webhooks, report.TOTP, report.APIKeys)
			for _, skipped := range report.Skipped {
				fmt.Println("skipped:", skipped)
			}
			if len(report.NewAPIKeys) > 0 {
				fmt.Println("\nRe-issued API keys (store them now — shown once):")
				for _, k := range report.NewAPIKeys {
					fmt.Printf("  %s: %s\n", k["name"], k["token"])
				}
			}
			return nil
		},
	}
}

// ---------------- agent ----------------

func agentCommand() *clipkg.Command {
	return &clipkg.Command{
		Name:  "agent",
		Usage: "run as headless cluster agent (no web UI)",
		Flags: []clipkg.Flag{
			&clipkg.StringFlag{Name: "panel", Required: true, Usage: "panel gRPC address (host:port)"},
			&clipkg.StringFlag{Name: "token", Required: true, Usage: "cluster shared token"},
			&clipkg.IntFlag{Name: "grpc-port", Value: 50052, Usage: "local gRPC listen port"},
			&clipkg.StringFlag{Name: "name", Value: "", Usage: "node name (defaults to hostname)"},
		},
		Action: func(ctx context.Context, cmd *clipkg.Command) error {
			cfg, app, err := loadConfigAndApp(cmd)
			if err != nil {
				return err
			}
			_ = cfg
			name := cmd.String("name")
			if name == "" {
				name, _ = os.Hostname()
			}
			if err := app.Start(); err != nil {
				return err
			}
			agent := cluster.NewAgent(cluster.AgentDeps{
				DB: app.DB, Log: app.Log, Bus: app.Bus, Registry: app.Registry,
				HostStats: app.HostStats, PanelAddr: cmd.String("panel"),
				Token: cmd.String("token"), NodeName: name, GRPCPort: cmd.Int("grpc-port"),
			})
			fmt.Printf("Servy agent %s joining panel at %s (grpc :%d)\n", version.Version, cmd.String("panel"), cmd.Int("grpc-port"))
			return agent.Run()
		},
	}
}

// ---------------- manifest ----------------

func manifestCommand() *clipkg.Command {
	return &clipkg.Command{
		Name:  "manifest",
		Usage: "manage server definition manifests",
		Commands: []*clipkg.Command{
			{
				Name:  "list",
				Usage: "list stored manifests",
				Action: func(ctx context.Context, cmd *clipkg.Command) error {
					_, app, err := loadConfigAndApp(cmd)
					if err != nil {
						return err
					}
					defs, _ := app.DB.ListDefinitions()
					for _, d := range defs {
						fmt.Printf("%s\t%s\t%s\n", d.DefinitionID, d.Name, d.Source)
					}
					return nil
				},
			},
			{
				Name:  "add",
				Usage: "import a manifest from a URL or file",
				Flags: []clipkg.Flag{
					&clipkg.StringFlag{Name: "url", Usage: "remote manifest URL"},
					&clipkg.StringFlag{Name: "file", Usage: "local manifest JSON"},
				},
				Action: func(ctx context.Context, cmd *clipkg.Command) error {
					_, app, err := loadConfigAndApp(cmd)
					if err != nil {
						return err
					}
					// reuse the API service through httpapi? — direct: parse then store
					var data []byte
					if u := cmd.String("url"); u != "" {
						dl, err := downloadTo(u)
						if err != nil {
							return err
						}
						data = dl
					} else if f := cmd.String("file"); f != "" {
						data, err = os.ReadFile(f)
						if err != nil {
							return err
						}
					} else {
						return fmt.Errorf("provide --url or --file")
					}
					// store raw; validation happens in the API layer
					var m struct {
						ID   string `json:"id"`
						Name string `json:"name"`
						Type string `json:"type"`
					}
					if err := jsonUnmarshal(data, &m); err != nil {
						return err
					}
					if m.ID == "" {
						m.ID = strings.ToLower(strings.ReplaceAll(m.Name, " ", "-"))
					}
					if m.Type == "" {
						m.Type = "minecraft-java"
					}
					now := database.Now()
					mver := "1"
					empty := ""
					emptyP := &empty
					if err := app.DB.UpsertDefinition(m.ID, m.Name, m.Type, emptyP, string(data), "cli", &mver, now, now); err != nil {
						return err
					}
					fmt.Printf("Manifest %q stored as %s\n", m.Name, m.ID)
					return nil
				},
			},
		},
	}
}

// ---------------- node ----------------

func nodeCommand() *clipkg.Command {
	return &clipkg.Command{
		Name:  "node",
		Usage: "cluster node utilities",
		Commands: []*clipkg.Command{
			{
				Name:  "hello",
				Usage: "test connectivity to a peer node",
				Flags: []clipkg.Flag{
					&clipkg.StringFlag{Name: "addr", Required: true},
					&clipkg.StringFlag{Name: "token", Value: ""},
				},
				Action: func(ctx context.Context, cmd *clipkg.Command) error {
					c := cluster.NewClient(cmd.String("token"))
					if err := c.Hello(cmd.String("addr"), "probe", "cli", version.Version, "panel", nil); err != nil {
						return err
					}
					fmt.Println("peer accepted hello")
					return nil
				},
			},
		},
	}
}

// ---------------- helpers ----------------

// systemPrincipal is a synthetic superuser principal for CLI ops.
func systemPrincipal() *authz.Principal {
	return &authz.Principal{Superuser: true, Username: "cli", Enabled: true}
}

func downloadTo(url string) ([]byte, error) {
	return fetchURL(url)
}
