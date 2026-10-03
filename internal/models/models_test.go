package models

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/atfa/duo/internal/driver"
)

// fake is a driver handle with no process behind it, which is what makes the
// catalog testable without launching an agent.
type fake struct {
	manifest *driver.Manifest
	list     *driver.ModelList
	err      error
}

func (f fake) Manifest() *driver.Manifest        { return f.manifest }
func (f fake) Capabilities() driver.Capabilities { return f.manifest.Capabilities }
func (f fake) Models(context.Context) (*driver.ModelList, error) {
	return f.list, f.err
}

func manifestFor(name, reference string, caps driver.Capabilities) *driver.Manifest {
	return &driver.Manifest{Name: name, ModelReference: reference, Capabilities: caps}
}

func withModels(caps driver.Capabilities) driver.Capabilities {
	caps.Models = true
	return caps
}

func TestReferenceFollowsTheDeclaredFormat(t *testing.T) {
	cases := []struct {
		name     string
		model    Model
		expected string
	}{
		{"qualified joins provider and id", Model{Provider: "anthropic", ID: "sonnet"}, "anthropic/sonnet"},
		{"bare is the id alone", Model{ID: "gemini-3.8-flash-high", Bare: true}, "gemini-3.8-flash-high"},
		{"bare ignores a provider that arrived anyway", Model{Provider: "google", ID: "flash", Bare: true}, "flash"},
		{"no provider falls back to the id", Model{ID: "local-model"}, "local-model"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.model.Reference(); got != tc.expected {
				t.Fatalf("Reference() = %q, want %q", got, tc.expected)
			}
		})
	}
}

// TestLoadCarriesTheDeclaredFormatOntoEveryModel is why the format rides on the
// model: the picker displays Reference, and the reference it displays has to be the
// one the driver will be sent.
func TestLoadCarriesTheDeclaredFormatOntoEveryModel(t *testing.T) {
	src := fake{
		manifest: manifestFor("agy", driver.ModelBare, withModels(driver.Capabilities{})),
		list: &driver.ModelList{Models: []driver.Model{
			{Provider: "google", ID: "flash", Thinking: true},
			{ID: "pro"},
		}},
	}
	got, err := Load(context.Background(), src)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 models, got %d", len(got))
	}
	if got[0].Reference() != "flash" {
		t.Errorf("bare driver produced %q", got[0].Reference())
	}
	if !got[0].Thinking {
		t.Error("thinking was dropped crossing the protocol")
	}
	if !got[0].Bare || !got[1].Bare {
		t.Error("the declared format was not carried onto the models")
	}
}

func TestLoadQualifiedKeepsTheProvider(t *testing.T) {
	src := fake{
		manifest: manifestFor("pi", driver.ModelQualified, withModels(driver.Capabilities{})),
		list:     &driver.ModelList{Models: []driver.Model{{Provider: "anthropic", ID: "sonnet"}}},
	}
	got, err := Load(context.Background(), src)
	if err != nil {
		t.Fatal(err)
	}
	if got[0].Reference() != "anthropic/sonnet" {
		t.Fatalf("Reference() = %q", got[0].Reference())
	}
}

// TestLoadReportsADriverThatPublishesNothing covers the distinction the picker needs:
// an empty catalog and a broken one are different things to tell a human.
func TestLoadReportsADriverThatPublishesNothing(t *testing.T) {
	src := fake{manifest: manifestFor("quiet", driver.ModelBare, driver.Capabilities{})}
	_, err := Load(context.Background(), src)
	if err == nil {
		t.Fatal("expected an error for a driver that publishes no catalog")
	}
	if want := "quiet"; !strings.Contains(err.Error(), want) {
		t.Fatalf("error should name the driver, got %q", err)
	}
}

func TestLoadPropagatesAFailedCall(t *testing.T) {
	boom := errors.New("no such agent")
	src := fake{
		manifest: manifestFor("broken", driver.ModelBare, withModels(driver.Capabilities{})),
		err:      boom,
	}
	if _, err := Load(context.Background(), src); !errors.Is(err, boom) {
		t.Fatalf("want the underlying failure, got %v", err)
	}
}

func TestLoadRejectsAMissingDriver(t *testing.T) {
	if _, err := Load(context.Background(), nil); err == nil {
		t.Fatal("expected an error with no driver at all")
	}
}

// TestDefaultComesFromTheManifest is the replacement for a table of driver names in
// Core: the driver declares its own default, and nothing here knows any driver's name.
func TestDefaultComesFromTheManifest(t *testing.T) {
	if got := Default(manifestFor("x", driver.ModelBare, driver.Capabilities{})); got != "" {
		t.Errorf("a driver that declares no default must get none, got %q", got)
	}
	man := manifestFor("x", driver.ModelBare, driver.Capabilities{})
	man.DefaultModel = "  some/model  "
	if got := Default(man); got != "some/model" {
		t.Errorf("Default() = %q", got)
	}
	if got := Default(nil); got != "" {
		t.Errorf("Default(nil) = %q", got)
	}
}

// TestApplyFoldsAReferenceIntoTheDeclaredFormat covers the failure this replaced: a
// model persisted for one driver reaching another, which aborts that agent at startup
// rather than being ignored.
func TestApplyFoldsAReferenceIntoTheDeclaredFormat(t *testing.T) {
	cases := []struct {
		reference, format, expected string
	}{
		{"google/gemini-3.8-flash-high", driver.ModelBare, "gemini-3.8-flash-high"},
		{"gemini-3.8-flash-high", driver.ModelBare, "gemini-3.8-flash-high"},
		{"", driver.ModelBare, ""},
		{"opencode/space-bunny", driver.ModelQualified, "opencode/space-bunny"},
		{"anthropic/sonnet", driver.ModelQualified, "anthropic/sonnet"},
		{"sonnet", driver.ModelQualified, "sonnet"},
	}
	for _, tc := range cases {
		if got := Apply(tc.reference, tc.format); got != tc.expected {
			t.Errorf("Apply(%q, %q) = %q, want %q", tc.reference, tc.format, got, tc.expected)
		}
	}
}

func contains(haystack, needle string) bool {
	return len(haystack) >= len(needle) && (haystack == needle ||
		len(needle) == 0 || indexOf(haystack, needle) >= 0)
}

func indexOf(haystack, needle string) int {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}
