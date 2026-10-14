-- Which folders a backup config covers, relative to the server directory.
-- NULL or empty means "the whole server directory" (the behaviour every row
-- written before this migration keeps). A non-empty list restricts the backup
-- to those folders and everything below them; excluded_dirs still subtracts
-- from the result. The folder picker in the server's backup settings fills
-- this in, pre-selected with the directories the game manifest declares
-- (steam catalog `backup_dirs`, creation recipe `backup_dirs`).
ALTER TABLE backups ADD COLUMN included_dirs TEXT;
