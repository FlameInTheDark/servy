package httpapi

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/gofiber/fiber/v3"

	"github.com/FlameInTheDark/servy/internal/cluster"
	"github.com/FlameInTheDark/servy/internal/files"
	"github.com/FlameInTheDark/servy/internal/repo/gen"
)

// Remote file manager: the panel's file API against a server that lives on a
// cluster node. Paths are validated by the node's guard (same code as local),
// the panel only relays bytes.

// remoteEntries converts node directory listings to the local Entry shape so
// the UI renders remote and local folders identically.
func remoteEntries(list []cluster.FileEntry) []files.Entry {
	out := make([]files.Entry, 0, len(list))
	for _, e := range list {
		entry := files.Entry{Name: e.Name, Size: e.Size, Dir: e.Dir}
		if t, err := time.Parse(time.RFC3339, e.Modified); err == nil {
			entry.Modified = t
		}
		out = append(out, entry)
	}
	return out
}

// remoteFilesDir lists a directory on the owning node.
func (a *App) remoteFilesDir(c fiber.Ctx, nodeID, serverID, rel string) error {
	if !a.nodeOnline(nodeID) {
		return fail(c, 502, "NODE_OFFLINE")
	}
	entries, err := a.Cluster.ListFiles(nodeID, serverID, rel)
	if err != nil {
		return failMsg(c, 502, "NODE_ERROR", err.Error())
	}
	root := map[string]interface{}{"path": rel, "name": "..", "dir": true, "root": true}
	return ok(c, fiber.Map{"root": root, "entries": remoteEntries(entries)})
}

// remoteFilesRoute handles every file-manager action for a remote server.
func (a *App) remoteFilesRoute(c fiber.Ctx, nodeID, serverID, rel, action string) error {
	p := currentUserFrom(c)
	user := ""
	if p != nil {
		user = p.Username
	}
	if !a.nodeOnline(nodeID) {
		return fail(c, 502, "NODE_OFFLINE")
	}
	failNode := func(err error) error { return failMsg(c, 502, "NODE_ERROR", err.Error()) }

	switch c.Method() {
	case "GET":
		switch action {
		case "download":
			data, err := a.Cluster.ReadFile(nodeID, serverID, rel, 512<<20)
			if err != nil {
				return fail(c, 404, "NOT_FOUND")
			}
			a.Log.Audit(user, userIDOf(c), serverID, c.IP(), fmt.Sprintf("Downloaded file %s (node %s)", rel, nodeID))
			c.Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", filepath.Base(rel)))
			return c.Send(data)
		case "stat":
			raw, err := a.Cluster.FileOp(nodeID, serverID, "stat", rel, "", nil)
			if err != nil {
				return fail(c, 404, "NOT_FOUND")
			}
			c.Set(fiber.HeaderContentType, fiber.MIMEApplicationJSON)
			return c.Send(raw)
		default:
			data, err := a.Cluster.ReadFile(nodeID, serverID, rel, 16<<20)
			if err != nil {
				return fail(c, 404, "NOT_FOUND")
			}
			return ok(c, fiber.Map{"path": rel, "content": string(data), "size": len(data)})
		}
	case "POST":
		switch action {
		case "create":
			op := "create"
			if c.Query("kind", "file") == "dir" {
				op = "mkdir"
			}
			if _, err := a.Cluster.FileOp(nodeID, serverID, op, rel, "", nil); err != nil {
				return failMsg(c, 400, err.Error(), err.Error())
			}
			return ok(c, fiber.Map{"created": rel})
		case "zip":
			var req struct {
				Paths []string `json:"paths"`
				Out   string   `json:"out"`
			}
			if err := parseBody(c, &req); err != nil {
				return err
			}
			if req.Out == "" {
				req.Out = "archive.zip"
			}
			if _, err := a.Cluster.FileOp(nodeID, serverID, "zip", "", req.Out, req.Paths); err != nil {
				return failMsg(c, 500, err.Error(), err.Error())
			}
			return ok(c, fiber.Map{"zipped": req.Paths, "out": req.Out})
		case "unzip":
			dest := c.Query("dest", ".")
			if _, err := a.Cluster.FileOp(nodeID, serverID, "unzip", rel, dest, nil); err != nil {
				return failMsg(c, 500, err.Error(), err.Error())
			}
			return ok(c, fiber.Map{"unzipped": rel})
		case "move", "copy":
			var req struct {
				Dest string `json:"dest"`
			}
			if err := parseBody(c, &req); err != nil {
				return err
			}
			if _, err := a.Cluster.FileOp(nodeID, serverID, action, rel, req.Dest, nil); err != nil {
				return failMsg(c, 400, err.Error(), err.Error())
			}
			return ok(c, fiber.Map{action + "d": rel, "dest": req.Dest})
		case "upload":
			return a.remoteChunkUpload(c, nodeID, serverID, rel)
		case "write":
			var req struct {
				Content string `json:"content"`
			}
			if err := parseBody(c, &req); err != nil {
				return err
			}
			if err := a.Cluster.WriteFileBytes(nodeID, serverID, rel, []byte(req.Content)); err != nil {
				return failNode(err)
			}
			return ok(c, fiber.Map{"saved": rel})
		}
		return failMsg(c, 400, "INVALID_ACTION", "unknown action "+action)
	case "PUT", "PATCH":
		var req struct {
			Content string `json:"content"`
		}
		if err := parseBody(c, &req); err != nil {
			return err
		}
		if err := a.Cluster.WriteFileBytes(nodeID, serverID, rel, []byte(req.Content)); err != nil {
			return failNode(err)
		}
		a.Log.Audit(user, userIDOf(c), serverID, c.IP(), fmt.Sprintf("Edited file %s (node %s)", rel, nodeID))
		return ok(c, fiber.Map{"saved": rel})
	case "DELETE":
		if _, err := a.Cluster.FileOp(nodeID, serverID, "delete", rel, "", nil); err != nil {
			return failMsg(c, 400, err.Error(), err.Error())
		}
		a.Log.Audit(user, userIDOf(c), serverID, c.IP(), fmt.Sprintf("Deleted %s (node %s)", rel, nodeID))
		return ok(c, fiber.Map{"deleted": rel})
	}
	return fail(c, 405, "SERVER_ERROR")
}

// remoteChunkUpload assembles a chunked upload on the panel, then streams the
// finished file to the node. Keeps request bodies small for strict proxies.
func (a *App) remoteChunkUpload(c fiber.Ctx, nodeID, serverID, rel string) error {
	uploadID := c.Query("upload_id")
	chunk := fiber.Query[int](c, "chunk", -1)
	total := fiber.Query[int](c, "total", -1)
	if uploadID == "" || chunk < 0 || total <= 0 {
		return failMsg(c, 400, "INVALID_JSON_SCHEMA", "upload_id, chunk and total required")
	}
	if err := a.Uploads.SaveChunk(uploadID, chunk, c.Body()); err != nil {
		return failMsg(c, 500, err.Error(), err.Error())
	}
	if chunk != total-1 {
		return ok(c, fiber.Map{"chunk": chunk, "received": true})
	}
	tmp, err := os.CreateTemp(filepath.Join(a.DataDir, "import"), "upload-*")
	if err != nil {
		tmp, err = os.CreateTemp("", "servy-upload-*")
		if err != nil {
			return failMsg(c, 500, err.Error(), err.Error())
		}
	}
	tmpPath := tmp.Name()
	tmp.Close()
	defer os.Remove(tmpPath)
	if err := a.Uploads.Assemble(uploadID, total, tmpPath); err != nil {
		return failMsg(c, 500, err.Error(), err.Error())
	}
	if err := a.Cluster.WriteFile(nodeID, serverID, rel, tmpPath); err != nil {
		return failMsg(c, 502, "NODE_ERROR", err.Error())
	}
	return ok(c, fiber.Map{"uploaded": rel, "assembled": true})
}

// remoteConfigList lists config-editable files inside a remote server folder.
func (a *App) remoteConfigList(c fiber.Ctx, nodeID string, def gen.Server) error {
	if !a.nodeOnline(nodeID) {
		return fail(c, 502, "NODE_OFFLINE")
	}
	entries, err := a.Cluster.ListFiles(nodeID, def.ServerID, "")
	if err != nil {
		return failMsg(c, 500, "CONFIG_LIST_FAILED", err.Error())
	}
	known := map[string]map[string]interface{}{}
	addFile := func(file, label, description, editor string) {
		if info := configInfoFor(file, label, description, editor); info != nil {
			known[info["file"].(string)] = info
		}
	}
	for _, entry := range entries {
		if entry.Dir || !isEditableConfigName(entry.Name) {
			continue
		}
		addFile(entry.Name, "", "", "")
	}
	game, hasGame, err := a.serverGameManifest(def)
	if err != nil {
		return failMsg(c, 500, "INVALID_SERVER_DEFINITION", err.Error())
	}
	if hasGame {
		exists := func(name string) bool {
			if _, err := a.Cluster.FileOp(nodeID, def.ServerID, "stat", name, "", nil); err != nil {
				return false
			}
			return true
		}
		for _, declared := range game.ConfigFiles {
			file := path.Clean(strings.ReplaceAll(declared, `\`, "/"))
			if exists(file) {
				addFile(file, "", "", "")
			}
		}
		for _, descriptor := range game.ConfigEditors {
			file := path.Clean(strings.ReplaceAll(descriptor.Path, `\`, "/"))
			if exists(file) {
				addFile(file, descriptor.Label, descriptor.Description, descriptor.Editor)
			}
		}
	}
	filesList := make([]map[string]interface{}, 0, len(known))
	for _, info := range known {
		filesList = append(filesList, info)
	}
	return ok(c, sortConfigList(filesList))
}
