package version

import (
	"runtime/debug"
	"testing"
)

// TestBuildFromReadsVCSMetadata pins the parser on hand-built BuildInfo values:
// a test binary is not built with VCS metadata, so asserting a real revision
// here would test the Go toolchain, not this code.
func TestBuildFromReadsVCSMetadata(t *testing.T) {
	cases := []struct {
		name string
		info *debug.BuildInfo
		want Build
	}{
		{name: "missing build info", info: nil, want: Build{}},
		{
			name: "no vcs settings",
			info: &debug.BuildInfo{Settings: []debug.BuildSetting{{Key: "GOOS", Value: "linux"}}},
			want: Build{},
		},
		{
			name: "revision and clean",
			info: &debug.BuildInfo{Settings: []debug.BuildSetting{
				{Key: "vcs.revision", Value: "abcdef1234567890"},
				{Key: "vcs.modified", Value: "false"},
			}},
			want: Build{Revision: "abcdef1234567890"},
		},
		{
			name: "revision and modified",
			info: &debug.BuildInfo{Settings: []debug.BuildSetting{
				{Key: "vcs.revision", Value: "abcdef1234567890"},
				{Key: "vcs.modified", Value: "true"},
			}},
			want: Build{Revision: "abcdef1234567890", Modified: true},
		},
		{
			name: "values are trimmed and modified is case-insensitive",
			info: &debug.BuildInfo{Settings: []debug.BuildSetting{
				{Key: "vcs.revision", Value: "  abcdef  "},
				{Key: "vcs.modified", Value: " TRUE "},
			}},
			want: Build{Revision: "abcdef", Modified: true},
		},
		{
			name: "malformed modified is not true",
			info: &debug.BuildInfo{Settings: []debug.BuildSetting{
				{Key: "vcs.modified", Value: "yes"},
			}},
			want: Build{},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := buildFrom(tc.info); got != tc.want {
				t.Fatalf("buildFrom() = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestBuildDescribe(t *testing.T) {
	cases := []struct {
		name  string
		build Build
		want  string
	}{
		{name: "no revision", build: Build{}, want: Version},
		{name: "short revision", build: Build{Revision: "abcdef"}, want: Version + " (abcdef)"},
		{name: "long revision is shortened", build: Build{Revision: "abcdef1234567890"}, want: Version + " (abcdef123456)"},
		{name: "modified is marked", build: Build{Revision: "abcdef1234567890", Modified: true}, want: Version + " (abcdef123456, modified)"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.build.Describe(); got != tc.want {
				t.Fatalf("Describe() = %q, want %q", got, tc.want)
			}
		})
	}
}
