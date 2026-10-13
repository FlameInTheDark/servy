-- name: CreateUser :one
INSERT INTO users (created, last_update, username, password, email, enabled, superuser, lang, valid_tokens_from, server_order, preparing, manager, pfp, theme, cleared_notifs, disable_password_auth, dashboard_columns, preferences)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: GetUserByID :one
SELECT * FROM users WHERE user_id = ?;

-- name: GetUserByUsername :one
SELECT * FROM users WHERE username = ? COLLATE NOCASE;

-- name: ListUsers :many
SELECT * FROM users ORDER BY username;

-- name: UpdateUser :exec
UPDATE users SET last_update = ?, username = ?, email = ?, enabled = ?, superuser = ?, lang = ?, server_order = ?, preparing = ?, manager = ?, pfp = ?, theme = ?, cleared_notifs = ?, disable_password_auth = ?, dashboard_columns = ?, preferences = ? WHERE user_id = ?;

-- name: UpdateUserPassword :exec
UPDATE users SET password = ?, valid_tokens_from = ?, last_update = ? WHERE user_id = ?;

-- name: TouchUserLogin :exec
UPDATE users SET last_login = ?, last_ip = ?, last_update = ? WHERE user_id = ?;

-- name: SetValidTokensFrom :exec
UPDATE users SET valid_tokens_from = ?, last_update = ? WHERE user_id = ?;

-- name: InvalidateAllTokens :exec
UPDATE users SET valid_tokens_from = ? WHERE user_id > 0;

-- name: DeleteUser :exec
DELETE FROM users WHERE user_id = ?;

-- name: CountSuperusers :one
SELECT COUNT(*) as count FROM users WHERE superuser = 1 AND enabled = 1;

-- name: CountUsers :one
SELECT COUNT(*) as count FROM users;

-- name: AddUserRole :exec
INSERT INTO user_roles (user_id, role_id) VALUES (?, ?) ON CONFLICT DO NOTHING;

-- name: RemoveUserRole :exec
DELETE FROM user_roles WHERE user_id = ? AND role_id = ?;

-- name: ClearUserRoles :exec
DELETE FROM user_roles WHERE user_id = ?;

-- name: GetUserRoleIDs :many
SELECT role_id FROM user_roles WHERE user_id = ?;

-- name: ListUsersWithRole :many
SELECT u.* FROM users u JOIN user_roles ur ON ur.user_id = u.user_id WHERE ur.role_id = ? ORDER BY u.username;

-- name: GetUserPanel :one
SELECT * FROM user_panel WHERE user_id = ?;

-- name: UpsertUserPanel :exec
INSERT INTO user_panel (user_id, permissions, limit_server_creation, limit_user_creation, limit_role_creation)
VALUES (?, ?, ?, ?, ?)
ON CONFLICT (user_id) DO UPDATE SET
  permissions = excluded.permissions,
  limit_server_creation = excluded.limit_server_creation,
  limit_user_creation = excluded.limit_user_creation,
  limit_role_creation = excluded.limit_role_creation;

-- name: DeleteUserPanel :exec
DELETE FROM user_panel WHERE user_id = ?;
