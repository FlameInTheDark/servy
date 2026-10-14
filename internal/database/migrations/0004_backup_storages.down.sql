ALTER TABLE backups DROP COLUMN storage_id;
DROP INDEX idx_backup_storages_default;
DROP TABLE backup_storages;
