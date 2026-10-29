package gotui

import (
	"github.com/gdamore/tcell/v3"
	ui "github.com/metaspartan/gotui/v5"
)

func rgb(r, g, b int32) tcell.Color { return tcell.NewRGBColor(r, g, b) }

// theme is deliberately small: a dark base, one accent per role, and semantic
// colours that are used everywhere instead of ad-hoc literals. TrueColor is
// passed straight to tcell, which falls back for terminals with fewer colours.
type theme struct {
	Bg        tcell.Color
	Panel     tcell.Color
	PanelHigh tcell.Color
	Fg        tcell.Color
	Muted     tcell.Color
	Faint     tcell.Color
	Border    tcell.Color
	Accent    tcell.Color
	Austin    tcell.Color
	Tony      tcell.Color
	Duo       tcell.Color
	Human     tcell.Color
	OK        tcell.Color
	Warn      tcell.Color
	Err       tcell.Color
	CodeBg    tcell.Color
	SelectBg  tcell.Color

	// Lang is shared by every copy of the theme, so switching /language updates
	// the already-constructed widgets without rebuilding the dashboard.
	Lang *string

	Base      ui.Style
	PanelS    ui.Style
	Title     ui.Style
	Subtle    ui.Style
	BorderS   ui.Style
	Body      ui.Style
	BodyMuted ui.Style
	Error     ui.Style
	Warning   ui.Style
	Success   ui.Style
	Code      ui.Style
	Selected  ui.Style
	Timestamp ui.Style
	AustinS   ui.Style
	TonyS     ui.Style
	DuoS      ui.Style
	HumanS    ui.Style
}

// tr translates the static interface labels used by the dashboard. Technical
// values (paths, model ids, phases) stay untranslated; this is chrome, not
// protocol text.
func (t theme) tr(en string) string {
	if t.Lang == nil || *t.Lang != "zh" {
		return en
	}
	if zh, ok := zhUI[en]; ok {
		return zh
	}
	return en
}

func newTheme() theme {
	lang := "en"
	t := theme{
		Lang:      &lang,
		Bg:        rgb(13, 18, 23),
		Panel:     rgb(20, 27, 34),
		PanelHigh: rgb(29, 38, 45),
		Fg:        rgb(220, 227, 236),
		Muted:     rgb(139, 152, 168),
		Faint:     rgb(86, 99, 116),
		Border:    rgb(49, 62, 69),
		Accent:    rgb(111, 218, 193),
		Austin:    rgb(115, 190, 219),
		Tony:      rgb(193, 166, 224),
		Duo:       rgb(255, 194, 102),
		Human:     rgb(137, 230, 152),
		OK:        rgb(110, 231, 183),
		Warn:      rgb(251, 191, 36),
		Err:       rgb(248, 113, 113),
		CodeBg:    rgb(24, 30, 40),
		SelectBg:  rgb(38, 78, 105),
	}
	t.Base = ui.Style{Fg: t.Fg, Bg: t.Bg}
	t.PanelS = ui.Style{Fg: t.Fg, Bg: t.Panel}
	t.Title = ui.Style{Fg: t.Fg, Bg: t.Panel, Modifier: tcell.AttrBold}
	t.Subtle = ui.Style{Fg: t.Muted, Bg: t.Panel}
	t.BorderS = ui.Style{Fg: t.Border, Bg: t.Panel}
	t.Body = ui.Style{Fg: t.Fg, Bg: t.Bg}
	t.BodyMuted = ui.Style{Fg: t.Muted, Bg: t.Bg}
	t.Error = ui.Style{Fg: t.Err, Bg: t.Bg}
	t.Warning = ui.Style{Fg: t.Warn, Bg: t.Bg}
	t.Success = ui.Style{Fg: t.OK, Bg: t.Bg}
	t.Code = ui.Style{Fg: rgb(230, 210, 160), Bg: t.CodeBg}
	t.Selected = ui.Style{Fg: t.Fg, Bg: t.SelectBg}
	t.Timestamp = ui.Style{Fg: t.Faint, Bg: t.Bg}
	t.AustinS = ui.Style{Fg: t.Austin, Bg: t.Bg, Modifier: tcell.AttrBold}
	t.TonyS = ui.Style{Fg: t.Tony, Bg: t.Bg, Modifier: tcell.AttrBold}
	t.DuoS = ui.Style{Fg: t.Duo, Bg: t.Bg, Modifier: tcell.AttrBold}
	t.HumanS = ui.Style{Fg: t.Human, Bg: t.Bg, Modifier: tcell.AttrBold}
	return t
}

func (t theme) agentStyle(id string) ui.Style {
	switch id {
	case "Austin":
		return t.AustinS
	case "Tony":
		return t.TonyS
	case "Duo":
		return t.DuoS
	case "Human":
		return t.HumanS
	default:
		return t.Title
	}
}

func (t theme) agentColor(id string) tcell.Color {
	switch id {
	case "Austin":
		return t.Austin
	case "Tony":
		return t.Tony
	case "Duo":
		return t.Duo
	case "Human":
		return t.Human
	default:
		return t.Accent
	}
}
