package models

import "testing"

func TestParseListModels(t *testing.T) {
	// Shape copied from `pi --list-models`: a header, aligned columns, provider
	// names and ids that contain slashes, and yes/no capability flags.
	output := `provider      model                                               context  max-out  thinking  images
cline         ~anthropic/claude-fable-latest                      1M       128K     yes       yes
cline         deepseek/deepseek-v4-flash-latest                  1.3M     943.7K   yes       no
workbuddy     deepseek-v4.1-flash                                 300K     64K      no        yes

Warning: errors loading models.json:
something went wrong`

	got := parse(output)
	want := []Model{
		{Provider: "cline", ID: "~anthropic/claude-fable-latest", Thinking: true, Images: true},
		{Provider: "cline", ID: "deepseek/deepseek-v4-flash-latest", Thinking: true, Images: false},
		{Provider: "workbuddy", ID: "deepseek-v4.1-flash", Thinking: false, Images: true},
	}
	if len(got) != len(want) {
		t.Fatalf("parsed %d models, want %d: %#v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("model %d = %#v, want %#v", i, got[i], want[i])
		}
	}
	if ref := got[0].Reference(); ref != "cline/~anthropic/claude-fable-latest" {
		t.Errorf("Reference() = %q", ref)
	}
}

func TestParseIgnoresProse(t *testing.T) {
	if got := parse("No models available\nrun pi auth first\n"); len(got) != 0 {
		t.Fatalf("parsed %#v, want none", got)
	}
}

// A model id may contain spaces; the id is everything between the provider and
// the last four columns, not just the second whitespace-separated token.
func TestParseKeepsSpacedModelID(t *testing.T) {
	output := `provider      model                        context  max-out  thinking  images
acme          gpt-5.3 codex preview         256K     64K      yes       no`

	got := parse(output)
	if len(got) != 1 {
		t.Fatalf("parsed %#v, want one model", got)
	}
	if got[0].Provider != "acme" || got[0].ID != "gpt-5.3 codex preview" {
		t.Fatalf("model = %#v, want acme/gpt-5.3 codex preview", got[0])
	}
	if !got[0].Thinking || got[0].Images {
		t.Fatalf("flags = %#v, want thinking yes images no", got[0])
	}
}
