// Package cli defines the servy command-line interface (urfave/cli v3):
// serve, setup, user, migrate, import, agent, manifest, version.
package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/gofiber/fiber/v3"
	clipkg "github.com/urfave/cli/v3"

	"github.com/FlameInTheDark/servy/internal/authz"
	"github.com/FlameInTheDark/servy/internal/cluster"
	"github.com/FlameInTheDark/servy/internal/config"
	"github.com/FlameInTheDark/servy/internal/configedit"
	"github.com/FlameInTheDark/servy/internal/core"
	"github.com/FlameInTheDark/servy/internal/database"
	"github.com/FlameInTheDark/servy/internal/steamcmd"
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
			gameManifestCommand(),
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
			// optional cluster node API
			c := cfg.Get()
			if c.GRPCEnabled {
				go app.StartGRPC(fmt.Sprintf(":%d", c.GRPCPort))
				fmt.Printf("Cluster node API on :%d — add machines under Nodes → Add node\n", c.GRPCPort)
			} else {
				fmt.Println("Cluster nodes disabled — enable grpc_enabled to run servers on other machines")
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
		Usage: "run this machine as a cluster node (no web UI)",
		Commands: []*clipkg.Command{
			{
				Name:  "run",
				Usage: "join the panel (once) and serve servers for it",
				Flags: []clipkg.Flag{
					&clipkg.StringFlag{Name: "panel", Usage: "panel address host:port (defaults to the saved one)"},
					&clipkg.StringFlag{Name: "ticket", Usage: "join ticket issued by the panel (required on first run)"},
					&clipkg.StringFlag{Name: "name", Usage: "node name shown in the panel (defaults to hostname)"},
					&clipkg.StringFlag{Name: "advertise", Usage: "address the panel should dial (defaults to the address this agent reports)"},
					&clipkg.IntFlag{Name: "grpc-port", Value: 50052, Usage: "node API listen port"},
				},
				Action: func(ctx context.Context, cmd *clipkg.Command) error {
					_, app, err := loadConfigAndApp(cmd)
					if err != nil {
						return err
					}
					dataDir := app.Paths.DataDir
					creds, err := cluster.LoadCredentials(dataDir)
					if err != nil {
						return err
					}
					name := cmd.String("name")
					if name == "" {
						name, _ = os.Hostname()
					}
					panelAddr := cmd.String("panel")
					ticket := cmd.String("ticket")
					if ticket != "" {
						if panelAddr == "" && creds != nil {
							panelAddr = creds.PanelAddr
						}
						if panelAddr == "" {
							return fmt.Errorf("--panel is required to join")
						}
						fmt.Printf("Joining panel %s ...\n", panelAddr)
						fresh, err := cluster.Join(cluster.JoinOptions{
							PanelAddr: panelAddr, Ticket: ticket, Name: name,
							GRPCPort: cmd.Int("grpc-port"), Advertise: cmd.String("advertise"),
							Version: version.Version,
						})
						if err != nil {
							return err
						}
						if err := cluster.SaveCredentials(dataDir, fresh); err != nil {
							return err
						}
						creds = fresh
						fmt.Printf("Joined as node %q (%s). Credentials stored in %s\n",
							fresh.Name, fresh.NodeID, cluster.CredentialsPath(dataDir))
					} else if creds == nil {
						return fmt.Errorf("this node has not joined a panel yet: get a ticket in the panel UI (Nodes → Add node) and run `servy agent run --panel <host:port> --ticket <ticket>`")
					} else if panelAddr != "" && panelAddr != creds.PanelAddr {
						return fmt.Errorf("saved credentials belong to panel %s; pass --ticket to re-join %s", creds.PanelAddr, panelAddr)
					}
					if creds == nil {
						return fmt.Errorf("no node credentials")
					}

					if err := app.Start(); err != nil {
						return err
					}
					agent := cluster.NewAgent(cluster.AgentDeps{
						Deps: app.ClusterDeps(),
						Log:  app.Log, Bus: app.Bus,
						DataDir: dataDir, PanelAddr: creds.PanelAddr,
						Token: creds.Token, NodeID: creds.NodeID, NodeName: creds.Name,
						GRPCPort: cmd.Int("grpc-port"),
					})
					fmt.Printf("Servy agent %s — node %s (%s)\n", version.Version, creds.NodeID, creds.Name)
					fmt.Printf("Node API on :%d | panel %s | servers in %s\n",
						cmd.Int("grpc-port"), creds.PanelAddr, app.Paths.ServersDir)
					fmt.Println("Keep this process running: the panel manages its servers through it.")
					return agent.Run()
				},
			},
			{
				Name:  "status",
				Usage: "show this node's registration",
				Action: func(ctx context.Context, cmd *clipkg.Command) error {
					_, app, err := loadConfigAndApp(cmd)
					if err != nil {
						return err
					}
					creds, err := cluster.LoadCredentials(app.Paths.DataDir)
					if err != nil {
						return err
					}
					if creds == nil {
						fmt.Println("not joined to a panel")
						return nil
					}
					fmt.Printf("node:    %s\nname:    %s\npanel:   %s\njoined:  %s\n",
						creds.NodeID, creds.Name, creds.PanelAddr, creds.JoinedAt)
					return nil
				},
			},
			{
				Name:  "leave",
				Usage: "forget the saved panel credentials (re-join needs a new ticket)",
				Action: func(ctx context.Context, cmd *clipkg.Command) error {
					_, app, err := loadConfigAndApp(cmd)
					if err != nil {
						return err
					}
					path := cluster.CredentialsPath(app.Paths.DataDir)
					if err := os.Remove(path); err != nil {
						if os.IsNotExist(err) {
							fmt.Println("nothing to remove (node was never joined)")
							return nil
						}
						return err
					}
					fmt.Printf("Removed %s — ask the panel for a new ticket before restarting.\n", path)
					return nil
				},
			},
		},
	}
}

// ---------------- manifest ----------------

func manifestCommand() *clipkg.Command {
	return &clipkg.Command{
		Name:  "manifest",
		Usage: "manage server-creation recipe manifests",
		Commands: []*clipkg.Command{
			{
				Name:  "list",
				Usage: "list stored server-creation manifests",
				Action: func(ctx context.Context, cmd *clipkg.Command) error {
					_, app, err := loadConfigAndApp(cmd)
					if err != nil {
						return err
					}
					defs, err := app.DB.ListDefinitions()
					if err != nil {
						return err
					}
					for _, d := range defs {
						fmt.Printf("%s\t%s\t%s\n", d.DefinitionID, d.Name, d.Source)
					}
					return nil
				},
			},
			{
				Name:  "add",
				Usage: "validate and import a server-creation manifest from a URL or file",
				Flags: []clipkg.Flag{
					&clipkg.StringFlag{Name: "url", Usage: "remote server-creation manifest URL"},
					&clipkg.StringFlag{Name: "file", Usage: "local server-creation manifest (JSON)"},
				},
				Action: func(ctx context.Context, cmd *clipkg.Command) error {
					url, file := cmd.String("url"), cmd.String("file")
					if (url == "") == (file == "") {
						return fmt.Errorf("provide exactly one of --url or --file")
					}
					registry := configedit.NewRegistry()
					var manifest *configedit.Manifest
					var err error
					if url != "" {
						manifest, err = registry.FromURL(url)
					} else {
						var data []byte
						data, err = os.ReadFile(file)
						if err == nil {
							manifest, err = registry.ParseManifest(data)
						}
					}
					if err != nil {
						return err
					}
					_, app, err := loadConfigAndApp(cmd)
					if err != nil {
						return err
					}
					raw, err := json.Marshal(manifest)
						if err != nil {
							return err
						}
					source := "cli"
					if url != "" {
						source = "url"
					}
					manifestVersion := manifest.Version
					if manifestVersion == "" {
						manifestVersion = "1"
					}
					now := database.Now()
					empty := ""
					if err := app.DB.UpsertDefinition(manifest.ID, manifest.Name, manifest.Type, &empty,
						string(raw), source, &manifestVersion, now, now); err != nil {
						return err
					}
					fmt.Printf("Manifest %q stored as %s\n", manifest.Name, manifest.ID)
					return nil
				},
			},
		},
	}
}

// gameManifestCommand manages SteamCMD game definitions. Unlike `manifest`,
// these manifests are runtime catalog entries stored in config/steamgames.json.
func gameManifestCommand() *clipkg.Command {
	return &clipkg.Command{
		Name:  "game-manifest",
		Usage: "manage SteamCMD game-server manifests",
		Commands: []*clipkg.Command{
			{
				Name:  "list",
				Usage: "list built-in and custom SteamCMD game definitions",
				Action: func(ctx context.Context, cmd *clipkg.Command) error {
					paths := core.ResolvePaths(dataDirOf(cmd))
					catalog, err := steamcmd.LoadCatalog(filepath.Join(paths.ConfigDir, "steamgames.json"))
						if err != nil {
							return err
						}
					fmt.Printf("%-8s %-28s %-12s %-6s %s\n", "SOURCE", "SLUG", "APP ID", "PORT", "NAME")
					for _, game := range catalog.All() {
						source := "builtin"
						if catalog.IsCustom(game.Slug) {
							source = "custom"
						}
						fmt.Printf("%-8s %-28s %-12d %-6d %s\n", source, game.Slug, game.AppID, game.Port, game.Name)
					}
					return nil
				},
			},
			{
				Name:  "validate",
				Usage: "validate a YAML or JSON SteamCMD game manifest",
				Flags: []clipkg.Flag{
					&clipkg.StringFlag{Name: "file", Required: true, Usage: "manifest file path"},
				},
				Action: func(ctx context.Context, cmd *clipkg.Command) error {
					data, err := os.ReadFile(cmd.String("file"))
					if err != nil {
						return err
					}
					game, err := steamcmd.ParseGameManifest(data)
					if err != nil {
						return err
					}
					fmt.Printf("Valid game manifest: %s (%s, Steam app %d)\n", game.Name, game.Slug, game.AppID)
					return nil
				},
			},
			{
				Name:  "add",
				Usage: "add or replace a custom manifest in the active catalog",
				Flags: []clipkg.Flag{
					&clipkg.StringFlag{Name: "file", Required: true, Usage: "manifest file path (YAML or JSON)"},
				},
				Action: func(ctx context.Context, cmd *clipkg.Command) error {
					data, err := os.ReadFile(cmd.String("file"))
					if err != nil {
						return err
					}
					game, err := steamcmd.ParseGameManifest(data)
					if err != nil {
						return err
					}
					paths := core.ResolvePaths(dataDirOf(cmd))
					catalogPath := filepath.Join(paths.ConfigDir, "steamgames.json")
					catalog, err := steamcmd.LoadCatalog(catalogPath)
					if err != nil {
						return err
					}
					if err := catalog.Upsert(game); err != nil {
						return err
					}
					fmt.Printf("Game manifest %q added to %s\n", game.Slug, catalogPath)
					return nil
				},
			},
			{
				Name:  "remove",
				Usage: "remove a custom game manifest (built-ins cannot be removed)",
				Flags: []clipkg.Flag{
					&clipkg.StringFlag{Name: "slug", Required: true, Usage: "custom manifest slug"},
				},
				Action: func(ctx context.Context, cmd *clipkg.Command) error {
					paths := core.ResolvePaths(dataDirOf(cmd))
					catalog, err := steamcmd.LoadCatalog(filepath.Join(paths.ConfigDir, "steamgames.json"))
					if err != nil {
						return err
					}
					if err := catalog.DeleteCustom(cmd.String("slug")); err != nil {
						return err
					}
					fmt.Printf("Custom game manifest %q removed\n", cmd.String("slug"))
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
		Usage: "cluster node utilities (panel side)",
		Commands: []*clipkg.Command{
			{
				Name:  "list",
				Usage: "list joined nodes of this panel",
				Action: func(ctx context.Context, cmd *clipkg.Command) error {
					_, app, err := loadConfigAndApp(cmd)
					if err != nil {
						return err
					}
					rows, err := app.DB.ListNodes()
					if err != nil {
						return err
					}
					if len(rows) == 0 {
						fmt.Println("no nodes joined yet — add one from the panel UI (Nodes → Add node)")
						return nil
					}
					fmt.Printf("%-22s %-20s %-18s %-10s %s\n", "NODE ID", "NAME", "ADDRESS", "STATUS", "LAST SEEN")
					for _, n := range rows {
						fmt.Printf("%-22s %-20s %-18s %-10s %s\n", n.NodeID, n.Name, n.Address, n.Status, derefStr(n.LastSeen))
					}
					return nil
				},
			},
			{
				Name:  "whoami",
				Usage: "show the agent credentials stored in this data dir",
				Action: func(ctx context.Context, cmd *clipkg.Command) error {
					_, app, err := loadConfigAndApp(cmd)
					if err != nil {
						return err
					}
					creds, err := cluster.LoadCredentials(app.Paths.DataDir)
					if err != nil {
						return err
					}
					if creds == nil {
						fmt.Println("this data dir is not joined to a panel")
						return nil
					}
					fmt.Printf("node %s (%s) → panel %s\n", creds.NodeID, creds.Name, creds.PanelAddr)
					return nil
				},
			},
		},
	}
}

func derefStr(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// ---------------- helpers ----------------

// systemPrincipal is a synthetic superuser principal for CLI ops.
func systemPrincipal() *authz.Principal {
	return &authz.Principal{Superuser: true, Username: "cli", Enabled: true}
}
