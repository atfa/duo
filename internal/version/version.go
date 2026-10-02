// Package version carries the single source of truth for Duo's version string.
//
// It lives in its own package because four separate binaries report it: the main
// CLI, the three driver shims, and the embedded MCP server. Duplication is what
// let the Pi bridge installer drift to a version four releases behind.
package version

// Version is the current Duo release. It carries the "v" prefix because that is
// the form users see in `duo --version` and the form release tags use.
const Version = "v0.9.0"
