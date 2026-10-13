-- Adds the per-server terminal/shell selection used to execute the server
-- command: '' (auto), 'bash', 'sh', 'cmd', 'powershell', 'pwsh', 'git-bash',
-- 'none' (direct exec, no shell).
ALTER TABLE servers ADD COLUMN shell TEXT NOT NULL DEFAULT '';
