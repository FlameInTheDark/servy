-- name: GetSettings :one
SELECT * FROM panel_settings WHERE id = 1;

-- name: UpsertSettings :exec
INSERT INTO panel_settings (id, secret_api_key, cookie_secret, login_photo, login_opacity, logo_full, logo_square,
  og_enabled, og_title, og_description, og_image, og_color, master_server_dir)
VALUES (1, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (id) DO UPDATE SET
  secret_api_key = COALESCE(excluded.secret_api_key, secret_api_key),
  cookie_secret = COALESCE(excluded.cookie_secret, cookie_secret),
  login_photo = excluded.login_photo,
  login_opacity = excluded.login_opacity,
  logo_full = excluded.logo_full,
  logo_square = excluded.logo_square,
  og_enabled = excluded.og_enabled,
  og_title = excluded.og_title,
  og_description = excluded.og_description,
  og_image = excluded.og_image,
  og_color = excluded.og_color,
  master_server_dir = COALESCE(excluded.master_server_dir, master_server_dir);

-- name: UpdateBranding :exec
UPDATE panel_settings SET login_photo = ?, login_opacity = ?, logo_full = ?, logo_square = ? WHERE id = 1;

-- name: UpdateEmbed :exec
UPDATE panel_settings SET og_enabled = ?, og_title = ?, og_description = ?, og_image = ?, og_color = ? WHERE id = 1;

-- name: UpdateSecrets :exec
UPDATE panel_settings SET secret_api_key = ?, cookie_secret = ? WHERE id = 1;

-- name: SetMasterServerDir :exec
UPDATE panel_settings SET master_server_dir = ? WHERE id = 1;
