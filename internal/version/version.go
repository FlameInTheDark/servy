// Package version reports build information and checks for panel updates
// against the project's GitHub releases (semantic-release publishes them).
package version

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"
)

// Build information — injected via -ldflags at release time:
//
//	-X github.com/FlameInTheDark/servy/internal/version.Version=1.2.3 \
//	-X github.com/FlameInTheDark/servy/internal/version.Commit=$(git rev-parse --short HEAD) \
//	-X github.com/FlameInTheDark/servy/internal/version.BuildDate=$(date -u +%Y-%m-%dT%H:%M:%SZ)
var (
	Version   = "0.0.0-dev"
	Commit    = "unknown"
	BuildDate = "unknown"
)

// UpdateCheckResult is the outcome of an update-check probe.
type UpdateCheckResult struct {
	CurrentVersion  string `json:"current_version"`
	LatestVersion   string `json:"latest_version"`
	UpdateAvailable bool   `json:"update_available"`
	CheckedAt       string `json:"checked_at"`
	ReleaseURL      string `json:"release_url,omitempty"`
	Error           string `json:"error,omitempty"`
}

// GitHubRelease mirrors the GitHub releases API entry (subset).
type GitHubRelease struct {
	Name        string `json:"name"`
	TagName     string `json:"tag_name"`
	HTMLURL     string `json:"html_url"`
	PublishedAt string `json:"published_at"`
}

var client = &http.Client{Timeout: 10 * time.Second}

// releasesURL resolves the update feed:
//  1. SERVY_UPDATE_CHECK_URL env (full API URL; "disabled" turns checks off)
//  2. https://api.github.com/repos/$GITHUB_REPOSITORY/releases/latest
//  3. https://api.github.com/repos/FlameInTheDark/servy/releases/latest
func releasesURL() (string, bool) {
	if v := strings.TrimSpace(os.Getenv("SERVY_UPDATE_CHECK_URL")); v != "" {
		if strings.EqualFold(v, "disabled") || strings.EqualFold(v, "off") {
			return "", false
		}
		return v, true
	}
	repo := "FlameInTheDark/servy"
	if v := strings.TrimSpace(os.Getenv("GITHUB_REPOSITORY")); v != "" {
		repo = v
	}
	return "https://api.github.com/repos/" + repo + "/releases/latest", true
}

// CheckForUpdate fetches the latest GitHub release and compares versions.
// Version tags are normalized by stripping a leading "v".
func CheckForUpdate() (*UpdateCheckResult, error) {
	res := &UpdateCheckResult{
		CurrentVersion: Version,
		CheckedAt:      time.Now().UTC().Format(time.RFC3339),
	}
	url, enabled := releasesURL()
	if !enabled {
		res.Error = "update check disabled"
		return res, nil
	}
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		res.Error = err.Error()
		return res, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := client.Do(req)
	if err != nil {
		res.Error = err.Error()
		return res, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		res.Error = fmt.Sprintf("release feed status %d", resp.StatusCode)
		return res, fmt.Errorf("%s", res.Error)
	}
	var rel GitHubRelease
	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
		res.Error = err.Error()
		return res, err
	}
	res.LatestVersion = strings.TrimPrefix(rel.TagName, "v")
	res.ReleaseURL = rel.HTMLURL
	res.UpdateAvailable = res.LatestVersion != "" && res.LatestVersion != strings.TrimPrefix(Version, "v")
	return res, nil
}
