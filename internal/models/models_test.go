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

func TestParseAgyModels(t *testing.T) {
	output := `⠋ Fetching available models...⠙ Fetching available models...gemini-3.8-flash-high     Gemini 3.8 Flash (High)
gemini-3.8-flash-medium   Gemini 3.8 Flash (Medium)
gemini-3.1-pro-low        Gemini 3.1 Pro (Low)
claude-sonnet-4-6         Claude Sonnet 4.6 (Thinking)
gpt-oss-120b-medium       GPT-OSS 120B (Medium)
custom-model-fast         Custom Fast Model`

	got := parseAgyModels(output)
	want := []Model{
		{Provider: "", ID: "gemini-3.8-flash-high", Thinking: true, Images: true},
		{Provider: "", ID: "gemini-3.8-flash-medium", Thinking: true, Images: true},
		{Provider: "", ID: "gemini-3.1-pro-low", Thinking: false, Images: true},
		{Provider: "", ID: "claude-sonnet-4-6", Thinking: true, Images: true},
		{Provider: "", ID: "gpt-oss-120b-medium", Thinking: true, Images: true},
		{Provider: "", ID: "custom-model-fast", Thinking: false, Images: true},
	}
	if len(got) != len(want) {
		t.Fatalf("parsed %d models, want %d: %#v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("model %d = %#v, want %#v", i, got[i], want[i])
		}
	}
	if ref := got[0].Reference(); ref != "gemini-3.8-flash-high" {
		t.Errorf("Reference() = %q, want gemini-3.8-flash-high", ref)
	}
}

func TestDefaultModelForDriver(t *testing.T) {
	if got := DefaultModelForDriver("agy"); got != "gemini-3.8-flash-high" {
		t.Fatalf("DefaultModelForDriver(agy) = %q, want gemini-3.8-flash-high", got)
	}

	piModel := DefaultModelForDriver("pi")
	if piModel == "" {
		t.Fatalf("DefaultModelForDriver(pi) returned empty string")
	}
}

// `opencode models` prints one plain provider/model reference per line.
func TestParseOpencodeModels(t *testing.T) {
	output := "opencode/claude-sonnet-4-6\nanthropic/claude-sonnet-4-5\nopencode/gemini-3.8-flash\nbroken-line-without-provider\n\n/opencode/leading-slash\n"

	got := parseOpencodeModels(output)
	want := []Model{
		{Provider: "opencode", ID: "claude-sonnet-4-6"},
		{Provider: "anthropic", ID: "claude-sonnet-4-5"},
		{Provider: "opencode", ID: "gemini-3.8-flash"},
	}
	if len(got) != len(want) {
		t.Fatalf("parsed %d models, want %d: %#v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("model %d = %#v, want %#v", i, got[i], want[i])
		}
	}
	if ref := got[0].Reference(); ref != "opencode/claude-sonnet-4-6" {
		t.Errorf("Reference() = %q, want opencode/claude-sonnet-4-6", ref)
	}
}

func TestDriverKind(t *testing.T) {
	cases := map[string]string{
		"pi":                          "pi",
		"":                            "pi",
		"agy":                         "agy",
		"agy --log-file /tmp/x.log":   "agy",
		"opencode":                    "opencode",
		"opencode --auto":             "opencode",
		"/opt/tools/duo-opencode/bin": "opencode",
		// The shipped bridges wrap the CLI, so the program is never named exactly
		// "agy" or "opencode". Matching only a "agy" token or a "/agy" path
		// segment read duo-agy as pi, and the picker then ran `--list-models`,
		// which agy answers with its usage instead of a catalog.
		"duo-agy":                          "agy",
		"/usr/local/bin/duo-agy --auto":    "agy",
		"duo-opencode --model gpt-6.1-sol": "opencode",
		`"/opt/my tools/duo-opencode" -a`:  "opencode",
		// A flag value is not the program: a pi run pointed at an agy-named
		// config is still pi.
		"pi --config /tmp/agy.json": "pi",
	}
	for command, want := range cases {
		if got := DriverKind(command); got != want {
			t.Errorf("DriverKind(%q) = %q, want %q", command, got, want)
		}
	}
}

// agy and opencode take a subcommand that accepts no flags. Reusing the launch
// command broke two ways: a subcommand appended at the end is read as the
// positional project path ("Failed to change directory to .../models"), and
// carrying agent flags such as --model makes the subcommand print its usage
// instead of a catalog.
func TestListCommandPlacement(t *testing.T) {
	cases := map[string]string{
		"opencode":                                   "opencode models",
		"opencode --model 'opencode/big-pickle'":     "opencode models",
		"opencode --auto":                            "opencode models",
		"/opt/bin/duo-opencode --auto":               "/opt/bin/duo-opencode models",
		"agy --model 'gemini-3.8-flash-high'":        "agy models",
		"agy --conversation x --log-file /tmp/a.log": "agy models",
	}
	for command, want := range cases {
		got := listCommand(command)
		if got != want {
			t.Errorf("listCommand(%q) = %q, want %q", command, got, want)
		}
	}

	// pi takes a flag, so its custom config flags must survive.
	if got := listCommand("pi --config /tmp/pi.json"); got != "pi --config /tmp/pi.json --list-models" {
		t.Errorf("listCommand(pi) = %q, want the launch command with --list-models appended", got)
	}

	// A quoted binary path is one token even when it contains a space, so the
	// subcommand is appended after the whole path instead of inside it.
	if got := listCommand(`"/opt/my tools/duo-agy" --model x`); got != `"/opt/my tools/duo-agy" models` {
		t.Errorf("listCommand(quoted agy path) = %q", got)
	}
}
