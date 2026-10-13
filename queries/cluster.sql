-- name: UpsertNode :exec
INSERT INTO nodes (node_id, name, address, role, status, last_seen, version, capabilities, cluster_token_hash, created)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (node_id) DO UPDATE SET
  name = excluded.name, address = excluded.address, role = excluded.role,
  status = excluded.status, last_seen = excluded.last_seen, version = excluded.version,
  capabilities = excluded.capabilities;

-- name: GetNode :one
SELECT * FROM nodes WHERE node_id = ?;

-- name: GetNodeByName :one
SELECT * FROM nodes WHERE name = ?;

-- name: ListNodes :many
SELECT * FROM nodes ORDER BY name;

-- name: UpdateNodeStatus :exec
UPDATE nodes SET status = ?, last_seen = ?, version = ? WHERE node_id = ?;

-- name: DeleteNode :exec
DELETE FROM nodes WHERE node_id = ?;

-- name: SetNodeServer :exec
INSERT INTO node_servers (node_id, server_id, remote_name) VALUES (?, ?, ?)
ON CONFLICT (node_id, server_id) DO UPDATE SET remote_name = excluded.remote_name;

-- name: RemoveNodeServer :exec
DELETE FROM node_servers WHERE node_id = ? AND server_id = ?;

-- name: ListNodeServers :many
SELECT * FROM node_servers WHERE node_id = ?;

-- name: UpsertDefinition :exec
INSERT INTO server_definitions (definition_id, name, type, game, definition, source, version, created, updated)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (definition_id) DO UPDATE SET
  name = excluded.name, type = excluded.type, game = excluded.game, definition = excluded.definition,
  source = excluded.source, version = excluded.version, updated = excluded.updated;

-- name: GetDefinition :one
SELECT * FROM server_definitions WHERE definition_id = ?;

-- name: ListDefinitions :many
SELECT * FROM server_definitions ORDER BY name;

-- name: DeleteDefinition :exec
DELETE FROM server_definitions WHERE definition_id = ?;
