# Backup Storage Destinations

By default a backup archive stays on the machine that runs the server. A
**storage destination** is a place a panel additionally uploads those archives
to — object storage, an FTP/FTPS server or a WebDAV share — so a dead host or a
wiped disk does not take the backups with it.

Destinations are configured once per panel (`Panel settings → Backups`), then
picked per backup config in the server's `Backups` tab.

## Supported destinations

| Kind | Typical use |
| --- | --- |
| S3-compatible | AWS S3, MinIO, Cloudflare R2, Backblaze B2, DigitalOcean Spaces, Wasabi |
| FTP / FTPS | plain FTP, explicit (`AUTH TLS`) and implicit TLS servers |
| WebDAV | Nextcloud, ownCloud, generic WebDAV mounts |

SFTP is implemented in the storage layer but hidden from the picker in this
release.

Every destination has an optional **folder prefix** and a set of credentials.
Each one can be tested from the UI before it is saved (`Test connection`).

## Uploads happen on the host that runs the server

The panel does not stream the archive through itself. It sends the destination
(and the retention policy) to the node that owns the server over the
authenticated gRPC channel, and that node uploads the archive with its own
connection. Servers that live on the panel are uploaded by the panel itself.

Consequences worth knowing:

- The node needs outbound access to the destination. The panel does not.
- Credentials travel to the node inside the backup job, are used for that job,
  and are not written to the node's database.
- A node never needs the panel's configuration: everything a run needs
  (destination, retention, cluster id) travels with the job.

## Cluster isolation

Every stored object is written below

```
<prefix>/<cluster-id>/<server-id>/<archive-name>
```

`cluster-id` belongs to the panel and is generated on first start (visible in
`Panel settings → Cluster` and in `config.yaml` as `cluster_id`). Two panels can
share one bucket or FTP account without ever seeing or overwriting each other's
archives: listings are always scoped to the panel's own namespace, and so are
deletions.

Because the namespace is part of the key, pointing a node at another panel does
not mix the histories — the old archives stay exactly where the old panel put
them.

## Retention

Two panel settings drive what is kept:

| Setting | Default | Meaning |
| --- | --- | --- |
| `backup_local_keep` | `1` | How many archives per server stay on the host when they are uploaded to a destination. The local copy is a working copy: the newest archive is kept so downloads and restores are instant, older ones are removed after a successful upload. `0` keeps everything. |
| `backup_remote_keep` | `5` | How many archives per server stay in the destination. Older ones are deleted after a successful upload. `0` keeps everything. |

A backup config without a destination keeps its own `max_backups` limit instead
(the classic behaviour). Uploads always happen before local pruning: if the
upload fails the run is still reported as failed and the local archives stay
put, so a broken destination can never cost you the only copy.

## Restores and downloads

The archive list of a backup config shows where each archive lives: on the host,
in the destination, or both. Restoring or downloading an archive that was
already pruned locally fetches it back from the destination first (on the node
for remote servers), so retention never blocks a recovery.

Deleting a backup config with `files=true` removes the config's archives from
the host **and** from the destination namespace of that server.

## API

```
GET    /api/v1/panel/backup-storages             list destinations
GET    /api/v1/panel/backup-storages/kinds       backends + their form fields
POST   /api/v1/panel/backup-storages             create
POST   /api/v1/panel/backup-storages/test        test an unsaved destination
GET    /api/v1/panel/backup-storages/:id         read one
PATCH  /api/v1/panel/backup-storages/:id         update (masked secrets are kept)
DELETE /api/v1/panel/backup-storages/:id[?force=true]
POST   /api/v1/panel/backup-storages/:id/test    test a saved destination
```

Secrets (`secret_key`, `session_token`, `password`, `passphrase`,
`private_key`) are never returned: they come back as `********`, and sending
that mask back keeps the stored value, so editing a destination does not require
retyping keys. Deleting a destination that backup configs still use answers
`409 STORAGE_IN_USE` unless `force=true` is passed; with `force` those configs
detach and fall back to host-only backups.

## Troubleshooting

- **`STORAGE_TEST_FAILED`** — the panel (or, for remote servers, the node)
  could not reach the destination: check host/port, credentials, TLS mode, and
  whether the bucket/collection already exists.
- **Uploads fail, run is marked failed, archives stay local** — expected.
  `last_upload_error` on the backup config names the reason.
- **FTPS behind an old server** — try explicit TLS first; some servers only
  support `AUTH TLS` on port 21, others hand out implicit TLS on port 990.
- **S3 with a self-hosted gateway** — enable *Path style requests* (MinIO and
  most gateways need it) and leave the region at its default.
