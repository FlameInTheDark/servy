-- Backup storage destinations: S3 compatible object storage, FTP/FTPS, SFTP,
-- WebDAV and plain local directories that archives are uploaded to after a
-- backup ran. Every destination carries the cluster id of the panel that owns
-- it, so archives of different clusters never share a namespace inside one
-- bucket / account / collection.
CREATE TABLE backup_storages (
  storage_id TEXT PRIMARY KEY,
  created TEXT NOT NULL,
  name TEXT NOT NULL,
  kind TEXT NOT NULL,
  prefix TEXT NOT NULL DEFAULT '',
  settings TEXT NOT NULL DEFAULT '{}',
  enabled INTEGER NOT NULL DEFAULT 1,
  "default" INTEGER NOT NULL DEFAULT 0,
  cluster_id TEXT NOT NULL DEFAULT ''
);
CREATE INDEX idx_backup_storages_default ON backup_storages("default");

-- Which destination a backup config uploads to ('' = keep archives local).
ALTER TABLE backups ADD COLUMN storage_id TEXT NOT NULL DEFAULT '';
