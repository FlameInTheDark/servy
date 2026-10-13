/* Typed REST client for /api/v1 + WebSocket SDK */

const BASE = "/api/v1";

export class APIError extends Error {
  code: string;
  constructor(code: string, message: string) {
    super(message);
    this.code = code;
  }
}

let token: string | null = localStorage.getItem("servy_token");
export function setToken(t: string | null) {
  token = t;
  if (t) localStorage.setItem("servy_token", t);
  else localStorage.removeItem("servy_token");
}
export function getToken() {
  return token;
}

export interface Envelope<T> {
  status: "ok" | "error";
  data?: T;
  error?: string;
  error_data?: string;
}

export async function api<T = any>(
  method: string,
  path: string,
  body?: any,
  raw = false
): Promise<T> {
  const headers: Record<string, string> = {};
  if (body !== undefined) headers["Content-Type"] = "application/json";
  if (token) headers["Authorization"] = `Bearer ${token}`;
  const res = await fetch(BASE + path, {
    method,
    headers,
    body: body !== undefined ? JSON.stringify(body) : undefined,
  });
  if (raw) return res as any;
  let json: Envelope<T>;
  try {
    json = await res.json();
  } catch {
    throw new APIError("SERVER_ERROR", `HTTP ${res.status}`);
  }
  if (!res.ok || json.status === "error") {
    throw new APIError(json.error || `HTTP_${res.status}`, json.error_data || json.error || "request failed");
  }
  return json.data as T;
}

export const GET = <T = any>(p: string) => api<T>("GET", p);
export const POST = <T = any>(p: string, b?: any) => api<T>("POST", p, b ?? {});
export const PATCH = <T = any>(p: string, b?: any) => api<T>("PATCH", p, b ?? {});
export const PUT = <T = any>(p: string, b?: any) => api<T>("PUT", p, b ?? {});
export const DEL = <T = any>(p: string) => api<T>("DELETE", p);

/* ------------------------------ WebSocket SDK ------------------------------ */

export type WSMessage = {
  type: string;
  topic?: string;
  event?: string;
  data?: any;
};

type Handler = (msg: WSMessage) => void;

export class RealtimeClient {
  private ws: WebSocket | null = null;
  private handlers = new Set<Handler>();
  private topics = new Set<string>();
  private reconnectTimer: number | null = null;
  private closed = false;
  private connectionPromise: Promise<void> | null = null;
  private resolveConnection: (() => void) | null = null;

  connect() {
    this.connectionPromise = new Promise((resolve) => {
      this.resolveConnection = resolve;
    });
    const proto = location.protocol === "https:" ? "wss" : "ws";
    const url = token ? `${proto}://${location.host}/ws?token=${encodeURIComponent(token)}` : `${proto}://${location.host}/ws`;
    this.ws = new WebSocket(url);
    this.ws.onopen = () => {
      this.topics.forEach((t) => this.send({ type: "subscribe", topic: t }));
      if (this.resolveConnection) {
        this.resolveConnection();
        this.resolveConnection = null;
      }
    };
    this.ws.onmessage = (ev) => {
      try {
        const msg = JSON.parse(ev.data);
        this.handlers.forEach((h) => h(msg));
      } catch {}
    };
    this.ws.onclose = () => {
      if (!this.closed) {
        this.reconnectTimer = window.setTimeout(() => this.connect(), 3000);
      }
      // Reset connection promise on reconnect
      this.connectionPromise = new Promise((resolve) => {
        this.resolveConnection = resolve;
      });
    };
  }

  async waitForConnection(): Promise<void> {
    if (this.ws && this.ws.readyState === WebSocket.OPEN) {
      return;
    }
    if (this.connectionPromise) {
      await this.connectionPromise;
    }
  }

  send(msg: WSMessage) {
    if (this.ws && this.ws.readyState === WebSocket.OPEN) {
      this.ws.send(JSON.stringify(msg));
    }
  }

  subscribe(topic: string) {
    this.topics.add(topic);
    this.send({ type: "subscribe", topic });
  }

  unsubscribe(topic: string) {
    this.topics.delete(topic);
    this.send({ type: "unsubscribe", topic });
  }

  on(handler: Handler): () => void {
    this.handlers.add(handler);
    return () => this.handlers.delete(handler);
  }

  close() {
    this.closed = true;
    if (this.reconnectTimer) clearTimeout(this.reconnectTimer);
    this.ws?.close();
  }
}

let sharedClient: RealtimeClient | null = null;
export function realtime(): RealtimeClient {
  if (!sharedClient) {
    sharedClient = new RealtimeClient();
    sharedClient.connect();
  }
  return sharedClient;
}

/* --------------------------------- Types ----------------------------------- */

export interface ServerDef {
  server_id: string;
  created: string;
  server_name: string;
  type: string;
  app_id?: string;
  steam_manifest?: boolean;
  path?: string;
  executable?: string;
  execution_command?: string;
  stop_command?: string;
  // execution environment
  java_selection?: string;
  shell?: string;
  shell_default?: string;
  execution_backend?: string;
  docker_image?: string;
  docker_env?: Record<string, string>;
  docker_ports?: string[];
  docker_binds?: string[];
  docker_network?: string;
  docker_workdir?: string;
  docker_autoremove?: boolean;
  auto_start: boolean;
  auto_start_delay: number;
  crash_detection: boolean;
  shutdown_timeout: number;
  server_ip: string;
  server_port: number;
  io_mode: string;
  show_status: boolean;
  state: string;
  running: boolean;
  crashed: boolean;
  updating: boolean;
  importing: boolean;
  cpu: number;
  mem: number;
  mem_percent: number;
  online: number;
  max: number;
  players: string[];
  version: string;
  desc: string;
  world_size: number;
  int_ping_results: number;
  eula_pending: boolean;
  started_at?: string | null; // null when the server has never been started
  executable_update_url?: string;
  node_id?: string;
}

export interface UserView {
  user_id: number;
  username: string;
  email?: string;
  enabled: boolean;
  superuser: boolean;
  lang: string;
  created: string;
  last_login?: string;
  roles: number[];
  panel_permissions: string;
  limit_server_creation: number;
  limit_user_creation: number;
  limit_role_creation: number;
  server_permissions: Record<string, string>;
  pfp?: string;
}

export interface RoleView {
  role_id: number;
  role_name: string;
  created: string;
  servers: Record<string, string>;
}

export interface BackupConfig {
  backup_id: string;
  created: string;
  backup_name: string;
  backup_location: string;
  excluded_dirs: string[];
  max_backups: number;
  server_id: string;
  compress: boolean;
  shutdown: boolean;
  before?: string;
  after?: string;
  default: boolean;
  enabled: boolean;
  backup_type: string;
  status: Record<string, any>;
}

export interface TaskView {
  schedule_id: number;
  server_id: string;
  enabled: boolean;
  action: string;
  interval: string;
  interval_int: number;
  one_time: boolean;
  delay: number;
  name: string;
  comment: string;
  command: string;
  cron_string: string;
  start_time: string;
  action_id: string;
  parent: number | null;
  next_run: string;
  last_run: string;
}

export interface WebhookView {
  webhook_id: string;
  server_id: string;
  name: string;
  url: string;
  webhook_type: string;
  bot_name: string;
  trigger: string[];
  body: string;
  color: string;
  enabled: boolean;
}

export interface HistoryPoint {
  time: string;
  cpu: number;
  mem: number;
  mem_percent: number;
  online: number;
  max: number;
  int_ping_results: number;
  world_size: number;
  running: boolean;
}

/** One HOST telemetry sample — mirrors backend models.HostStatPoint
 *  (GET /panel/stats → data.history[]). Note: CPU is `cpu_usage`, NOT `cpu`. */
export interface HostHistoryPoint {
  time: string;
  boot_time: string;
  cpu_usage: number;
  cpu_cores: number;
  cpu_cur_freq: number;
  cpu_max_freq: number;
  mem_percent: number;
  mem_usage: number;
  mem_total: number;
  disk_json: { device: string; mount: string; total: number; used: number; free: number; percent_used: number }[];
}

export interface HostStatsSnapshot {
  boot_time: string;
  cpu_usage: number;
  cpu_cores: number;
  cpu_cur_freq: number;
  cpu_max_freq: number;
  mem_percent: number;
  mem_usage: number;
  mem_total: number;
  disk_json: { device: string; mount: string; total: number; used: number; free: number; percent_used: number }[];
  load_avg: number;
  os: string;
}

export interface ExposedGamePort {
  port?: number;
  proto?: "tcp" | "udp";
  optional?: boolean;
  offset?: number;
}

export interface SteamConfigEditor {
  path: string;
  label?: string;
  description?: string;
  editor?: "auto" | "raw" | "lines";
}

export interface SteamLaunchOptionValue {
  value: string;
  label: string;
}

export interface SteamLaunchOption {
  key: string;
  label: string;
  description?: string;
  type: "text" | "password" | "number" | "boolean" | "select";
  flag?: string;
  flag_separator?: string;
  source?: "server_name" | "port" | "query_port" | "save_dir";
  default?: string;
  required?: boolean;
  min?: number;
  max?: number;
  min_length?: number;
  max_length?: number;
  true_value?: string;
  false_value?: string;
  options?: SteamLaunchOptionValue[];
}

export interface SteamGame {
  app_id: number;
  name: string;
  slug: string;
  type?: "steam_cmd";
  run_command: string;
  executable: string;
  stop_command?: string;
  port: number;
  port_name?: string;
  workshop?: boolean;
  config_files?: string[];
  config_editors?: SteamConfigEditor[];
  launch_options?: SteamLaunchOption[];
  expose?: ExposedGamePort[];
  windows_only?: boolean;
  query_port?: number;
  platform?: "linux" | "windows";
  docker_image?: string;
  env?: Record<string, string[]>;
  save_dirs?: string[];
  backup_dirs?: string[];
  notes?: string;
  custom?: boolean;
}

export interface JavaInstall {
  path: string;
  home: string;
  version: string;
  major: number;
  vendor: string;
  source: string;
}

export interface FileEntry {
  name: string;
  dir: boolean;
  size: number;
  modified: string;
}
