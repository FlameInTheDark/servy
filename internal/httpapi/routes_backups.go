package httpapi

import (
	"encoding/json"
	"bytes"
	"io"
	"net/http"
	"path/filepath"
	"sort"
	"strings"

	"github.com/gofiber/fiber/v3"

	"github.com/FlameInTheDark/servy/internal/authz"
	"github.com/FlameInTheDark/servy/internal/backups"
	"github.com/FlameInTheDark/servy/internal/repo/gen"
	"github.com/FlameInTheDark/servy/internal/scheduler"
	"github.com/FlameInTheDark/servy/internal/servers"
)

// readZipInfo summarizes an archive for the wizard (top dirs + executables).
func (a *App) readZipInfo(data []byte) (map[string]interface{}, error) {
	zr, err := zipReaderBytes(data)
	if err != nil {
		return nil, err
	}
	defer zr.Close()
	roots := map[string]bool{}
	allFiles := []string{}
	for _, f := range zr.Files() {
		allFiles = append(allFiles, f.Name)
		parts := strings.SplitN(filepath.ToSlash(f.Name), "/", 2)
		if parts[0] != "" {
			roots[parts[0]] = true
		}
	}
	rootList := []string{}
	for r := range roots {
		rootList = append(rootList, r)
	}
	sort.Strings(rootList)
	exes := []string{}
	for _, name := range allFiles {
		l := strings.ToLower(name)
		if strings.HasSuffix(l, ".jar") || strings.HasSuffix(l, ".sh") ||
			strings.HasSuffix(l, ".bat") || l == "bedrock_server" {
			if !strings.Contains(l, "installer") {
				exes = append(exes, name)
			}
		}
	}
	if len(exes) > 20 {
		exes = exes[:20]
	}
	return map[string]interface{}{
		"roots": rootList, "files": len(allFiles), "executables": exes,
	}, nil
}

func (a *App) mountBackups(api fiber.Router) {
	api.Get("/servers/:id/backups", a.listBackups)
	api.Post("/servers/:id/backups", a.createBackup)
	api.Get("/backups/backup/:backup_id", a.getBackup)
	api.Patch("/backups/backup/:backup_id", a.patchBackup)
	api.Delete("/backups/backup/:backup_id", a.deleteBackup)
	api.Post("/backups/backup/:backup_id/run", a.runBackup)
	api.Post("/backups/backup/:backup_id/restore/:name", a.restoreBackup)
	api.Get("/backups/backup/:backup_id/archives", a.backupArchives)
	api.Get("/backups/backup/:backup_id/download/:name", a.backupDownload)
	api.Get("/backups/backup/:backup_id/files/:name", a.backupArchiveFiles)

	// schedules (tasks)
	api.Get("/servers/:id/tasks", a.listTasks)
	api.Post("/servers/:id/tasks", a.createTask)
	api.Get("/tasks/:task_id", a.getTask)
	api.Patch("/tasks/:task_id", a.patchTask)
	api.Delete("/tasks/:task_id", a.deleteTask)
	api.Get("/tasks/:task_id/children", a.taskChildren)
	api.Post("/tasks/:task_id/run", a.runTask)
}

func (a *App) listBackups(c fiber.Ctx) error {
	p := a.requireAuth(c)
	if p == nil {
		return nil
	}
	serverID := c.Params("id")
	if !p.CanServer(serverID, authz.ServerBackup) {
		return fail(c, 403, "NOT_AUTHORIZED")
	}
	return ok(c, a.Backups.List(serverID))
}

func (a *App) createBackup(c fiber.Ctx) error {
	p := a.requireAuth(c)
	if p == nil {
		return nil
	}
	serverID := c.Params("id")
	if !p.CanServer(serverID, authz.ServerBackup) {
		return fail(c, 403, "NOT_AUTHORIZED")
	}
	var req struct {
		Name         string   `json:"name"`
		Location     string   `json:"location,omitempty"`
		ExcludedDirs []string `json:"excluded_dirs,omitempty"`
		MaxBackups   int64    `json:"max_backups"`
		Compress     bool     `json:"compress"`
		Shutdown     bool     `json:"shutdown"`
		Before       string   `json:"before,omitempty"`
		After        string   `json:"after,omitempty"`
		Default      bool     `json:"default"`
		Enabled      bool     `json:"enabled"`
		BackupType   string   `json:"backup_type,omitempty"`
	}
	if err := parseBody(c, &req); err != nil {
		return err
	}
	def, err := a.DB.GetServer(serverID)
	if err != nil {
		return fail(c, 404, "SERVER_NOT_FOUND")
	}
	location := req.Location
	if location == "" {
		location = filepath.Join(a.BackupRoot, servers.SanitizeServerName(def.ServerName))
	}
	excluded := strings.Join(req.ExcludedDirs, ",")
	btype := req.BackupType
	if btype == "" {
		btype = "zip_vault"
	}
	if req.Default {
		a.DB.UnsetDefaultBackups(serverID)
	}
	b, err := a.Backups.Create(gen.Backup{
		BackupName: req.Name, BackupLocation: location, ExcludedDirs: &excluded,
		MaxBackups: req.MaxBackups, ServerID: serverID, Compress: bool2i(req.Compress),
		Shutdown: bool2i(req.Shutdown), Before: &req.Before, After: &req.After,
		Default: bool2i(req.Default), Enabled: bool2i(req.Enabled), BackupType: btype,
	})
	if err != nil {
		return failMsg(c, 400, err.Error(), err.Error())
	}
	return ok(c, a.Backups.View(b))
}

func (a *App) loadBackup(c fiber.Ctx, perm int) (gen.Backup, bool) {
	p := a.requireAuth(c)
	if p == nil {
		return gen.Backup{}, false
	}
	b, err := a.Backups.Get(c.Params("backup_id"))
	if err != nil {
		_ = fail(c, 404, "BACKUP_NOT_FOUND")
		return gen.Backup{}, false
	}
	if !p.CanServer(b.ServerID, perm) {
		_ = fail(c, 403, "NOT_AUTHORIZED")
		return gen.Backup{}, false
	}
	return b, true
}

func (a *App) getBackup(c fiber.Ctx) error {
	b, good := a.loadBackup(c, authz.ServerBackup)
	if !good {
		return nil
	}
	return ok(c, a.Backups.View(b))
}

func (a *App) patchBackup(c fiber.Ctx) error {
	b, good := a.loadBackup(c, authz.ServerBackup)
	if !good {
		return nil
	}
	var req struct {
		Name         *string  `json:"name,omitempty"`
		Location     *string  `json:"location,omitempty"`
		ExcludedDirs []string `json:"excluded_dirs,omitempty"`
		MaxBackups   *int64   `json:"max_backups,omitempty"`
		Compress     *bool    `json:"compress,omitempty"`
		Shutdown     *bool    `json:"shutdown,omitempty"`
		Before       *string  `json:"before,omitempty"`
		After        *string  `json:"after,omitempty"`
		Default      *bool    `json:"default,omitempty"`
		Enabled      *bool    `json:"enabled,omitempty"`
	}
	if err := parseBody(c, &req); err != nil {
		return err
	}
	if req.Name != nil {
		b.BackupName = *req.Name
	}
	if req.Location != nil {
		b.BackupLocation = *req.Location
	}
	if req.ExcludedDirs != nil {
		j := strings.Join(req.ExcludedDirs, ",")
		b.ExcludedDirs = &j
	}
	if req.MaxBackups != nil {
		b.MaxBackups = *req.MaxBackups
	}
	if req.Compress != nil {
		b.Compress = bool2i(*req.Compress)
	}
	if req.Shutdown != nil {
		b.Shutdown = bool2i(*req.Shutdown)
	}
	if req.Before != nil {
		b.Before = req.Before
	}
	if req.After != nil {
		b.After = req.After
	}
	if req.Default != nil {
		if *req.Default {
			a.DB.UnsetDefaultBackups(b.ServerID)
		}
		b.Default = bool2i(*req.Default)
	}
	if req.Enabled != nil {
		b.Enabled = bool2i(*req.Enabled)
	}
	if err := a.Backups.Update(b); err != nil {
		return failMsg(c, 400, err.Error(), err.Error())
	}
	return ok(c, a.Backups.View(b))
}

func (a *App) deleteBackup(c fiber.Ctx) error {
	b, good := a.loadBackup(c, authz.ServerBackup)
	if !good {
		return nil
	}
	removeFiles := fiber.Query[bool](c, "files", false)
	if nodeID := a.nodeIDForServer(b.ServerID); nodeID != "" {
		if !a.nodeOnline(nodeID) {
			return fail(c, 502, "NODE_OFFLINE")
		}
		if err := a.Cluster.BackupDelete(nodeID, b.ServerID, b.BackupID, removeFiles); err != nil {
			return failMsg(c, 502, "NODE_ERROR", err.Error())
		}
		if err := a.DB.DeleteBackup(b.BackupID); err != nil {
			return failMsg(c, 500, "SERVER_ERROR", err.Error())
		}
		return ok(c, fiber.Map{"deleted": b.BackupID, "node_id": nodeID})
	}
	if err := a.Backups.Delete(b.BackupID, removeFiles); err != nil {
		return failMsg(c, 400, err.Error(), err.Error())
	}
	return ok(c, fiber.Map{"deleted": b.BackupID})
}

func (a *App) runBackup(c fiber.Ctx) error {
	b, good := a.loadBackup(c, authz.ServerBackup)
	if !good {
		return nil
	}
	p := currentUserFrom(c)
	uid := int64(0)
	if p != nil {
		uid = p.UserID
	}
	if nodeID := a.nodeIDForServer(b.ServerID); nodeID != "" {
		return a.runRemoteBackup(c, nodeID, b)
	}
	if err := a.Backups.Run(b.BackupID, uid); err != nil {
		return failMsg(c, 500, err.Error(), err.Error())
	}
	return ok(c, fiber.Map{"backup_id": b.BackupID, "status": "completed"})
}

// runRemoteBackup executes a backup config on the node that owns the server and
// mirrors the node's status report back into the panel row.
func (a *App) runRemoteBackup(c fiber.Ctx, nodeID string, b gen.Backup) error {
	if !a.nodeOnline(nodeID) {
		return fail(c, 502, "NODE_OFFLINE")
	}
	raw, err := a.Cluster.BackupRun(nodeID, b.ServerID, b.BackupID, b)
	if err != nil {
		return failMsg(c, 502, "NODE_ERROR", err.Error())
	}
	if len(raw) > 0 {
		var row gen.Backup
		if err := json.Unmarshal(raw, &row); err == nil && row.Status != nil {
			_ = a.DB.SetBackupStatus(row.Status, b.BackupID)
		}
	}
	return ok(c, fiber.Map{"backup_id": b.BackupID, "status": "completed", "node_id": nodeID})
}

func (a *App) restoreBackup(c fiber.Ctx) error {
	b, good := a.loadBackup(c, authz.ServerBackup)
	if !good {
		return nil
	}
	archive := c.Params("name")
	path := filepath.Join(b.BackupLocation, archive)
	if strings.Contains(archive, "..") {
		return fail(c, 400, "TRAVERSAL DETECTED")
	}
	p := currentUserFrom(c)
	uid := int64(0)
	if p != nil {
		uid = p.UserID
	}
	if nodeID := a.nodeIDForServer(b.ServerID); nodeID != "" {
		if !a.nodeOnline(nodeID) {
			return fail(c, 502, "NODE_OFFLINE")
		}
		if err := a.Cluster.BackupRestore(nodeID, b.ServerID, b.BackupID, archive); err != nil {
			return failMsg(c, 502, "NODE_ERROR", err.Error())
		}
		return ok(c, fiber.Map{"restored": archive, "node_id": nodeID})
	}
	if err := a.Backups.Restore(b.BackupID, path, uid); err != nil {
		return failMsg(c, 500, err.Error(), err.Error())
	}
	return ok(c, fiber.Map{"restored": archive})
}

func (a *App) backupArchives(c fiber.Ctx) error {
	b, good := a.loadBackup(c, authz.ServerBackup)
	if !good {
		return nil
	}
	if nodeID := a.nodeIDForServer(b.ServerID); nodeID != "" {
		if !a.nodeOnline(nodeID) {
			return fail(c, 502, "NODE_OFFLINE")
		}
		raw, err := a.Cluster.BackupFiles(nodeID, b.ServerID, b.BackupID, "")
		if err != nil {
			return failMsg(c, 502, "NODE_ERROR", err.Error())
		}
		return ok(c, json.RawMessage(raw))
	}
	archives, err := a.Backups.Archives(b.BackupID)
	if err != nil {
		return failMsg(c, 500, err.Error(), err.Error())
	}
	return ok(c, archives)
}

func (a *App) backupDownload(c fiber.Ctx) error {
	b, good := a.loadBackup(c, authz.ServerBackup)
	if !good {
		return nil
	}
	name := c.Params("name")
	if strings.Contains(name, "..") {
		return fail(c, 400, "TRAVERSAL DETECTED")
	}
	if nodeID := a.nodeIDForServer(b.ServerID); nodeID != "" {
		if !a.nodeOnline(nodeID) {
			return fail(c, 502, "NODE_OFFLINE")
		}
		data, err := a.Cluster.BackupRead(nodeID, b.ServerID, b.BackupID, name)
		if err != nil {
			return failMsg(c, 502, "NODE_ERROR", err.Error())
		}
		c.Set("Content-Disposition", "attachment; filename="+name)
		return c.Send(data)
	}
	path := filepath.Join(b.BackupLocation, name)
	c.Set("Content-Disposition", "attachment; filename="+name)
	return c.SendFile(path)
}

func (a *App) backupArchiveFiles(c fiber.Ctx) error {
	b, good := a.loadBackup(c, authz.ServerBackup)
	if !good {
		return nil
	}
	name := c.Params("name")
	if strings.Contains(name, "..") {
		return fail(c, 400, "TRAVERSAL DETECTED")
	}
	if nodeID := a.nodeIDForServer(b.ServerID); nodeID != "" {
		if !a.nodeOnline(nodeID) {
			return fail(c, 502, "NODE_OFFLINE")
		}
		raw, err := a.Cluster.BackupFiles(nodeID, b.ServerID, b.BackupID, name)
		if err != nil {
			return failMsg(c, 502, "NODE_ERROR", err.Error())
		}
		return ok(c, json.RawMessage(raw))
	}
	path := filepath.Join(b.BackupLocation, name)
	files, err := a.Backups.ListArchiveFiles(path)
	if err != nil {
		return failMsg(c, 500, err.Error(), err.Error())
	}
	return ok(c, files)
}

// ---------------- schedules (tasks) ----------------

func (a *App) taskView(s gen.Schedule) map[string]interface{} {
	v := map[string]interface{}{
		"schedule_id": s.ScheduleID, "server_id": s.ServerID, "enabled": s.Enabled == 1,
		"action": s.Action, "interval": s.Interval, "interval_int": s.IntervalInt,
		"one_time": s.OneTime == 1, "delay": s.Delay, "name": "", "comment": "",
		"command": "", "cron_string": "", "start_time": "", "action_id": "", "parent": nil,
		"next_run": "", "last_run": "",
	}
	if s.Name != nil {
		v["name"] = *s.Name
	}
	if s.Comment != nil {
		v["comment"] = *s.Comment
	}
	if s.Command != nil {
		v["command"] = *s.Command
	}
	if s.CronString != nil {
		v["cron_string"] = *s.CronString
	}
	if s.StartTime != nil {
		v["start_time"] = *s.StartTime
	}
	if s.ActionID != nil {
		v["action_id"] = *s.ActionID
	}
	if s.Parent != nil {
		v["parent"] = *s.Parent
	}
	if s.NextRun != nil {
		v["next_run"] = *s.NextRun
	}
	if s.LastRun != nil {
		v["last_run"] = *s.LastRun
	}
	return v
}

func (a *App) listTasks(c fiber.Ctx) error {
	p := a.requireAuth(c)
	if p == nil {
		return nil
	}
	serverID := c.Params("id")
	if !p.CanServer(serverID, authz.ServerSchedule) {
		return fail(c, 403, "NOT_AUTHORIZED")
	}
	rows, _ := a.DB.ListSchedulesForServer(serverID)
	out := []map[string]interface{}{}
	for _, s := range rows {
		out = append(out, a.taskView(s))
	}
	return ok(c, out)
}

func (a *App) createTask(c fiber.Ctx) error {
	p := a.requireAuth(c)
	if p == nil {
		return nil
	}
	serverID := c.Params("id")
	if !p.CanServer(serverID, authz.ServerSchedule) {
		return fail(c, 403, "NOT_AUTHORIZED")
	}
	var req struct {
		Name        string `json:"name"`
		Action      string `json:"action"`
		Interval    string `json:"interval"`
		IntervalInt int64  `json:"interval_int"`
		StartTime   string `json:"start_time,omitempty"`
		Command     string `json:"command,omitempty"`
		ActionID    string `json:"action_id,omitempty"`
		OneTime     bool   `json:"one_time"`
		CronString  string `json:"cron_string,omitempty"`
		Parent      *int64 `json:"parent,omitempty"`
		Delay       int64  `json:"delay"`
		Comment     string `json:"comment,omitempty"`
		Enabled     bool   `json:"enabled"`
	}
	if err := parseBody(c, &req); err != nil {
		return err
	}
	s := gen.Schedule{
		ServerID: serverID, Enabled: bool2i(req.Enabled), Action: req.Action,
		Interval: req.Interval, IntervalInt: req.IntervalInt, StartTime: &req.StartTime,
		Command: &req.Command, ActionID: &req.ActionID, Name: &req.Name,
		OneTime: bool2i(req.OneTime), CronString: &req.CronString, Parent: req.Parent,
		Delay: req.Delay, Comment: &req.Comment,
	}
	if req.Interval == "" {
		s.Interval = "hours"
	}
	if req.Interval == "reaction" && req.Parent == nil {
		return failMsg(c, 400, "INVALID_JSON_SCHEMA", "reaction tasks need a parent")
	}
	if err := scheduler.ValidateSchedule(s); err != nil {
		return failMsg(c, 400, "INVALID_JSON_SCHEMA", err.Error())
	}
	row, err := a.DB.CreateSchedule(s.ServerID, s.Enabled, s.Action, s.Interval, s.IntervalInt,
		s.StartTime, s.Command, s.ActionID, s.Name, s.OneTime, s.CronString, s.Parent, s.Delay, nil, nil, s.Comment)
	if err != nil {
		return failMsg(c, 400, err.Error(), err.Error())
	}
	a.Log.Audit(p.Username, p.UserID, serverID, c.IP(), "Created schedule "+req.Name)
	return ok(c, a.taskView(row))
}

func (a *App) loadTask(c fiber.Ctx, perm int) (gen.Schedule, bool) {
	p := a.requireAuth(c)
	if p == nil {
		return gen.Schedule{}, false
	}
	tid := int64(0)
	if _, err := fmtSscan(c.Params("task_id"), &tid); err != nil {
		_ = fail(c, 404, "NOT_FOUND")
		return gen.Schedule{}, false
	}
	s, err := a.DB.GetSchedule(tid)
	if err != nil {
		_ = fail(c, 404, "NOT_FOUND")
		return gen.Schedule{}, false
	}
	if !p.CanServer(s.ServerID, perm) {
		_ = fail(c, 403, "NOT_AUTHORIZED")
		return gen.Schedule{}, false
	}
	return s, true
}

func (a *App) getTask(c fiber.Ctx) error {
	s, good := a.loadTask(c, authz.ServerSchedule)
	if !good {
		return nil
	}
	return ok(c, a.taskView(s))
}

func (a *App) patchTask(c fiber.Ctx) error {
	s, good := a.loadTask(c, authz.ServerSchedule)
	if !good {
		return nil
	}
	var req struct {
		Name        *string `json:"name,omitempty"`
		Action      *string `json:"action,omitempty"`
		Interval    *string `json:"interval,omitempty"`
		IntervalInt *int64  `json:"interval_int,omitempty"`
		StartTime   *string `json:"start_time,omitempty"`
		Command     *string `json:"command,omitempty"`
		ActionID    *string `json:"action_id,omitempty"`
		OneTime     *bool   `json:"one_time,omitempty"`
		CronString  *string `json:"cron_string,omitempty"`
		Delay       *int64  `json:"delay,omitempty"`
		Comment     *string `json:"comment,omitempty"`
		Enabled     *bool   `json:"enabled,omitempty"`
	}
	if err := parseBody(c, &req); err != nil {
		return err
	}
	if req.Name != nil {
		s.Name = req.Name
	}
	if req.Action != nil {
		s.Action = *req.Action
	}
	if req.Interval != nil {
		s.Interval = *req.Interval
	}
	if req.IntervalInt != nil {
		s.IntervalInt = *req.IntervalInt
	}
	if req.StartTime != nil {
		s.StartTime = req.StartTime
	}
	if req.Command != nil {
		s.Command = req.Command
	}
	if req.ActionID != nil {
		s.ActionID = req.ActionID
	}
	if req.OneTime != nil {
		s.OneTime = bool2i(*req.OneTime)
	}
	if req.CronString != nil {
		s.CronString = req.CronString
	}
	if req.Delay != nil {
		s.Delay = *req.Delay
	}
	if req.Comment != nil {
		s.Comment = req.Comment
	}
	if req.Enabled != nil {
		s.Enabled = bool2i(*req.Enabled)
	}
	if err := scheduler.ValidateSchedule(s); err != nil {
		return failMsg(c, 400, "INVALID_JSON_SCHEMA", err.Error())
	}
	if err := a.DB.UpdateSchedule(s.Enabled, s.Action, s.Interval, s.IntervalInt, s.StartTime, s.Command,
		s.ActionID, s.Name, s.OneTime, s.CronString, s.Parent, s.Delay, s.NextRun, s.LastRun, s.Comment, s.ScheduleID); err != nil {
		return failMsg(c, 400, err.Error(), err.Error())
	}
	updated, _ := a.DB.GetSchedule(s.ScheduleID)
	return ok(c, a.taskView(updated))
}

func (a *App) deleteTask(c fiber.Ctx) error {
	s, good := a.loadTask(c, authz.ServerSchedule)
	if !good {
		return nil
	}
	if err := a.DB.DeleteSchedule(s.ScheduleID); err != nil {
		return failMsg(c, 400, err.Error(), err.Error())
	}
	return ok(c, fiber.Map{"deleted": s.ScheduleID})
}

func (a *App) taskChildren(c fiber.Ctx) error {
	s, good := a.loadTask(c, authz.ServerSchedule)
	if !good {
		return nil
	}
	children, _ := a.DB.ListChildSchedules(&s.ScheduleID)
	out := []map[string]interface{}{}
	for _, ch := range children {
		out = append(out, a.taskView(ch))
	}
	return ok(c, out)
}

func (a *App) runTask(c fiber.Ctx) error {
	s, good := a.loadTask(c, authz.ServerSchedule)
	if !good {
		return nil
	}
	p := currentUserFrom(c)
	uid := int64(0)
	if p != nil {
		uid = p.UserID
	}
	// build the schedule executor inline (same semantics as the engine)
	exec := a.scheduleExecutor()
	_ = exec
	go func() {
		// run via scheduler engine semantics: exec + children cascade
		exec(s.ServerID, s.Action, deref(s.ActionID), deref(s.Command), uid)
	}()
	lastRun := databaseNow()
	a.DB.SetScheduleNextRun(s.NextRun, &lastRun, s.ScheduleID)
	return ok(c, fiber.Map{"started": s.ScheduleID})
}

func fmtSscan(s string, v *int64) (int, error) {
	n, err := sscanf64(s, v)
	return n, err
}

var _ = bytes.MinRead
var _ = io.EOF
var _ = http.StatusOK
var _ = backups.ErrNotFound
