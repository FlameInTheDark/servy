-- name: CreateRole :one
INSERT INTO roles (created, last_update, role_name, manager, mfa_required)
VALUES (?, ?, ?, ?, ?)
RETURNING *;

-- name: GetRole :one
SELECT * FROM roles WHERE role_id = ?;

-- name: ListRoles :many
SELECT * FROM roles ORDER BY role_name;

-- name: UpdateRole :exec
UPDATE roles SET last_update = ?, role_name = ?, manager = ?, mfa_required = ? WHERE role_id = ?;

-- name: DeleteRole :exec
DELETE FROM roles WHERE role_id = ?;

-- name: SetRoleServer :exec
INSERT INTO role_servers (role_id, server_id, permissions) VALUES (?, ?, ?)
ON CONFLICT (role_id, server_id) DO UPDATE SET permissions = excluded.permissions;

-- name: RemoveRoleServer :exec
DELETE FROM role_servers WHERE role_id = ? AND server_id = ?;

-- name: ClearRoleServers :exec
DELETE FROM role_servers WHERE role_id = ?;

-- name: ListRoleServers :many
SELECT * FROM role_servers WHERE role_id = ?;

-- name: GetRoleServerPermission :one
SELECT permissions FROM role_servers WHERE role_id = ? AND server_id = ?;

-- name: ListUserServerPerms :many
SELECT rs.server_id AS server_id, rs.permissions AS permissions
FROM role_servers rs
JOIN user_roles ur ON ur.role_id = rs.role_id
WHERE ur.user_id = ?;
