-- name: InsertAudit :exec
INSERT INTO audit_log (created, user_name, user_id, server_id, source_ip, log_msg) VALUES (?, ?, ?, ?, ?, ?);

-- name: ListAudit :many
SELECT * FROM audit_log ORDER BY audit_id DESC LIMIT ? OFFSET ?;

-- name: ListAuditForServer :many
SELECT * FROM audit_log WHERE server_id = ? ORDER BY audit_id DESC LIMIT ? OFFSET ?;

-- name: ListAuditForUser :many
SELECT * FROM audit_log WHERE user_id = ? ORDER BY audit_id DESC LIMIT ? OFFSET ?;

-- name: DeleteAuditOlder :exec
DELETE FROM audit_log WHERE created < ?;

-- name: CreateAPIKey :one
INSERT INTO api_keys (name, created, user_id, server_permissions, panel_permissions, full_access, token_hash, token_prefix)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: GetAPIKey :one
SELECT * FROM api_keys WHERE token_id = ?;

-- name: GetAPIKeyByPrefix :one
SELECT * FROM api_keys WHERE token_prefix = ?;

-- name: ListAPIKeysForUser :many
SELECT * FROM api_keys WHERE user_id = ? ORDER BY created DESC;

-- name: UpdateAPIKey :exec
UPDATE api_keys SET name = ?, server_permissions = ?, panel_permissions = ?, full_access = ? WHERE token_id = ?;

-- name: DeleteAPIKey :exec
DELETE FROM api_keys WHERE token_id = ?;

-- name: CreateTOTP :one
INSERT INTO totp_data (id, name, user_id, totp_secret) VALUES (?, ?, ?, ?) RETURNING *;

-- name: ListTOTPForUser :many
SELECT * FROM totp_data WHERE user_id = ?;

-- name: GetTOTP :one
SELECT * FROM totp_data WHERE id = ?;

-- name: DeleteTOTP :exec
DELETE FROM totp_data WHERE id = ?;

-- name: DeleteTOTPForUser :exec
DELETE FROM totp_data WHERE user_id = ?;

-- name: InsertRecovery :exec
INSERT INTO totp_recovery (id, user_id, recovery_secret) VALUES (?, ?, ?);

-- name: ListRecoveryForUser :many
SELECT * FROM totp_recovery WHERE user_id = ?;

-- name: DeleteRecoveryForUser :exec
DELETE FROM totp_recovery WHERE user_id = ?;

-- name: CreatePasskey :exec
INSERT INTO passkey_data (id, name, user_id, credential_id, public_key, sign_count, transports, device_type, backed_up, created_at, last_used_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: ListPasskeysForUser :many
SELECT * FROM passkey_data WHERE user_id = ?;

-- name: GetPasskeyByCredential :one
SELECT * FROM passkey_data WHERE credential_id = ?;

-- name: UpdatePasskeyUsage :exec
UPDATE passkey_data SET sign_count = ?, last_used_at = ? WHERE id = ?;

-- name: DeletePasskey :exec
DELETE FROM passkey_data WHERE id = ?;

-- name: InsertPasskeyChallenge :exec
INSERT INTO passkey_challenges (id, user_id, challenge, challenge_type, created_at, expires_at) VALUES (?, ?, ?, ?, ?, ?);

-- name: ListPasskeyChallenges :many
SELECT * FROM passkey_challenges WHERE user_id = ? AND challenge_type = ? AND expires_at >= ?;

-- name: DeletePasskeyChallenges :exec
DELETE FROM passkey_challenges WHERE expires_at < ?;
