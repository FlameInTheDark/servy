-- name: InsertServerStat :exec
INSERT INTO server_stats (server_id, created, started, running, cpu, mem, mem_percent, world_name, world_size,
  server_port, int_ping_results, online, max, players, desc, version, updating, waiting_start, crashed, importing, icon)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: GetLatestServerStat :one
SELECT * FROM server_stats WHERE server_id = ? ORDER BY created DESC LIMIT 1;

-- name: GetServerStatsSince :many
SELECT * FROM server_stats WHERE server_id = ? AND created >= ? ORDER BY created ASC;

-- name: GetServerStatsWindow :many
SELECT * FROM server_stats WHERE server_id = ? AND created >= ? AND created <= ? ORDER BY created ASC;

-- name: GetRecentServerStats :many
SELECT * FROM server_stats WHERE server_id = ? ORDER BY created DESC LIMIT ?;

-- name: DeleteServerStatsOlder :exec
DELETE FROM server_stats WHERE created < ?;

-- name: DeleteServerStatsForServer :exec
DELETE FROM server_stats WHERE server_id = ?;

-- name: InsertHostStat :exec
INSERT INTO host_stats (time, boot_time, cpu_usage, cpu_cores, cpu_cur_freq, cpu_max_freq, mem_percent, mem_usage, mem_total, disk_json)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: GetHostStatsSince :many
SELECT * FROM host_stats WHERE time >= ? ORDER BY time ASC;

-- name: DeleteHostStatsOlder :exec
DELETE FROM host_stats WHERE time < ?;
