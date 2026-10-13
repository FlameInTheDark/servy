// Package bigbucket implements the download-catalog manifest client (jars,
// zips, steam, hytale): manifests are cached locally, refreshing every 12h with
// a graceful fallback to the cached copy.
package bigbucket

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

// Manifest is the cached big bucket structure (superset across manifest types).
type Manifest struct {
	LastRefreshed string     `json:"last_refreshed"`
	Categories    []Category `json:"categories"`
}

// Category groups servers.
type Category struct {
	Name    string  `json:"name"`
	Servers []Entry `json:"servers"`
}

// Entry is one downloadable server artifact.
type Entry struct {
	Name    string `json:"name"`
	Type    string `json:"type"` // java | bedrock | forge | paper | steam | hytale | ...
	Version string `json:"version"`
	URL     string `json:"url"`
	// Steam-specific
	AppID    string `json:"app_id,omitempty"`
	Workshop bool   `json:"workshop,omitempty"`
	OS       string `json:"os,omitempty"`
	Desc     string `json:"desc,omitempty"`
}

// Client fetches and caches manifests.
type Client struct {
	baseURL  string
	cacheDir string
	http     *http.Client
}

// New builds a client rooted at the configured repo URL.
func New(baseURL, cacheDir string) *Client {
	return &Client{baseURL: baseURL, cacheDir: cacheDir, http: &http.Client{Timeout: 30 * time.Second}}
}

// cachePath returns the local cache file for a manifest kind.
func (c *Client) cachePath(kind string) string {
	return filepath.Join(c.cacheDir, kind+".json")
}

// Get returns the manifest for kind ("manifest" = jars, "steamapps", "hytale"),
// refreshing when stale (>12h). Falls back to any cached copy on network error.
func (c *Client) Get(kind string) (*Manifest, error) {
	os.MkdirAll(c.cacheDir, 0o755)
	path := c.cachePath(kind)
	var cached Manifest
	if data, err := os.ReadFile(path); err == nil {
		if json.Unmarshal(data, &cached) == nil {
			if cached.LastRefreshed == "" || time.Since(mustParse(cached.LastRefreshed)) < 12*time.Hour {
				return &cached, nil
			}
		}
	}
	// refresh (two URL shapes: kind.json and /kind)
	for _, url := range []string{
		fmt.Sprintf("%s/%s.json", c.baseURL, kind),
		fmt.Sprintf("%s/%s", c.baseURL, kind),
	} {
		resp, err := c.http.Get(url)
		if err != nil || resp.StatusCode != http.StatusOK {
			if resp != nil {
				resp.Body.Close()
			}
			continue
		}
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
		resp.Body.Close()
		var fresh Manifest
		if json.Unmarshal(body, &fresh) == nil && len(fresh.Categories) > 0 {
			fresh.LastRefreshed = time.Now().UTC().Format(time.RFC3339)
			if out, err := json.MarshalIndent(&fresh, "", "  "); err == nil {
				os.WriteFile(path, out, 0o644)
			}
			return &fresh, nil
		}
	}
	if cached.LastRefreshed != "" || len(cached.Categories) > 0 {
		return &cached, nil // stale but usable
	}
	return nil, fmt.Errorf("manifest %q unavailable (network error) and no cache", kind)
}

// ListJars returns the java jar manifest.
func (c *Client) ListJars() (*Manifest, error) { return c.Get("manifest") }

// ListSteamApps returns the steam apps manifest.
func (c *Client) ListSteamApps() (*Manifest, error) { return c.Get("steamapps") }

// ListHytale returns the hytale manifest.
func (c *Client) ListHytale() (*Manifest, error) { return c.Get("hytale") }

// ForceRefresh drops caches (panel JarCache refresh).
func (c *Client) ForceRefresh() {
	for _, kind := range []string{"manifest", "steamapps", "hytale"} {
		os.Remove(c.cachePath(kind))
	}
}

func mustParse(s string) time.Time {
	t, _ := time.Parse(time.RFC3339, s)
	if t.IsZero() {
		return time.Now().Add(24 * time.Hour) // unparsable = treat as fresh
	}
	return t
}
