-- servy baseline schema (clean denormalized form)
-- Derived from the legacy panel target schema with documented breaking changes (docs/02-database-schema.md §3.3):
--  * UUID primary keys for servers/backups (text), int PKs kept for users/roles/tasks (import compat)
--  * server_stats collapsed into main DB (was per-server sqlite files)
--  * api_keys secret stored hashed
--  * new tables: nodes, node_servers, server_definitions (cluster + manifests)

CREATE TABLE users (
  user_id INTEGER PRIMARY KEY AUTOINCREMENT,
  created TEXT NOT NULL,
  last_login TEXT,
  last_update TEXT NOT NULL,
  last_ip TEXT,
  username TEXT NOT NULL UNIQUE COLLATE NOCASE,
  password TEXT NOT NULL,
  email TEXT,
  enabled INTEGER NOT NULL DEFAULT 1,
  superuser INTEGER NOT NULL DEFAULT 0,
  lang TEXT NOT NULL DEFAULT 'en',
  valid_tokens_from TEXT NOT NULL DEFAULT '2000-01-01T00:00:00Z',
  server_order TEXT,
  preparing INTEGER NOT NULL DEFAULT 0,
  hints TEXT,
  manager INTEGER,
  pfp TEXT,
  theme TEXT,
  cleared_notifs TEXT,
  disable_password_auth INTEGER NOT NULL DEFAULT 0,
  dashboard_columns INTEGER DEFAULT 4,
  preferences TEXT
);

CREATE TABLE roles (
  role_id INTEGER PRIMARY KEY AUTOINCREMENT,
  created TEXT NOT NULL,
  last_update TEXT NOT NULL,
  role_name TEXT NOT NULL UNIQUE COLLATE NOCASE,
  manager INTEGER,
  mfa_required INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE user_roles (
  user_id INTEGER NOT NULL REFERENCES users(user_id) ON DELETE CASCADE,
  role_id INTEGER NOT NULL REFERENCES roles(role_id) ON DELETE CASCADE,
  PRIMARY KEY (user_id, role_id)
);

CREATE TABLE servers (
  server_id TEXT PRIMARY KEY,
  created TEXT NOT NULL,
  last_update TEXT NOT NULL,
  server_name TEXT NOT NULL,
  path TEXT NOT NULL,
  executable TEXT NOT NULL,
  log_path TEXT NOT NULL DEFAULT 'server.log',
  execution_command TEXT NOT NULL,
  auto_start INTEGER NOT NULL DEFAULT 0,
  auto_start_delay INTEGER NOT NULL DEFAULT 10,
  crash_detection INTEGER NOT NULL DEFAULT 1,
  stop_command TEXT NOT NULL DEFAULT 'stop',
  executable_update_url TEXT,
  server_ip TEXT NOT NULL DEFAULT '127.0.0.1',
  server_port INTEGER NOT NULL DEFAULT 25565,
  logs_delete_after INTEGER NOT NULL DEFAULT 0,
  type TEXT NOT NULL DEFAULT 'minecraft-java',
  show_status INTEGER NOT NULL DEFAULT 1,
  created_by INTEGER,
  shutdown_timeout INTEGER NOT NULL DEFAULT 60,
  ignored_exits TEXT,
  app_id TEXT,
  count_players INTEGER NOT NULL DEFAULT 0,
  update_watcher INTEGER NOT NULL DEFAULT 0,
  stop_action TEXT NOT NULL DEFAULT 'command',
  io_mode TEXT NOT NULL DEFAULT 'pipes',
  node_id TEXT,
  definitions TEXT
);
CREATE INDEX idx_servers_node ON servers(node_id);

CREATE TABLE role_servers (
  role_id INTEGER NOT NULL REFERENCES roles(role_id) ON DELETE CASCADE,
  server_id TEXT NOT NULL REFERENCES servers(server_id) ON DELETE CASCADE,
  permissions TEXT NOT NULL DEFAULT '00000000',
  PRIMARY KEY (role_id, server_id)
);

CREATE TABLE server_stats (
  stat_id INTEGER PRIMARY KEY AUTOINCREMENT,
  server_id TEXT NOT NULL,
  created TEXT NOT NULL,
  started TEXT,
  running INTEGER NOT NULL DEFAULT 0,
  cpu REAL NOT NULL DEFAULT 0,
  mem INTEGER NOT NULL DEFAULT 0,
  mem_percent REAL NOT NULL DEFAULT 0,
  world_name TEXT,
  world_size INTEGER NOT NULL DEFAULT 0,
  server_port INTEGER,
  int_ping_results REAL NOT NULL DEFAULT 0,
  online INTEGER NOT NULL DEFAULT 0,
  max INTEGER NOT NULL DEFAULT 0,
  players TEXT,
  desc TEXT,
  version TEXT,
  updating INTEGER NOT NULL DEFAULT 0,
  waiting_start INTEGER NOT NULL DEFAULT 0,
  crashed INTEGER NOT NULL DEFAULT 0,
  importing INTEGER NOT NULL DEFAULT 0,
  icon TEXT
);
CREATE INDEX idx_server_stats_sid_created ON server_stats(server_id, created);

CREATE TABLE host_stats (
  stat_id INTEGER PRIMARY KEY AUTOINCREMENT,
  time TEXT NOT NULL,
  boot_time TEXT NOT NULL,
  cpu_usage REAL NOT NULL,
  cpu_cores INTEGER NOT NULL,
  cpu_cur_freq REAL NOT NULL,
  cpu_max_freq REAL NOT NULL,
  mem_percent REAL NOT NULL,
  mem_usage REAL NOT NULL,
  mem_total REAL NOT NULL,
  disk_json TEXT
);
CREATE INDEX idx_host_stats_time ON host_stats(time);

CREATE TABLE panel_settings (
  id INTEGER PRIMARY KEY CHECK (id = 1),
  secret_api_key TEXT,
  cookie_secret TEXT,
  login_photo TEXT,
  login_opacity REAL NOT NULL DEFAULT 1.0,
  logo_full TEXT,
  logo_square TEXT,
  og_enabled INTEGER NOT NULL DEFAULT 0,
  og_title TEXT,
  og_description TEXT,
  og_image TEXT,
  og_color TEXT,
  master_server_dir TEXT
);

CREATE TABLE schedules (
  schedule_id INTEGER PRIMARY KEY AUTOINCREMENT,
  server_id TEXT NOT NULL REFERENCES servers(server_id) ON DELETE CASCADE,
  enabled INTEGER NOT NULL DEFAULT 1,
  action TEXT NOT NULL DEFAULT 'start',
  interval TEXT NOT NULL DEFAULT 'hours',
  interval_int INTEGER NOT NULL DEFAULT 1,
  start_time TEXT,
  command TEXT,
  action_id TEXT,
  name TEXT,
  one_time INTEGER NOT NULL DEFAULT 0,
  cron_string TEXT,
  parent INTEGER,
  delay INTEGER NOT NULL DEFAULT 0,
  next_run TEXT,
  last_run TEXT,
  comment TEXT,
  FOREIGN KEY (parent) REFERENCES schedules(schedule_id) ON DELETE CASCADE
);
CREATE INDEX idx_schedules_server ON schedules(server_id);
CREATE INDEX idx_schedules_enabled ON schedules(enabled) WHERE enabled = 1;

CREATE TABLE backups (
  backup_id TEXT PRIMARY KEY,
  created TEXT NOT NULL,
  backup_name TEXT NOT NULL,
  backup_location TEXT NOT NULL,
  excluded_dirs TEXT,
  max_backups INTEGER NOT NULL DEFAULT 0,
  server_id TEXT NOT NULL REFERENCES servers(server_id) ON DELETE CASCADE,
  compress INTEGER NOT NULL DEFAULT 1,
  shutdown INTEGER NOT NULL DEFAULT 0,
  before TEXT,
  after TEXT,
  "default" INTEGER NOT NULL DEFAULT 0,
  status TEXT,
  enabled INTEGER NOT NULL DEFAULT 1,
  backup_type TEXT NOT NULL DEFAULT 'zip_vault'
);
CREATE INDEX idx_backups_server ON backups(server_id);

CREATE TABLE webhooks (
  webhook_id TEXT PRIMARY KEY,
  created TEXT NOT NULL,
  server_id TEXT NOT NULL REFERENCES servers(server_id) ON DELETE CASCADE,
  name TEXT NOT NULL,
  url TEXT NOT NULL,
  webhook_type TEXT NOT NULL DEFAULT 'Discord',
  bot_name TEXT,
  trigger TEXT,
  body TEXT,
  color TEXT,
  enabled INTEGER NOT NULL DEFAULT 1
);

CREATE TABLE audit_log (
  audit_id INTEGER PRIMARY KEY AUTOINCREMENT,
  created TEXT NOT NULL,
  user_name TEXT,
  user_id INTEGER,
  server_id TEXT,
  source_ip TEXT,
  log_msg TEXT NOT NULL
);
CREATE INDEX idx_audit_created ON audit_log(created);

CREATE TABLE api_keys (
  token_id INTEGER PRIMARY KEY AUTOINCREMENT,
  name TEXT NOT NULL,
  created TEXT NOT NULL,
  user_id INTEGER NOT NULL REFERENCES users(user_id) ON DELETE CASCADE,
  server_permissions TEXT NOT NULL DEFAULT '00000000',
  panel_permissions TEXT NOT NULL DEFAULT '000',
  full_access INTEGER NOT NULL DEFAULT 0,
  token_hash TEXT NOT NULL,
  token_prefix TEXT NOT NULL
);

CREATE TABLE totp_data (
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL,
  user_id INTEGER NOT NULL REFERENCES users(user_id) ON DELETE CASCADE,
  totp_secret TEXT NOT NULL
);

CREATE TABLE totp_recovery (
  id TEXT PRIMARY KEY,
  user_id INTEGER NOT NULL REFERENCES users(user_id) ON DELETE CASCADE,
  recovery_secret TEXT NOT NULL
);

CREATE TABLE passkey_data (
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL,
  user_id INTEGER NOT NULL REFERENCES users(user_id) ON DELETE CASCADE,
  credential_id TEXT NOT NULL UNIQUE,
  public_key BLOB NOT NULL,
  sign_count INTEGER NOT NULL DEFAULT 0,
  transports TEXT,
  device_type TEXT,
  backed_up INTEGER NOT NULL DEFAULT 0,
  created_at TEXT NOT NULL,
  last_used_at TEXT
);

CREATE TABLE passkey_challenges (
  id TEXT PRIMARY KEY,
  user_id INTEGER NOT NULL,
  challenge BLOB NOT NULL,
  challenge_type TEXT NOT NULL,
  created_at TEXT NOT NULL,
  expires_at TEXT NOT NULL
);

CREATE TABLE nodes (
  node_id TEXT PRIMARY KEY,
  name TEXT NOT NULL,
  address TEXT NOT NULL,
  role TEXT NOT NULL DEFAULT 'agent',
  status TEXT NOT NULL DEFAULT 'offline',
  last_seen TEXT,
  version TEXT,
  capabilities TEXT,
  cluster_token_hash TEXT,
  created TEXT NOT NULL
);

CREATE TABLE node_servers (
  node_id TEXT NOT NULL REFERENCES nodes(node_id) ON DELETE CASCADE,
  server_id TEXT NOT NULL,
  remote_name TEXT,
  PRIMARY KEY (node_id, server_id)
);

CREATE TABLE server_definitions (
  definition_id TEXT PRIMARY KEY,
  name TEXT NOT NULL,
  type TEXT NOT NULL,
  game TEXT,
  definition TEXT NOT NULL,
  source TEXT NOT NULL DEFAULT 'builtin',
  version TEXT,
  created TEXT NOT NULL,
  updated TEXT NOT NULL
);

CREATE TABLE user_panel (
  user_id INTEGER PRIMARY KEY REFERENCES users(user_id) ON DELETE CASCADE,
  permissions TEXT NOT NULL DEFAULT '000',
  limit_server_creation INTEGER NOT NULL DEFAULT -1,
  limit_user_creation INTEGER NOT NULL DEFAULT 0,
  limit_role_creation INTEGER NOT NULL DEFAULT 0
);
