// Package version holds the build version, overridden at release time with -ldflags.
package version

// Version is set via -ldflags "-X github.com/siddik7181/zdm/internal/version.Version=...".
var Version = "dev"

// String returns the build version.
func String() string { return Version }
