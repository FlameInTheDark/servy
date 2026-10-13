-- 0002: execution backends (process | docker) + per-server java selection.
-- Adds Docker container spec columns and the java_selection field (legacy parity).
ALTER TABLE servers ADD COLUMN java_selection TEXT NOT NULL DEFAULT '';
ALTER TABLE servers ADD COLUMN execution_backend TEXT NOT NULL DEFAULT 'process';
ALTER TABLE servers ADD COLUMN docker_image TEXT;
ALTER TABLE servers ADD COLUMN docker_env TEXT;
ALTER TABLE servers ADD COLUMN docker_ports TEXT;
ALTER TABLE servers ADD COLUMN docker_binds TEXT;
ALTER TABLE servers ADD COLUMN docker_network TEXT;
ALTER TABLE servers ADD COLUMN docker_workdir TEXT;
ALTER TABLE servers ADD COLUMN docker_autoremove INTEGER NOT NULL DEFAULT 0;
