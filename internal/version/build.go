package version

import (
	"fmt"
	"runtime/debug"
	"strings"
)

// Build identifies the source revision a binary was built from. The Go toolchain
// records it when the build runs inside a Git checkout; a binary built from an
// archive has no revision. Every consumer therefore treats the fields as optional.
type Build struct {
	Revision string
	Modified bool
}

// Current reads the build identity stamped into this binary.
func Current() Build {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return Build{}
	}
	return buildFrom(info)
}

// buildFrom is the pure parser, separate from Current so the missing, malformed
// and modified cases can be tested without depending on how the test binary was
// built.
func buildFrom(info *debug.BuildInfo) Build {
	var build Build
	if info == nil {
		return build
	}
	for _, setting := range info.Settings {
		switch setting.Key {
		case "vcs.revision":
			build.Revision = strings.TrimSpace(setting.Value)
		case "vcs.modified":
			build.Modified = strings.EqualFold(strings.TrimSpace(setting.Value), "true")
		}
	}
	return build
}

// Describe returns the version followed by the short build revision when one is
// available. A binary built from a checkout with uncommitted changes says so,
// because that binary is not reproducible from the commit alone.
func (b Build) Describe() string {
	if b.Revision == "" {
		return Version
	}
	revision := b.Revision
	if len(revision) > 12 {
		revision = revision[:12]
	}
	if b.Modified {
		return fmt.Sprintf("%s (%s, modified)", Version, revision)
	}
	return fmt.Sprintf("%s (%s)", Version, revision)
}
