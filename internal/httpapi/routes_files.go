package httpapi

import (
        "archive/zip"
        "errors"
        "fmt"
        "net/url"
        "os"
        "path/filepath"
        "strings"

        "github.com/gofiber/fiber/v3"

        "github.com/FlameInTheDark/servy/internal/authz"
        "github.com/FlameInTheDark/servy/internal/files"
)

func (a *App) mountFiles(api fiber.Router) {
        sg := api.Group("/servers/:id/files")
        sg.Get("/", a.filesList)
        sg.Get("/*", a.filesRoute)
        sg.Post("/*", a.filesRoute)
        sg.Put("/*", a.filesRoute)
        sg.Patch("/*", a.filesRoute)
        sg.Delete("/*", a.filesRoute)
        // import archive upload (wizard step 1)
        api.Post("/servers/import/upload", a.importUpload)
        api.Post("/import/archive/select", a.importArchiveSelect)
}

// filesRoute multiplexes by method+query action (original-style file API).
func (a *App) filesRoute(c fiber.Ctx) error {
        p := a.requireAuth(c)
        if p == nil {
                return nil
        }
        serverID := c.Params("id")
        if !p.CanServer(serverID, authz.ServerFiles) {
                return fail(c, 403, "NOT_AUTHORIZED")
        }
        def, err := a.DB.GetServer(serverID)
        if err != nil {
                return fail(c, 404, "SERVER_NOT_FOUND")
        }
        rel := strings.TrimPrefix(c.Path(), "/api/v1/servers/"+serverID+"/files")
        rel = strings.TrimPrefix(rel, "/")
        // c.Path() keeps the URL-encoded form (fiber v2 PathOriginal), so a
        // filename like "my world.zip" arrives as "my%20world.zip". Decode
        // it back so files are stored/read under their real names; if the raw
        // path is not valid percent-encoding, use it as-is (legacy clients).
        if dec, derr := url.PathUnescape(rel); derr == nil {
                rel = dec
        }
        action := c.Query("action")
        if nodeID := a.nodeIDForServer(serverID); nodeID != "" {
                return a.remoteFilesRoute(c, nodeID, serverID, rel, action)
        }
        guard, err := files.NewGuard(def.Path)
        if err != nil {
                return fail(c, 500, "SERVER_ERROR")
        }

        switch c.Method() {
        case "GET":
                switch action {
                case "download":
                        data, err := a.Files.Read(guard, rel, 512<<20)
                        if err != nil {
                                return fail(c, 404, "NOT_FOUND")
                        }
                        a.Log.Audit(p.Username, p.UserID, serverID, c.IP(), fmt.Sprintf("Downloaded file %s", rel))
                        c.Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", filepath.Base(rel)))
                        return c.Send(data)
                case "stat":
                        entry, err := a.Files.Stat(guard, rel)
                        if err != nil {
                                return fail(c, 404, "NOT_FOUND")
                        }
                        return ok(c, entry)
                default:
                        data, err := a.Files.Read(guard, rel, 16<<20)
                        if err != nil {
                                if err == files.ErrTraversal {
                                        return fail(c, 400, "TRAVERSAL DETECTED")
                                }
                                return fail(c, 404, "NOT_FOUND")
                        }
                        return ok(c, fiber.Map{"path": rel, "content": string(data), "size": len(data)})
                }
        case "POST":
                switch action {
                case "create":
                        kind := c.Query("kind", "file")
                        if kind == "dir" {
                                if err := a.Files.CreateDir(guard, rel); err != nil {
                                        return failMsg(c, 400, err.Error(), err.Error())
                                }
                        } else {
                                if err := a.Files.CreateFile(guard, rel); err != nil {
                                        return failMsg(c, 400, err.Error(), err.Error())
                                }
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
                        if err := a.Files.Zip(guard, req.Paths, req.Out); err != nil {
                                return failMsg(c, 500, err.Error(), err.Error())
                        }
                        return ok(c, fiber.Map{"zipped": req.Paths, "out": req.Out})
                case "unzip":
                        dest := c.Query("dest", ".")
                        if err := a.Files.Unzip(guard, rel, dest); err != nil {
                                if err == files.ErrTraversal {
                                        return fail(c, 400, "TRAVERSAL DETECTED")
                                }
                                return failMsg(c, 500, err.Error(), err.Error())
                        }
                        return ok(c, fiber.Map{"unzipped": rel})
                case "move":
                        var req struct {
                                Dest string `json:"dest"`
                        }
                        if err := parseBody(c, &req); err != nil {
                                return err
                        }
                        if err := a.Files.Move(guard, rel, req.Dest); err != nil {
                                if err == files.ErrTraversal {
                                        return fail(c, 400, "TRAVERSAL DETECTED")
                                }
                                return failMsg(c, 400, err.Error(), err.Error())
                        }
                        return ok(c, fiber.Map{"moved": rel, "dest": req.Dest})
                case "copy":
                        var req struct {
                                Dest string `json:"dest"`
                        }
                        if err := parseBody(c, &req); err != nil {
                                return err
                        }
                        if err := a.Files.Copy(guard, rel, req.Dest); err != nil {
                                if err == files.ErrTraversal {
                                        return fail(c, 400, "TRAVERSAL DETECTED")
                                }
                                return failMsg(c, 400, err.Error(), err.Error())
                        }
                        return ok(c, fiber.Map{"copied": rel, "dest": req.Dest})
                case "upload":
                        // chunked upload: ?chunk=N&total=M&upload_id=X
                        return a.filesChunkUpload(c, guard, rel)
                case "write":
                        var req struct {
                                Content string `json:"content"`
                        }
                        if err := parseBody(c, &req); err != nil {
                                return err
                        }
                        if err := a.Files.Write(guard, rel, []byte(req.Content)); err != nil {
                                return failMsg(c, 400, err.Error(), err.Error())
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
                if err := a.Files.Write(guard, rel, []byte(req.Content)); err != nil {
                        return failMsg(c, 400, err.Error(), err.Error())
                }
                a.Log.Audit(p.Username, p.UserID, serverID, c.IP(), fmt.Sprintf("Edited file %s", rel))
                return ok(c, fiber.Map{"saved": rel})
        case "DELETE":
                if err := a.Files.Delete(guard, rel); err != nil {
                        if err == files.ErrTraversal {
                                return fail(c, 400, "TRAVERSAL DETECTED")
                        }
                        return failMsg(c, 400, err.Error(), err.Error())
                }
                a.Log.Audit(p.Username, p.UserID, serverID, c.IP(), fmt.Sprintf("Deleted %s", rel))
                return ok(c, fiber.Map{"deleted": rel})
        }
        return fail(c, 405, "SERVER_ERROR")
}

// filesList lists a directory (?path=subdir).
func (a *App) filesList(c fiber.Ctx) error {
        return a.filesRouteDir(c)
}

func (a *App) filesRouteDir(c fiber.Ctx) error {
        p := a.requireAuth(c)
        if p == nil {
                return nil
        }
        serverID := c.Params("id")
        if !p.CanServer(serverID, authz.ServerFiles) {
                return fail(c, 403, "NOT_AUTHORIZED")
        }
        def, err := a.DB.GetServer(serverID)
        if err != nil {
                return fail(c, 404, "SERVER_NOT_FOUND")
        }
        rel := c.Query("path", "")
        if nodeID := a.nodeIDForServer(serverID); nodeID != "" {
                return a.remoteFilesDir(c, nodeID, serverID, rel)
        }
        guard, _ := files.NewGuard(def.Path)
        entries, err := a.Files.List(guard, rel)
        if err != nil {
                if err == files.ErrTraversal {
                        return fail(c, 400, "TRAVERSAL DETECTED")
                }
                return fail(c, 404, "NOT_FOUND")
        }
        root := map[string]interface{}{"path": rel, "name": "..", "dir": true, "root": true}
        return ok(c, fiber.Map{"root": root, "entries": entries})
}

// filesChunkUpload assembles chunked uploads (POST .../files/<path>?action=upload).
func (a *App) filesChunkUpload(c fiber.Ctx, guard *files.Guard, rel string) error {
        uploadID := c.Query("upload_id")
        chunk := fiber.Query[int](c, "chunk", -1)
        total := fiber.Query[int](c, "total", -1)
        if uploadID == "" || chunk < 0 || total <= 0 {
                return failMsg(c, 400, "INVALID_JSON_SCHEMA", "upload_id, chunk and total required")
        }
        if err := a.Uploads.SaveChunk(uploadID, chunk, c.Body()); err != nil {
                return failMsg(c, 500, err.Error(), err.Error())
        }
        if chunk == total-1 {
                target, err := guard.Validate(rel)
                if err != nil {
                        return fail(c, 400, "TRAVERSAL DETECTED")
                }
                if err := a.Uploads.Assemble(uploadID, total, target); err != nil {
                        return failMsg(c, 500, err.Error(), err.Error())
                }
                return ok(c, fiber.Map{"uploaded": rel, "assembled": true})
        }
        return ok(c, fiber.Map{"chunk": chunk, "received": true})
}

// importUpload receives the zip for the creation wizard. Two modes:
//   - single request with the raw body (legacy / small archives)
//   - chunked: ?upload_id=&chunk=N&total=M — every request stays small
//     (works behind strict reverse proxies, no giant bodies in RAM) and the
//     UI can render real progress.
// The assembled archive is verified to be a readable zip before the wizard
// is allowed to continue (catches truncated/corrupt uploads early).
func (a *App) importUpload(c fiber.Ctx) error {
        p := a.requireAuth(c)
        if p == nil {
                return nil
        }
        if !p.CanPanel(authz.PanelServerCreation) {
                return fail(c, 403, "NOT_AUTHORIZED")
        }
        name := c.Query("name")
        if name == "" {
                name = "upload.zip"
        }
        if !strings.HasSuffix(name, ".zip") {
                name += ".zip"
        }
        guard := files.NewGuardOrPanic(a.ImportDir)
        dest, err := guard.Validate(name)
        if err != nil {
                return fail(c, 400, "TRAVERSAL DETECTED")
        }

        uploadID := c.Query("upload_id")
        if uploadID == "" {
                // single-shot raw body
                if err := a.Files.Write(guard, name, c.Body()); err != nil {
                        return failMsg(c, 500, err.Error(), err.Error())
                }
                if err := checkZipReadable(dest); err != nil {
                        os.Remove(dest)
                        return failMsg(c, 400, "INVALID_ARCHIVE", err.Error())
                }
                a.Log.Audit(p.Username, p.UserID, "", c.IP(), fmt.Sprintf("Uploaded import archive %s", name))
                return ok(c, fiber.Map{"path": dest, "name": name, "size": len(c.Body())})
        }

        // chunked mode
        chunk := fiber.Query[int](c, "chunk", -1)
        total := fiber.Query[int](c, "total", -1)
        if chunk < 0 || total <= 0 {
                return failMsg(c, 400, "INVALID_JSON_SCHEMA", "upload_id, chunk and total required")
        }
        if err := a.Uploads.SaveChunk(uploadID, chunk, c.Body()); err != nil {
                return failMsg(c, 500, err.Error(), err.Error())
        }
        if chunk != total-1 {
                return ok(c, fiber.Map{"chunk": chunk, "received": true})
        }
        if err := a.Uploads.Assemble(uploadID, total, dest); err != nil {
                return failMsg(c, 500, err.Error(), err.Error())
        }
        if err := checkZipReadable(dest); err != nil {
                os.Remove(dest)
                return failMsg(c, 400, "INVALID_ARCHIVE", err.Error())
        }
        size := int64(0)
        if st, err := os.Stat(dest); err == nil {
                size = st.Size()
        }
        a.Log.Audit(p.Username, p.UserID, "", c.IP(), fmt.Sprintf("Uploaded import archive %s", name))
        return ok(c, fiber.Map{"path": dest, "name": name, "size": size})
}

// checkZipReadable opens the zip central directory (fast — does not read
// entry data) to verify the archive is complete and parseable.
func checkZipReadable(path string) error {
        zr, err := zip.OpenReader(path)
        if err != nil {
                return fmt.Errorf("not a valid zip archive: %v", err)
        }
        defer zr.Close()
        if len(zr.File) == 0 {
                return errors.New("zip archive is empty")
        }
        return nil
}

// importArchiveSelect peeks into an uploaded zip: lists top-level entries +
// detected executables so the wizard can prefill.
func (a *App) importArchiveSelect(c fiber.Ctx) error {
        p := a.requireAuth(c)
        if p == nil {
                return nil
        }
        var req struct {
                Path string `json:"path"`
        }
        if err := parseBody(c, &req); err != nil {
                return err
        }
        guard := files.NewGuardOrPanic(a.ImportDir)
        name := strings.TrimPrefix(req.Path, a.ImportDir+"/")
        name = strings.TrimPrefix(name, a.ImportDir)
        data, err := a.Files.Read(guard, name, 0)
        if err != nil {
                return fail(c, 404, "NOT_FOUND")
        }
        zipInfo, err := a.readZipInfo(data)
        if err != nil {
                return failMsg(c, 400, "INVALID_ARCHIVE", err.Error())
        }
        _ = data
        return ok(c, zipInfo)
}
