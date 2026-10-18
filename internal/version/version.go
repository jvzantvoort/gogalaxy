// Package version holds build information, overridable through -ldflags.
package version

var (
	// Version is the gogalaxy release version.
	Version = "dev"
	// Commit is the VCS revision the binary was built from.
	Commit = ""
	// Date is the build time (RFC 3339).
	Date = ""
	// GalaxyNGCompat is the galaxy_ng version reported to clients.
	GalaxyNGCompat = "4.10.0"
)
