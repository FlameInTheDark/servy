// Package defaults centralizes user-facing defaults that are meant to be
// adjusted when deploying your own build — one obvious place to edit instead
// of hunting through the codebase.
package defaults

import "os"

// fallbackSteamWineImage is the published image from GitHub Container Registry.
//
//	docker pull ghcr.io/flameinthedark/servy-wine:latest
const fallbackSteamWineImage = "ghcr.io/flameinthedark/servy-wine:latest"

// DockerSteamWineImage is the default container image used to run Steam game
// servers — native Linux servers AND Windows servers via Wine. It is applied
// automatically when a server is created without an explicit image, so users
// never have to type an image reference by hand.
//
// CHANGE THIS when you publish your own build: the CI workflow
// (.github/workflows/docker-publish.yml) pushes docker/steamcmd-wine to
// ghcr.io/flameinthedark/servy-wine — set this value to that
// reference. The SERVY_STEAM_WINE_IMAGE environment variable (if set) wins
// over this constant, so deployments can override it without a rebuild.
var DockerSteamWineImage = envOr("SERVY_STEAM_WINE_IMAGE", fallbackSteamWineImage)

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
