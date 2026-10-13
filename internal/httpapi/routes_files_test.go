package httpapi

import (
        "archive/zip"
        "bytes"
        "encoding/json"
        "fmt"
        "io"
        "net/http"
        "net/http/httptest"
        "os"
        "path/filepath"
        "testing"

        "github.com/gofiber/fiber/v3"

        "github.com/FlameInTheDark/servy/internal/authz"
        "github.com/FlameInTheDark/servy/internal/files"
        "github.com/FlameInTheDark/servy/internal/logging"
)

// uploadTestApp wires an App with just enough for importUpload: import dir,
// file manager, chunk spool and audit log. A middleware injects the given
// principal (requireAuth checks c.Locals("principal") first).
func uploadTestAppWith(t *testing.T, principal *authz.Principal) (*fiber.App, string) {
        t.Helper()
        root := t.TempDir()
        importDir := filepath.Join(root, "import", "upload")
        if err := os.MkdirAll(importDir, 0o755); err != nil {
                t.Fatal(err)
        }
        uploads, err := files.NewUploadStore(importDir)
        if err != nil {
                t.Fatal(err)
        }
        log, err := logging.NewManager(filepath.Join(root, "logs"), 100)
        if err != nil {
                t.Fatal(err)
        }
        a := &App{ImportDir: importDir, Files: files.New(), Uploads: uploads, Log: log}
        app := fiber.New()
        app.Use(func(c fiber.Ctx) error {
                c.Locals("principal", principal)
                return c.Next()
        })
        api := app.Group("/api/v1")
        api.Post("/servers/import/upload", a.importUpload)
        return app, importDir
}

func uploadTestApp(t *testing.T) (*fiber.App, string) {
        return uploadTestAppWith(t, &authz.Principal{Username: "admin", Enabled: true, Superuser: true})
}

// testZipBytes builds an in-memory zip from name->content entries.
func testZipBytes(t *testing.T, entries map[string]string) []byte {
        t.Helper()
        var buf bytes.Buffer
        zw := zip.NewWriter(&buf)
        for name, content := range entries {
                w, err := zw.Create(name)
                if err != nil {
                        t.Fatal(err)
                }
                if _, err := w.Write([]byte(content)); err != nil {
                        t.Fatal(err)
                }
        }
        if err := zw.Close(); err != nil {
                t.Fatal(err)
        }
        return buf.Bytes()
}

// postUpload posts one chunk and decodes the JSON envelope.
func postUpload(t *testing.T, app *fiber.App, url string, body []byte) (int, map[string]interface{}) {
        t.Helper()
        req := httptest.NewRequest(http.MethodPost, url, bytes.NewReader(body))
        req.Header.Set("Content-Type", "application/octet-stream")
        resp, err := app.Test(req)
        if err != nil {
                t.Fatalf("POST %s: %v", url, err)
        }
        raw, _ := io.ReadAll(resp.Body)
        resp.Body.Close()
        var env map[string]interface{}
        if err := json.Unmarshal(raw, &env); err != nil {
                t.Fatalf("POST %s: non-JSON response: %s", url, raw)
        }
        return resp.StatusCode, env
}

// TestImportUploadChunked verifies the wizard's chunked upload mode: small
// per-chunk requests, spool assembly into the import dir, zip validation and
// a final response carrying the absolute path the wizard sends back as
// archive_path. Regression guard for "server archive upload doesn't work"
// (single-body uploads died at proxies / the 512MB body limit with no progress).
func TestImportUploadChunked(t *testing.T) {
        app, importDir := uploadTestApp(t)
        data := testZipBytes(t, map[string]string{
                "myworld/server.properties": "server-port=25565\n",
                "myworld/start.sh":          "#!/bin/bash\n",
        })

        // 3 chunks
        const total = 3
        size := (len(data) + total - 1) / total
        var final map[string]interface{}
        for i := 0; i < total; i++ {
                lo := i * size
                hi := lo + size
                if hi > len(data) {
                        hi = len(data)
                }
                url := fmt.Sprintf("/api/v1/servers/import/upload?name=world.zip&upload_id=t1&chunk=%d&total=%d", i, total)
                status, env := postUpload(t, app, url, data[lo:hi])
                if status != 200 || env["status"] != "ok" {
                        t.Fatalf("chunk %d: status=%d env=%v", i, status, env)
                }
                if i < total-1 {
                        d, _ := env["data"].(map[string]interface{})
                        if d == nil || d["received"] != true {
                                t.Fatalf("chunk %d: expected {received:true}, got %v", i, env["data"])
                        }
                } else {
                        final, _ = env["data"].(map[string]interface{})
                }
        }
        if final == nil {
                t.Fatal("final chunk returned no data payload")
        }
        if final["path"] == nil || final["name"] != "world.zip" {
                t.Fatalf("final response missing path/name: %v", final)
        }
        if sz, _ := final["size"].(float64); int(sz) != len(data) {
                t.Fatalf("final size = %v, want %d", final["size"], len(data))
        }

        // assembled file must be byte-identical and a valid zip
        got, err := os.ReadFile(filepath.Join(importDir, "world.zip"))
        if err != nil {
                t.Fatalf("assembled file missing: %v", err)
        }
        if !bytes.Equal(got, data) {
                t.Fatalf("assembled file corrupted: %d bytes, want %d", len(got), len(data))
        }
        zr, err := zip.OpenReader(filepath.Join(importDir, "world.zip"))
        if err != nil {
                t.Fatalf("assembled file is not a valid zip: %v", err)
        }
        zr.Close()
}

// TestImportUploadSingleShot: legacy raw-body mode still works and rejects
// non-zip payloads with INVALID_ARCHIVE (nothing left on disk).
func TestImportUploadSingleShot(t *testing.T) {
        app, importDir := uploadTestApp(t)

        // valid zip in one request
        status, env := postUpload(t, app, "/api/v1/servers/import/upload?name=one.zip", testZipBytes(t, map[string]string{"a.txt": "hi"}))
        if status != 200 || env["status"] != "ok" {
                t.Fatalf("single-shot upload failed: %d %v", status, env)
        }
        if d, _ := env["data"].(map[string]interface{}); d == nil || d["path"] == nil {
                t.Fatalf("single-shot response missing path: %v", env)
        }

        // garbage body → INVALID_ARCHIVE, file removed
        status, env = postUpload(t, app, "/api/v1/servers/import/upload?name=bad.zip", []byte("this is not a zip at all"))
        if status != 400 {
                t.Fatalf("garbage upload: status=%d, want 400", status)
        }
        if env["error"] != "INVALID_ARCHIVE" {
                t.Fatalf("garbage upload: error=%v, want INVALID_ARCHIVE", env["error"])
        }
        if _, err := os.Stat(filepath.Join(importDir, "bad.zip")); !os.IsNotExist(err) {
                t.Error("invalid archive was left on disk after rejection")
        }
}

// TestImportUploadChunkedInvalidZip: a chunked transfer that assembles into
// garbage must fail on the final chunk (the wizard surfaces the error).
func TestImportUploadChunkedInvalidZip(t *testing.T) {
        app, importDir := uploadTestApp(t)
        status, env := postUpload(t, app,
                "/api/v1/servers/import/upload?name=bad2.zip&upload_id=t2&chunk=0&total=1",
                []byte("definitely not zip data"))
        if status != 400 || env["error"] != "INVALID_ARCHIVE" {
                t.Fatalf("chunked garbage: status=%d env=%v, want 400 INVALID_ARCHIVE", status, env)
        }
        if _, err := os.Stat(filepath.Join(importDir, "bad2.zip")); !os.IsNotExist(err) {
                t.Error("invalid chunked archive was left on disk after rejection")
        }
}

// TestImportUploadRequiresCreationPerm: non-superuser without the
// SERVER_CREATION panel bit is rejected.
func TestImportUploadRequiresCreationPerm(t *testing.T) {
        app, _ := uploadTestAppWith(t, &authz.Principal{Username: "peon", Enabled: true, PanelPerms: "000"})
        status, env := postUpload(t, app, "/api/v1/servers/import/upload?name=x.zip", testZipBytes(t, map[string]string{"a": "b"}))
        if status != 403 {
                t.Fatalf("limited user upload: status=%d env=%v, want 403", status, env)
        }
}
