-- name: CreateSchedule :one
INSERT INTO schedules (server_id, enabled, action, interval, interval_int, start_time, command, action_id,
  name, one_time, cron_string, parent, delay, next_run, last_run, comment)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: GetSchedule :one
SELECT * FROM schedules WHERE schedule_id = ?;

-- name: ListSchedulesForServer :many
SELECT * FROM schedules WHERE server_id = ? ORDER BY schedule_id;

-- name: ListEnabledSchedules :many
SELECT * FROM schedules WHERE enabled = 1 AND parent IS NULL;

-- name: ListChildSchedules :many
SELECT * FROM schedules WHERE parent = ?;

-- name: UpdateSchedule :exec
UPDATE schedules SET enabled = ?, action = ?, interval = ?, interval_int = ?, start_time = ?, command = ?,
  action_id = ?, name = ?, one_time = ?, cron_string = ?, parent = ?, delay = ?, next_run = ?, last_run = ?, comment = ?
WHERE schedule_id = ?;

-- name: SetScheduleNextRun :exec
UPDATE schedules SET next_run = ?, last_run = ? WHERE schedule_id = ?;

-- name: SetScheduleEnabled :exec
UPDATE schedules SET enabled = ? WHERE schedule_id = ?;

-- name: DeleteSchedule :exec
DELETE FROM schedules WHERE schedule_id = ?;

-- name: DeleteSchedulesForServer :exec
DELETE FROM schedules WHERE server_id = ?;

-- name: CountEnabledSchedulesForServer :one
SELECT COUNT(*) as count FROM schedules WHERE server_id = ? AND enabled = 1;
