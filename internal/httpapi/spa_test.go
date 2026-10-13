package httpapi

import (
        "io"
        "net/http"
        "net/http/httptest"
        "strings"
        "testing"
        "testing/fstest"

        "github.com/gofiber/fiber/v3"
)

// fakeSPA builds an in-memory frontend build for SPA serving tests.
func fakeSPA() fstest.MapFS {
        return fstest.MapFS{
                "index.html":         {Data: []byte(`<html><body>panel <script src="/assets/app-4f2a.js"></script></body></html>`)},
                "favicon.svg":        {Data: []byte("<svg/>")},
                "assets/app-4f2a.js": {Data: []byte("console.log(1)")},
                "assets/app-4f2a.css": {Data: []byte("body{}")},
                "assets/logo.woff2":  {Data: []byte("font-bytes")},
        }
}

// spaTestApp mounts the API index + mountSPA exactly like the real router.
func spaTestApp() *fiber.App {
        app := fiber.New()
        app.Get("/api/v1/", func(c fiber.Ctx) error {
                return c.JSON(fiber.Map{"status": "ok"})
        })
        mountSPA(app)
        return app
}

// TestSPAServing verifies the embedded-UI serving path end to end: correct
// Content-Type from Fiber's MIME map, index fallback for client-side routes,
// immutable caching for hashed assets, and no shadowing of API routes.
// Regression guard for the "panel downloads instead of rendering" and
// "all assets return index.html" bugs.
func TestSPAServing(t *testing.T) {
        SPAFiles = fakeSPA()
        t.Cleanup(func() { SPAFiles = nil })

        app := spaTestApp()

        cases := []struct {
                path        string
                ctype       string
                cache       string // "" = don't check
                bodyContains string
        }{
                {"/", "text/html", "", "panel"},
                {"/index.html", "text/html", "", "panel"},
                {"/assets/app-4f2a.js", "text/javascript", "public, max-age=31536000, immutable", "console.log"},
                {"/assets/app-4f2a.css", "text/css", "public, max-age=31536000, immutable", "body"},
                {"/assets/logo.woff2", "font/woff2", "public, max-age=31536000, immutable", ""},
                {"/favicon.svg", "image/svg+xml", "", ""},
                // SPA fallback: client-side route with no backing file
                {"/servers/abc123/console", "text/html", "", "panel"},
        }
        for _, tc := range cases {
                resp, err := app.Test(httptest.NewRequest(http.MethodGet, tc.path, nil))
                if err != nil {
                        t.Fatalf("GET %s: %v", tc.path, err)
                }
                body, _ := io.ReadAll(resp.Body)
                resp.Body.Close()
                if got := resp.Header.Get("Content-Type"); !strings.HasPrefix(got, tc.ctype) {
                        t.Errorf("GET %s: Content-Type = %q, want prefix %q", tc.path, got, tc.ctype)
                }
                if tc.cache != "" {
                        if got := resp.Header.Get("Cache-Control"); got != tc.cache {
                                t.Errorf("GET %s: Cache-Control = %q, want %q", tc.path, got, tc.cache)
                        }
                }
                if tc.bodyContains != "" && !strings.Contains(string(body), tc.bodyContains) {
                        t.Errorf("GET %s: body does not contain %q (got %d bytes) — asset fell back to index.html?",
                                tc.path, tc.bodyContains, len(body))
                }
        }

        // API must never be shadowed by the SPA catch-all.
        resp, err := app.Test(httptest.NewRequest(http.MethodGet, "/api/v1/", nil))
        if err != nil {
                t.Fatal(err)
        }
        resp.Body.Close()
        if resp.StatusCode != 200 || !strings.HasPrefix(resp.Header.Get("Content-Type"), "application/json") {
                t.Errorf("api index: status=%d content-type=%q, want 200 application/json",
                        resp.StatusCode, resp.Header.Get("Content-Type"))
        }

        // Unknown API paths must NOT serve the SPA shell.
        resp, err = app.Test(httptest.NewRequest(http.MethodGet, "/api/v1/does-not-exist", nil))
        if err != nil {
                t.Fatal(err)
        }
        body, _ := io.ReadAll(resp.Body)
        resp.Body.Close()
        if resp.StatusCode != 404 {
                t.Errorf("unknown api path: status=%d, want 404", resp.StatusCode)
        }
        if strings.Contains(string(body), "<html") {
                t.Error("unknown api path returned the SPA shell instead of a JSON 404")
        }
}

// TestSPAHeadRequest: HEAD on the shell returns headers only.
func TestSPAHeadRequest(t *testing.T) {
        SPAFiles = fakeSPA()
        t.Cleanup(func() { SPAFiles = nil })

        app := spaTestApp()
        resp, err := app.Test(httptest.NewRequest(http.MethodHead, "/", nil))
        if err != nil {
                t.Fatal(err)
        }
        resp.Body.Close()
        if resp.StatusCode != 200 || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/html") {
                t.Errorf("HEAD /: status=%d content-type=%q", resp.StatusCode, resp.Header.Get("Content-Type"))
        }
        if resp.Header.Get("Content-Length") == "" {
                t.Error("HEAD /: missing Content-Length")
        }
}

// TestSPANoEmbed: with no frontend wired, requests fall through to 404 JSON
// (agent mode / API-only deployments).
func TestSPANoEmbed(t *testing.T) {
        app := spaTestApp()
        resp, err := app.Test(httptest.NewRequest(http.MethodGet, "/", nil))
        if err != nil {
                t.Fatal(err)
        }
        resp.Body.Close()
        if resp.StatusCode != 404 {
                t.Errorf("no-embed GET /: status=%d, want 404", resp.StatusCode)
        }
}
