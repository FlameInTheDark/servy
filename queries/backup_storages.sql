-- name: CreateBackupStorage :one
INSERT INTO backup_storages (storage_id, created, name, kind, prefix, settings, enabled, "default", cluster_id)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: GetBackupStorage :one
SELECT * FROM backup_storages WHERE storage_id = ?;

-- name: ListBackupStorages :many
SELECT * FROM backup_storages ORDER BY created;

-- name: GetDefaultBackupStorage :one
SELECT * FROM backup_storages WHERE "default" = 1 AND enabled = 1 LIMIT 1;

-- name: UpdateBackupStorage :exec
UPDATE backup_storages SET name = ?, kind = ?, prefix = ?, settings = ?, enabled = ?, "default" = ?, cluster_id = ?
  WHERE storage_id = ?;

-- name: DeleteBackupStorage :exec
DELETE FROM backup_storages WHERE storage_id = ?;

-- name: UnsetDefaultBackupStorages :exec
UPDATE backup_storages SET "default" = 0;

-- name: ListBackupsForStorage :many
SELECT * FROM backups WHERE storage_id = ?;
