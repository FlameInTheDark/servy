-- name: CreateBackup :one
INSERT INTO backups (backup_id, created, backup_name, backup_location, excluded_dirs, max_backups, server_id,
  compress, shutdown, before, after, "default", status, enabled, backup_type)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: GetBackup :one
SELECT * FROM backups WHERE backup_id = ?;

-- name: ListBackupsForServer :many
SELECT * FROM backups WHERE server_id = ? ORDER BY created;

-- name: GetDefaultBackup :one
SELECT * FROM backups WHERE server_id = ? AND default = 1 LIMIT 1;

-- name: UpdateBackup :exec
UPDATE backups SET backup_name = ?, backup_location = ?, excluded_dirs = ?, max_backups = ?, compress = ?,
  shutdown = ?, before = ?, after = ?, default = ?, status = ?, enabled = ?, backup_type = ? WHERE backup_id = ?;

-- name: SetBackupStatus :exec
UPDATE backups SET status = ? WHERE backup_id = ?;

-- name: DeleteBackup :exec
DELETE FROM backups WHERE backup_id = ?;

-- name: DeleteBackupsForServer :exec
DELETE FROM backups WHERE server_id = ?;

-- name: UnsetDefaultBackups :exec
UPDATE backups SET default = 0 WHERE server_id = ?;

-- name: CreateWebhook :one
INSERT INTO webhooks (webhook_id, created, server_id, name, url, webhook_type, bot_name, trigger, body, color, enabled)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: GetWebhook :one
SELECT * FROM webhooks WHERE webhook_id = ?;

-- name: ListWebhooksForServer :many
SELECT * FROM webhooks WHERE server_id = ?;

-- name: UpdateWebhook :exec
UPDATE webhooks SET name = ?, url = ?, webhook_type = ?, bot_name = ?, trigger = ?, body = ?, color = ?, enabled = ? WHERE webhook_id = ?;

-- name: DeleteWebhook :exec
DELETE FROM webhooks WHERE webhook_id = ?;

-- name: DeleteWebhooksForServer :exec
DELETE FROM webhooks WHERE server_id = ?;

-- name: ListWebhooksByTrigger :many
SELECT * FROM webhooks WHERE server_id = ? AND enabled = 1;
