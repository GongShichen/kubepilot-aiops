// Package artifactlayout defines the shared, human-readable artifact layout
// used by every benchmark suite. Runtime/API run IDs remain in manifests, but
// are deliberately not used as directory names.
package artifactlayout

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

var unsafeComponent = regexp.MustCompile(`[^a-z0-9._-]+`)

// RunDirectory returns root/<suite>/<profile>/<UTC timestamp>. Timestamped
// directories keep concurrent runs separate without exposing opaque IDs.
func RunDirectory(root, suite, profile string, started time.Time) string {
	if strings.TrimSpace(profile) == "" {
		profile = "default"
	}
	return filepath.Join(root, component(suite), component(profile), started.UTC().Format("20060102T150405.000Z"))
}

func RunLabel(suite, profile string, started time.Time) string {
	return fmt.Sprintf("%s-%s-%s", component(suite), component(profile), started.UTC().Format("20060102T150405.000Z"))
}

func component(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	value = unsafeComponent.ReplaceAllString(value, "-")
	value = strings.Trim(value, "-._")
	if value == "" {
		return "unknown"
	}
	return value
}
