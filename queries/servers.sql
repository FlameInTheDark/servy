-- name: CreateServer :one
INSERT INTO servers (server_id, created, last_update, server_name, path, executable, log_path, execution_command,
  auto_start, auto_start_delay, crash_detection, stop_command, executable_update_url, server_ip, server_port,
  logs_delete_after, type, show_status, created_by, shutdown_timeout, ignored_exits, app_id, count_players,
  update_watcher, stop_action, io_mode, node_id, definitions,
  java_selection, execution_backend, docker_image, docker_env, docker_ports, docker_binds, docker_network,
  docker_workdir, docker_autoremove, shell)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?,
  ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: GetServer :one
SELECT * FROM servers WHERE server_id = ?;

-- name: ListServers :many
SELECT * FROM servers ORDER BY server_name COLLATE NOCASE;

-- name: ListServersByNode :many
SELECT * FROM servers WHERE node_id = ? ORDER BY server_name COLLATE NOCASE;

-- name: UpdateServerConfig :exec
UPDATE servers SET last_update = ?, server_name = ?, path = ?, executable = ?, log_path = ?, execution_command = ?,
  auto_start = ?, auto_start_delay = ?, crash_detection = ?, stop_command = ?, executable_update_url = ?, server_ip = ?,
  server_port = ?, logs_delete_after = ?, type = ?, show_status = ?, shutdown_timeout = ?, ignored_exits = ?,
  app_id = ?, count_players = ?, update_watcher = ?, stop_action = ?, io_mode = ?, definitions = ?,
  java_selection = ?, execution_backend = ?, docker_image = ?, docker_env = ?, docker_ports = ?, docker_binds = ?,
  docker_network = ?, docker_workdir = ?, docker_autoremove = ?, shell = ? WHERE server_id = ?;

-- name: SetServerNode :exec
UPDATE servers SET node_id = ?, last_update = ? WHERE server_id = ?;

-- name: SetServerExecutable :exec
UPDATE servers SET executable = ?, executable_update_url = ?, last_update = ? WHERE server_id = ?;

-- name: DeleteServer :exec
DELETE FROM servers WHERE server_id = ?;

-- name: CountServers :one
SELECT COUNT(*) as count FROM servers;

-- name: CountServersForUser :one
SELECT COUNT(DISTINCT rs.server_id) as count
FROM role_servers rs JOIN user_roles ur ON ur.role_id = rs.role_id WHERE ur.user_id = ?;

-- name: ListServersForUser :many
SELECT s.* FROM servers s
JOIN role_servers rs ON rs.server_id = s.server_id
JOIN user_roles ur ON ur.role_id = rs.role_id
WHERE ur.user_id = ?
GROUP BY s.server_id
ORDER BY s.server_name COLLATE NOCASE;
