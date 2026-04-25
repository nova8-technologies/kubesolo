package manifests

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/rs/zerolog/log"
)

const manifestsDir = "manifests"

// Deploy applies user-specified Kubernetes manifests.
// It processes manifests from the KUBESOLO_MANIFESTS env var (newline-separated URLs or file paths)
// and auto-scans the manifests/ directory under basePath.
func Deploy(adminKubeconfig, manifestsList, basePath string) error {
	var allManifests [][]byte

	// Collect manifests from the env var
	if manifestsList != "" {
		entries := strings.Split(manifestsList, "\n")
		for _, entry := range entries {
			entry = strings.TrimSpace(entry)
			if entry == "" || strings.HasPrefix(entry, "#") {
				continue
			}

			data, err := fetchManifest(entry)
			if err != nil {
				log.Error().Err(err).Str("source", entry).Msg("failed to fetch manifest, skipping")
				continue
			}
			log.Info().Str("component", "manifests").Str("source", entry).Msg("loaded manifest")
			allManifests = append(allManifests, data)
		}
	}

	// Auto-scan manifests directory
	dir := filepath.Join(basePath, manifestsDir)
	dirManifests, err := scanDirectory(dir)
	if err != nil {
		log.Debug().Err(err).Str("dir", dir).Msg("no manifests directory found")
	} else {
		allManifests = append(allManifests, dirManifests...)
	}

	if len(allManifests) == 0 {
		log.Info().Str("component", "manifests").Msg("no user manifests to deploy")
		return nil
	}

	log.Info().Str("component", "manifests").Int("count", len(allManifests)).Msg("applying user manifests...")
	return applyAll(adminKubeconfig, allManifests)
}

// fetchManifest retrieves manifest content from a URL or local file path.
func fetchManifest(source string) ([]byte, error) {
	if strings.HasPrefix(source, "http://") || strings.HasPrefix(source, "https://") {
		return fetchURL(source)
	}
	return os.ReadFile(source)
}

// fetchURL downloads manifest content from an HTTP(S) URL.
func fetchURL(url string) ([]byte, error) {
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return nil, fmt.Errorf("HTTP GET failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d from %s", resp.StatusCode, url)
	}

	return io.ReadAll(resp.Body)
}

// scanDirectory reads all .yaml and .yml files from a directory.
func scanDirectory(dir string) ([][]byte, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}

	var results [][]byte
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !strings.HasSuffix(name, ".yaml") && !strings.HasSuffix(name, ".yml") {
			continue
		}

		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			log.Error().Err(err).Str("file", name).Msg("failed to read manifest file, skipping")
			continue
		}
		log.Info().Str("component", "manifests").Str("file", name).Msg("loaded manifest from directory")
		results = append(results, data)
	}
	return results, nil
}
