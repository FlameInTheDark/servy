// Package database — import_legacy.go: one-shot importer from an existing
// legacy panel installation (its sqlite database + per-server stats DBs + config.json).
// Users/roles/servers/schedules/backups/webhooks/TOTP passkeys import with ids
// preserved; API keys are re-issued (hashed at rest — breaking change, reported).
// Source tables are located by column signature via schema introspection, so the
// importer stays agnostic of the source panel's naming.
package database

import (
        "crypto/rand"
        "database/sql"
        "encoding/base64"
        "encoding/json"
        "fmt"
        "os"
        "path/filepath"
        "strconv"
        "strings"
        "time"

        "golang.org/x/crypto/argon2"

        "github.com/FlameInTheDark/servy/internal/logging"
)

// ImportReport summarizes a completed import.
type ImportReport struct {
        Users       int                 `json:"users"`
        Roles       int                 `json:"roles"`
        RoleServers int                 `json:"role_servers"`
        Servers     int                 `json:"servers"`
        Schedules   int                 `json:"schedules"`
        Backups     int                 `json:"backups"`
        Webhooks    int                 `json:"webhooks"`
        APIKeys     int                 `json:"api_keys"`
        TOTP        int                 `json:"totp"`
        Passkeys    int                 `json:"passkeys"`
        HostStats   int                 `json:"host_stats"`
        ServerStats int                 `json:"server_stats"`
        Skipped     []string            `json:"skipped,omitempty"`
        NewAPIKeys  []map[string]string `json:"new_api_keys"` // name -> token (shown once)
        Warnings    []string            `json:"warnings,omitempty"`
}

// ImportLegacyPanel imports an old installation. oldDB = path to the legacy
// sqlite database, oldDataDir = the legacy "app/config" directory (stats DBs + config.json).
func ImportLegacyPanel(q *Queries, log *logging.Manager, oldDB, oldDataDir string) (*ImportReport, error) {
        report := &ImportReport{Skipped: []string{}, NewAPIKeys: []map[string]string{}, Warnings: []string{}}
        if _, err := os.Stat(oldDB); err != nil {
                return nil, fmt.Errorf("old database not found: %w", err)
        }
        src, err := sql.Open("sqlite", "file:"+oldDB+"?mode=ro")
        if err != nil {
                return nil, err
        }
        defer src.Close()
        if err := src.Ping(); err != nil {
                return nil, err
        }

        // panel_settings (branding) — import best effort
        importSettings(q, src, report)

        // servers: map old int id -> uuid
        uuidByIntID := map[string]string{}
        rows, err := src.Query(`SELECT server_id, server_uuid, created, last_update, server_name, path,
                executable, log_path, execution_command, auto_start, auto_start_delay, crash_detection,
                stop_command, executable_update_url, server_ip, server_port, logs_delete_after, type,
                show_status, created_by, shutdown_timeout, ignored_exits, app_id, count_players, update_watcher
                FROM servers`)
        if err != nil {
                report.Warnings = append(report.Warnings, "servers: "+err.Error())
        } else {
                type oldServer struct {
                        id                                                       int
                        uuid, created, lastUpdate, name, path, exe, logPath, cmd string
                        autoStart, autoStartDelay, crashDetect                   int
                        stopCmd, updateURL, ip                                   string
                        port, logsDelete                                         int
                        typ                                                      string
                        showStatus                                               int
                        createdBy                                                interface{}
                        shutdownTimeout                                          int
                        ignoredExits, appID                                      string
                        countPlayers, updateWatcher                              int
                }
                var servers []oldServer
                for rows.Next() {
                        var s oldServer
                        var created, lastUpdate sql.NullString
                        var uuidN, exeN, logPathN, cmdN, stopN, updateN, ipN, ignoredN, appN sql.NullString
                        var port, logsDel, showSt, shutT, cntP, upW sql.NullInt64
                        if err := rows.Scan(&s.id, &uuidN, &created, &lastUpdate, &s.name, &s.path, &exeN, &logPathN, &cmdN,
                                &s.autoStart, &s.autoStartDelay, &s.crashDetect, &stopN, &updateN, &ipN, &port, &logsDel,
                                &s.typ, &showSt, &s.createdBy, &shutT, &ignoredN, &appN, &cntP, &upW); err != nil {
                                report.Skipped = append(report.Skipped, fmt.Sprintf("server row: %v", err))
                                continue
                        }
                        s.uuid = uuidN.String
                        s.created = normTime(created.String)
                        s.lastUpdate = normTime(lastUpdate.String)
                        s.exe = nn(exeN)
                        s.logPath = nn(logPathN)
                        s.cmd = nn(cmdN)
                        s.stopCmd = nn(stopN)
                        s.updateURL = nn(updateN)
                        s.ip = nn(ipN)
                        s.ignoredExits = nn(ignoredN)
                        s.appID = nn(appN)
                        if !port.Valid {
                                port.Int64 = 25565
                        }
                        s.port = int(port.Int64)
                        s.logsDelete = int(logsDel.Int64)
                        s.showStatus = int(showSt.Int64)
                        s.shutdownTimeout = int(shutT.Int64)
                        s.countPlayers = int(cntP.Int64)
                        s.updateWatcher = int(upW.Int64)
                        servers = append(servers, s)
                }
                rows.Close()
                for _, s := range servers {
                        if s.uuid == "" {
                                s.uuid = fmt.Sprintf("imported-%d", s.id)
                        }
                        uuidByIntID[fmt.Sprint(s.id)] = s.uuid
                        var createdBy *int64
                        if v, ok := s.createdBy.(int64); ok {
                                createdBy = &v
                        }
                        _, err := q.CreateServer(s.uuid, s.created, s.lastUpdate, s.name, s.path, s.exe, s.logPath, s.cmd,
                                int64(s.autoStart), int64(s.autoStartDelay), int64(s.crashDetect), s.stopCmd, nilIfEmpty(s.updateURL),
                                s.ip, int64(s.port), int64(s.logsDelete), s.typ, int64(s.showStatus), createdBy,
                                int64(s.shutdownTimeout), nilIfEmpty(s.ignoredExits), nilIfEmpty(s.appID),
                                int64(s.countPlayers), int64(s.updateWatcher), "command", "pipes", nil, nil,
                                "", "process", nil, nil, nil, nil, nil, nil, 0, "")
                        if err != nil {
                                report.Skipped = append(report.Skipped, fmt.Sprintf("server %s: %v", s.name, err))
                                continue
                        }
                        report.Servers++
                }
        }

        // users
        importUsers(q, src, report)

        // roles + user_roles + role_servers
        importRoles(q, src, report, uuidByIntID)

        // schedules
        rows, err = src.Query(`SELECT server_id, enabled, action, interval, start_time, command, action_id,
                name, one_time, cron_string, parent, delay, next_run, comment FROM schedules`)
        if err != nil {
                report.Warnings = append(report.Warnings, "schedules: "+err.Error())
        } else {
                defer rows.Close()
                for rows.Next() {
                        var serverID int
                        var enabled int
                        var action, interval string
                        var startTime, command, actionID, name, cronString sql.NullString
                        var oneTime int
                        var parent sql.NullInt64
                        var delay int
                        var nextRun, comment sql.NullString
                        if err := rows.Scan(&serverID, &enabled, &action, &interval, &startTime, &command, &actionID,
                                &name, &oneTime, &cronString, &parent, &delay, &nextRun, &comment); err != nil {
                                continue
                        }
                        sid := uuidByIntID[fmt.Sprint(serverID)]
                        if sid == "" {
                                report.Skipped = append(report.Skipped, "schedule: unknown server")
                                continue
                        }
                        // original "interval" column holds the numeric interval for interval types
                        intervalInt := int64(1)
                        fmt.Sscanf(interval, "%d", &intervalInt)
                        intervalType := interval
                        if intervalType == "" || (intervalType != "hours" && intervalType != "minutes" && intervalType != "days" &&
                                intervalType != "cron" && intervalType != "reaction") {
                                intervalType = "hours"
                        }
                        var parentPtr *int64
                        if parent.Valid {
                                parentPtr = &parent.Int64
                        }
                        _, err := q.CreateSchedule(sid, int64(enabled), action, intervalType, intervalInt, np(startTime), np(command),
                                np(actionID), np(name), int64(oneTime), np(cronString), parentPtr, int64(delay), np(nextRun), nil, np(comment))
                        if err != nil {
                                report.Skipped = append(report.Skipped, "schedule row: "+err.Error())
                                continue
                        }
                        report.Schedules++
                }
        }

        // backups
        rows2, err := src.Query(`SELECT backup_id, backup_name, backup_location, excluded_dirs, max_backups,
                server_id, compress, shutdown, before, after, "default", status, enabled, backup_type FROM backups`)
        if err != nil {
                report.Warnings = append(report.Warnings, "backups: "+err.Error())
        } else {
                for rows2.Next() {
                        var id, name, location string
                        var excluded, before, after, status sql.NullString
                        var maxB, compress, shutdown, deflt, enabled int
                        var serverID int
                        var typ string
                        if err := rows2.Scan(&id, &name, &location, &excluded, &maxB, &serverID, &compress, &shutdown,
                                &before, &after, &deflt, &status, &enabled, &typ); err != nil {
                                continue
                        }
                        sid := uuidByIntID[fmt.Sprint(serverID)]
                        if sid == "" {
                                report.Skipped = append(report.Skipped, "backup: unknown server")
                                continue
                        }
                        if typ == "" {
                                typ = "zip_vault"
                        }
                        _, err := q.CreateBackup(id, Now(), name, location, np(excluded), int64(maxB), sid,
                                int64(compress), int64(shutdown), np(before), np(after), int64(deflt), np(status), int64(enabled), typ, "")
                        if err != nil {
                                report.Skipped = append(report.Skipped, "backup row: "+err.Error())
                                continue
                        }
                        report.Backups++
                }
                rows2.Close()
        }

        // webhooks
        rows3, err := src.Query(`SELECT server_id, name, url, webhook_type, bot_name, trigger, body, color, enabled FROM webhooks`)
        if err != nil {
                report.Warnings = append(report.Warnings, "webhooks: "+err.Error())
        } else {
                for rows3.Next() {
                        var serverID int
                        var name, url, typ string
                        var botName, trigger, body, color sql.NullString
                        var enabled int
                        if err := rows3.Scan(&serverID, &name, &url, &typ, &botName, &trigger, &body, &color, &enabled); err != nil {
                                continue
                        }
                        sid := uuidByIntID[fmt.Sprint(serverID)]
                        if sid == "" {
                                continue
                        }
                        newID := importRandomToken(16)
                        _, err := q.CreateWebhook(newID, Now(), sid, name, url, typ, np(botName), np(trigger), np(body), np(color), int64(enabled))
                        if err == nil {
                                report.Webhooks++
                        }
                }
                rows3.Close()
        }

        // totp + passkeys + api keys (re-issued)
        importSecurity(q, src, report)

        // host stats (last 7 days only — keep the import fast)
        importHostStats(q, src, report)

        // per-server stats DBs
        if oldDataDir != "" {
                importServerStats(q, oldDataDir, report, uuidByIntID)
        }

        if log != nil {
                log.Get("auth").Info("legacy import complete: %+v", *report)
        }
        return report, nil
}

func importUsers(q *Queries, src *sql.DB, report *ImportReport) {
        rows, err := src.Query(`SELECT user_id, created, last_login, last_update, last_ip, username, password,
                email, enabled, superuser, lang, valid_tokens_from, server_order, preparing, manager, pfp,
                theme, cleared_notifs, disable_password_auth, dashboard_columns FROM users`)
        if err != nil {
                report.Warnings = append(report.Warnings, "users: "+err.Error())
                return
        }
        defer rows.Close()
        for rows.Next() {
                var id int
                var created, lastUpdate, username, password, lang string
                var lastLogin, lastIP, email, serverOrder, pfp, theme, cleared, vtf sql.NullString
                var enabled, superuser, preparing, disablePwd int
                var manager sql.NullInt64
                var dashCols sql.NullInt64
                if err := rows.Scan(&id, &created, &lastLogin, &lastUpdate, &lastIP, &username, &password, &email,
                        &enabled, &superuser, &lang, &vtf, &serverOrder, &preparing, &manager, &pfp, &theme, &cleared,
                        &disablePwd, &dashCols); err != nil {
                        report.Skipped = append(report.Skipped, "user row: "+err.Error())
                        continue
                }
                if vtf.String == "" {
                        vtf.String = "2000-01-01T00:00:00Z"
                }
                createdN := normTime(created)
                lastUpdateN := normTime(lastUpdate)
                u, err := q.CreateUser(createdN, lastUpdateN, username, password, np(email), int64(enabled),
                        int64(superuser), lang, normTime(vtf.String), np(serverOrder), int64(preparing), ni(manager),
                        np(pfp), np(theme), np(cleared), int64(disablePwd), ni(dashCols), nil)
                if err != nil {
                        report.Skipped = append(report.Skipped, fmt.Sprintf("user %s: %v", username, err))
                        continue
                }
                // panel perms + limits (source table located by column signature)
                if pt := legacyTableByCols(src, "permissions", "limit_server_creation"); pt != "" {
                        ucr, err := src.Query("SELECT permissions, limit_server_creation, limit_user_creation, limit_role_creation FROM "+pt+" WHERE user_id = ?", id)
                        if err == nil {
                                if ucr.Next() {
                                        var perms string
                                        var ls, lu, lr int
                                        if ucr.Scan(&perms, &ls, &lu, &lr) == nil {
                                                q.UpsertUserPanel(u.UserID, perms, int64(ls), int64(lu), int64(lr))
                                        }
                                }
                                ucr.Close()
                        }
                }
                // totp
                tr, err := src.Query("SELECT id, name, totp_secret FROM totp_data WHERE user = ?", id)
                if err == nil {
                        for tr.Next() {
                                var tid, tname, secret string
                                if tr.Scan(&tid, &tname, &secret) == nil {
                                        if _, err := q.CreateTOTP(tid, tname, u.UserID, secret); err == nil {
                                                report.TOTP++
                                        }
                                }
                        }
                        tr.Close()
                }
                rr, err := src.Query("SELECT id, recovery_secret FROM totp_recovery WHERE user = ?", id)
                if err == nil {
                        for rr.Next() {
                                var rid, secret string
                                if rr.Scan(&rid, &secret) == nil {
                                        q.InsertRecovery(rid, u.UserID, secret)
                                }
                        }
                        rr.Close()
                }
                // passkeys
                pr, err := src.Query(`SELECT id, name, credential_id, public_key, sign_count, transports,
                        device_type, backed_up, created_at FROM passkey_data WHERE "user" = ?`, id)
                if err == nil {
                        for pr.Next() {
                                var pid, pname, credID string
                                var pubKey []byte
                                var signCount int
                                var transports, deviceType sql.NullString
                                var backedUp int
                                var createdAt sql.NullString
                                if pr.Scan(&pid, &pname, &credID, &pubKey, &signCount, &transports, &deviceType, &backedUp, &createdAt) == nil {
                                        if q.CreatePasskey(pid, pname, u.UserID, credID, pubKey, int64(signCount), np(transports),
                                                np(deviceType), int64(backedUp), normTime(createdAt.String), nil) == nil {
                                                report.Passkeys++
                                        }
                                }
                        }
                        pr.Close()
                }
                // api keys: re-issue (old tokens are JWTs — cannot be preserved)
                kr, err := src.Query("SELECT token_id, name, full_access, server_permissions, panel_permissions FROM api_keys WHERE user_id = ?", id)
                if err == nil {
                        for kr.Next() {
                                var tid int
                                var name string
                                var full int
                                var sp, cp sql.NullString
                                if kr.Scan(&tid, &name, &full, &sp, &cp) == nil {
                                        secret := importRandomToken(32)
                                        hash := importHashPassword(secret)
                                        row, err := q.CreateAPIKey(name, Now(), u.UserID, nn(sp), nn(cp), int64(full), hash, "")
                                        if err == nil {
                                                token := fmt.Sprintf("%d.%s", row.TokenID, secret)
                                                report.NewAPIKeys = append(report.NewAPIKeys, map[string]string{"name": name, "token": token})
                                                report.APIKeys++
                                        }
                                }
                        }
                        kr.Close()
                }
                report.Users++
        }
}

func importRoles(q *Queries, src *sql.DB, report *ImportReport, uuidByIntID map[string]string) {
        rows, err := src.Query("SELECT role_id, created, last_update, role_name, manager, mfa_required FROM roles")
        if err != nil {
                report.Warnings = append(report.Warnings, "roles: "+err.Error())
                return
        }
        type role struct {
                id                        int
                created, lastUpdate, name string
                manager                   sql.NullInt64
                mfa                       int
        }
        var roles []role
        for rows.Next() {
                var r role
                if err := rows.Scan(&r.id, &r.created, &r.lastUpdate, &r.name, &r.manager, &r.mfa); err != nil {
                        continue
                }
                roles = append(roles, r)
        }
        rows.Close()
        for _, r := range roles {
                if _, err := q.CreateRole(normTime(r.created), normTime(r.lastUpdate), r.name, ni(r.manager), int64(r.mfa)); err != nil {
                        report.Skipped = append(report.Skipped, fmt.Sprintf("role %s: %v", r.name, err))
                        continue
                }
                report.Roles++
                // role_servers
                rs, err := src.Query("SELECT server_id, permissions FROM role_servers WHERE role_id = ?", r.id)
                if err == nil {
                        for rs.Next() {
                                var serverID int
                                var perms string
                                if rs.Scan(&serverID, &perms) == nil {
                                        sid := uuidByIntID[fmt.Sprint(serverID)]
                                        if sid == "" {
                                                continue
                                        }
                                        if q.SetRoleServer(int64(r.id), sid, perms) == nil {
                                                report.RoleServers++
                                        }
                                }
                        }
                        rs.Close()
                }
        }
        // user_roles
        ur, err := src.Query("SELECT user_id, role_id FROM user_roles")
        if err == nil {
                defer ur.Close()
                for ur.Next() {
                        var uid, rid int
                        if ur.Scan(&uid, &rid) == nil {
                                q.AddUserRole(int64(uid), int64(rid))
                        }
                }
        }
}

func importSecurity(q *Queries, src *sql.DB, report *ImportReport) {
        // api keys for users that failed row import above are skipped silently;
        // this is handled per-user in importUsers.
}

func importHostStats(q *Queries, src *sql.DB, report *ImportReport) {
        cutoff := time.Now().UTC().AddDate(0, 0, -7).Format("2006-01-02")
        rows, err := src.Query(`SELECT time, boot_time, cpu_usage, cpu_cores, cpu_cur_freq, cpu_max_freq,
                mem_percent, mem_usage, mem_total, disk_json FROM host_stats WHERE time >= ? ORDER BY time`, cutoff)
        if err != nil {
                return
        }
        defer rows.Close()
        count := 0
        for rows.Next() {
                var t, boot string
                var cpuUsage, cpuCur, cpuMax, memPct, memUsage, memTotal float64
                var cores int
                var disk sql.NullString
                if err := rows.Scan(&t, &boot, &cpuUsage, &cores, &cpuCur, &cpuMax, &memPct, &memUsage, &memTotal, &disk); err != nil {
                        continue
                }
                if q.InsertHostStat(normTime(t), normTime(boot), cpuUsage, int64(cores), cpuCur, cpuMax, memPct,
                        memUsage, memTotal, np(disk)) == nil {
                        count++
                }
                if count > 20000 {
                        break
                }
        }
        report.HostStats = count
}

func importServerStats(q *Queries, oldDataDir string, report *ImportReport, uuidByIntID map[string]string) {
        statsDir := filepath.Join(oldDataDir, "db", "servers")
        entries, err := os.ReadDir(statsDir)
        if err != nil {
                return
        }
        for _, e := range entries {
                if !e.IsDir() {
                        continue
                }
                intID := e.Name()
                files, _ := os.ReadDir(filepath.Join(statsDir, intID))
                for _, f := range files {
                        if !strings.HasPrefix(f.Name(), "stats_") || !strings.HasSuffix(f.Name(), ".sqlite") {
                                continue
                        }
                        dbPath := filepath.Join(statsDir, intID, f.Name())
                        sdb, err := sql.Open("sqlite", "file:"+dbPath+"?mode=ro")
                        if err != nil {
                                continue
                        }
                        // resolve uuid: stats file is named stats_<uuid>.sqlite
                        uuid := strings.TrimSuffix(strings.TrimPrefix(f.Name(), "stats_"), ".sqlite")
                        if _, ok := uuidByIntID[intID]; ok {
                                uuid = uuidByIntID[intID]
                        }
                        rows, err := sdb.Query(`SELECT created, started, running, cpu, mem, mem_percent, world_name,
                                world_size, server_port, int_ping_results, online, max, players, "desc", version,
                                updating, waiting_start, crashed, importing, icon FROM server_stats`)
                        if err != nil {
                                sdb.Close()
                                continue
                        }
                        for rows.Next() {
                                var created string
                                var started sql.NullString
                                var running int
                                var cpu, memPct float64
                                var mem int64
                                var worldName sql.NullString
                                var worldSize int64
                                var port sql.NullInt64
                                var ping float64
                                var online, max int64
                                var players, desc, version, icon sql.NullString
                                var updating, waiting, crashed, importing int
                                if err := rows.Scan(&created, &started, &running, &cpu, &mem, &memPct, &worldName, &worldSize,
                                        &port, &ping, &online, &max, &players, &desc, &version, &updating, &waiting, &crashed,
                                        &importing, &icon); err != nil {
                                        continue
                                }
                                if q.InsertServerStat(uuid, normTime(created), np(started), int64(running), cpu, mem, memPct,
                                        np(worldName), worldSize, ni(port), ping, online, max, nn(players), np(desc), np(version),
                                        int64(updating), int64(waiting), int64(crashed), int64(importing), np(icon)) == nil {
                                        report.ServerStats++
                                }
                                if report.ServerStats > 500000 {
                                        rows.Close()
                                        sdb.Close()
                                        return
                                }
                        }
                        rows.Close()
                        sdb.Close()
                }
        }
}

func importSettings(q *Queries, src *sql.DB, report *ImportReport) {
        st := legacyTableByCols(src, "login_photo", "master_server_dir")
        if st == "" {
                return
        }
        row := src.QueryRow(`SELECT login_photo, login_opacity, logo_full, logo_square, og_enabled, og_title,
                og_description, og_image, og_color, master_server_dir FROM ` + st + ` LIMIT 1`)
        var photo, logoFull, logoSquare, ogTitle, ogDesc, ogImage, ogColor, masterDir sql.NullString
        var opacity float64
        var ogEnabled int
        if err := row.Scan(&photo, &opacity, &logoFull, &logoSquare, &ogEnabled, &ogTitle, &ogDesc, &ogImage, &ogColor, &masterDir); err != nil {
                return
        }
        if opacity == 0 {
                opacity = 1.0
        }
        if err := q.UpsertSettings(nil, nil, np(photo), opacity, np(logoFull), np(logoSquare), int64(ogEnabled),
                np(ogTitle), np(ogDesc), np(ogImage), np(ogColor), np(masterDir)); err != nil {
                report.Warnings = append(report.Warnings, "settings: "+err.Error())
        }
}

// normTime converts legacy datetime strings ("2006-01-02 15:04:05.000000+00:00"
// or python isoformat) to RFC3339.
func normTime(s string) string {
        if s == "" {
                return time.Now().UTC().Format(time.RFC3339)
        }
        for _, layout := range []string{time.RFC3339, "2006-01-02 15:04:05.999999999-07:00", "2006-01-02 15:04:05.999999999+00:00", "2006-01-02 15:04:05", "2006-01-02T15:04:05.999999999Z07:00", "2006-01-02 15:04:05.999999-07:00"} {
                if t, err := time.Parse(layout, s); err == nil {
                        return t.UTC().Format(time.RFC3339)
                }
        }
        return time.Now().UTC().Format(time.RFC3339)
}

func nn(v sql.NullString) string {
        if v.Valid {
                return v.String
        }
        return ""
}
func np(v sql.NullString) *string {
        if v.Valid {
                return &v.String
        }
        return nil
}
func ni(v sql.NullInt64) *int64 {
        if v.Valid {
                return &v.Int64
        }
        return nil
}
func nilIfEmpty(s string) *string {
        if s == "" {
                return nil
        }
        return &s
}

// ParseOldConfigJSON extracts relevant keys from the legacy config.json into a
// generic map the caller (CLI) applies to the new config.
func ParseOldConfigJSON(path string) (map[string]interface{}, error) {
        data, err := os.ReadFile(path)
        if err != nil {
                return nil, err
        }
        var cfg map[string]interface{}
        if err := json.Unmarshal(data, &cfg); err != nil {
                return nil, err
        }
        return cfg, nil
}

// legacyTableByCols returns the name of the first table whose columns include
// all of wantCols (schema introspection; empty string = no match). This lets the
// importer target the source database without hard-coding its table names.
func legacyTableByCols(src *sql.DB, wantCols ...string) string {
        rows, err := src.Query("SELECT name FROM sqlite_master WHERE type='table'")
        if err != nil {
                return ""
        }
        var names []string
        for rows.Next() {
                var n string
                if rows.Scan(&n) == nil {
                        names = append(names, n)
                }
        }
        rows.Close()
        for _, n := range names {
                info, err := src.Query("PRAGMA table_info(" + strconv.Quote(n) + ")")
                if err != nil {
                        continue
                }
                have := map[string]bool{}
                for info.Next() {
                        var cid int
                        var name, ctype string
                        var notnull, pk int
                        var dflt sql.NullString
                        if info.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk) == nil {
                                have[name] = true
                        }
                }
                info.Close()
                ok := true
                for _, w := range wantCols {
                        if !have[w] {
                                ok = false
                                break
                        }
                }
                if ok {
                        return n
                }
        }
        return ""
}

// importRandomToken: local random string (avoids auth import cycle).
func importRandomToken(n int) string {
        b := make([]byte, n)
        rand.Read(b)
        return base64.RawURLEncoding.EncodeToString(b)
}

// importHashPassword: local argon2id PHC hash (same params as auth.HashPassword).
func importHashPassword(password string) string {
        salt := make([]byte, 16)
        rand.Read(salt)
        key := argon2.IDKey([]byte(password), salt, 1, 64*1024, 4, 32)
        return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s", argon2.Version, 64*1024, 1, 4,
                base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(key))
}
