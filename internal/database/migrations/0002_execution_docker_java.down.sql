-- 0002 down: drop execution/java columns.
-- (sqlc reads the migration dir as schema; the .down file runs first in
-- alphabetical order, so guard with a no-op when columns are absent is not
-- possible in SQLite — this rollback is only valid against a migrated DB.)
ALTER TABLE servers DROP COLUMN java_selection;
ALTER TABLE servers DROP COLUMN execution_backend;
ALTER TABLE servers DROP COLUMN docker_image;
ALTER TABLE servers DROP COLUMN docker_env;
ALTER TABLE servers DROP COLUMN docker_ports;
ALTER TABLE servers DROP COLUMN docker_binds;
ALTER TABLE servers DROP COLUMN docker_network;
ALTER TABLE servers DROP COLUMN docker_workdir;
ALTER TABLE servers DROP COLUMN docker_autoremove;
