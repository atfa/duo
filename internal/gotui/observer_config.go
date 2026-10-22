package gotui

import (
	"context"
	"image"
	"strings"

	"github.com/atfa/duo/internal/observer"
	"github.com/atfa/duo/internal/session"
	"github.com/gdamore/tcell/v3"
	ui "github.com/metaspartan/gotui/v5"
)

const (
	observerEndpointField = iota
	observerModelField
	observerAPIKeyField
	observerEnabledField
)

// observerConfigOverlay edits only the public observer settings. The API key is
// a transient rune buffer and is never copied into a session command or View.
type observerConfigOverlay struct {
	*ui.Block
	theme      theme
	box        image.Rectangle
	endpoint   []rune
	model      []rune
	apiKey     []rune
	enabled    bool
	active     int
	cursor     int
	view       session.ObserverView
	onboarding bool
}

func newObserverConfig(t theme) *observerConfigOverlay {
	return &observerConfigOverlay{Block: newModalBlock(t), theme: t}
}

func (o *observerConfigOverlay) begin(view session.ObserverView) {
	o.clearSecret()
	o.onboarding = false
	o.view = view
	o.endpoint = []rune(view.Endpoint)
	o.model = []rune(view.Model)
	o.enabled = view.Enabled
	o.active = observerEndpointField
	o.cursor = len(o.endpoint)
}

func (o *observerConfigOverlay) beginOnboarding(view session.ObserverView) {
	o.begin(view)
	o.onboarding = true
	if strings.TrimSpace(view.Endpoint) == "" {
		o.endpoint = []rune("http://127.0.0.1:4000/v1")
	}
	if strings.TrimSpace(view.Model) == "" {
		o.model = []rune("exec")
	}
	o.enabled = false
}

func (o *observerConfigOverlay) clearSecret() {
	for i := range o.apiKey {
		o.apiKey[i] = 0
	}
	o.apiKey = nil
}

func (o *observerConfigOverlay) place(l layout) {
	o.box = centered(l, 78, 17)
	o.Block.SetRect(o.box.Min.X, o.box.Min.Y, o.box.Max.X, o.box.Max.Y)
}

func (o *observerConfigOverlay) field() *[]rune {
	switch o.active {
	case observerEndpointField:
		return &o.endpoint
	case observerModelField:
		return &o.model
	case observerAPIKeyField:
		return &o.apiKey
	default:
		return nil
	}
}

func (o *observerConfigOverlay) moveField(delta int) {
	o.active = (o.active + delta + 4) % 4
	if field := o.field(); field != nil {
		o.cursor = len(*field)
	}
}

func (o *observerConfigOverlay) insert(text string) {
	field := o.field()
	if field == nil {
		return
	}
	value := *field
	if o.cursor < 0 || o.cursor > len(value) {
		o.cursor = len(value)
	}
	insert := []rune(text)
	value = append(value, make([]rune, len(insert))...)
	copy(value[o.cursor+len(insert):], value[o.cursor:len(value)-len(insert)])
	copy(value[o.cursor:], insert)
	o.cursor += len(insert)
	*field = value
}

func (o *observerConfigOverlay) backspace() {
	field := o.field()
	if field == nil || o.cursor <= 0 {
		return
	}
	value := *field
	copy(value[o.cursor-1:], value[o.cursor:])
	value = value[:len(value)-1]
	o.cursor--
	*field = value
}

func (o *observerConfigOverlay) delete() {
	field := o.field()
	if field == nil {
		return
	}
	value := *field
	if o.cursor >= len(value) {
		return
	}
	copy(value[o.cursor:], value[o.cursor+1:])
	*field = value[:len(value)-1]
}

func (o *observerConfigOverlay) displayValue(field int) string {
	var value []rune
	switch field {
	case observerEndpointField:
		value = o.endpoint
	case observerModelField:
		value = o.model
	case observerAPIKeyField:
		value = []rune(strings.Repeat("•", len(o.apiKey)))
	}
	if field != o.active {
		return string(value)
	}
	if o.cursor < 0 || o.cursor > len(value) {
		o.cursor = len(value)
	}
	// Insert the visible cursor into a copy. Appending directly to a field slice
	// can reuse its backing array, corrupting the configured value as it is drawn.
	value = append([]rune(nil), value...)
	value = append(value, 0)
	copy(value[o.cursor+1:], value[o.cursor:len(value)-1])
	value[o.cursor] = '│'
	return string(value)
}

func (o *observerConfigOverlay) Draw(buf *ui.Buffer) {
	fillBlock(buf, o.GetRect(), ui.Style{Fg: o.theme.Fg, Bg: o.theme.PanelHigh})
	title := "OBSERVER SETTINGS"
	if o.onboarding {
		title = "SET UP OBSERVER"
	}
	o.Title = " " + o.theme.tr(title) + " "
	o.Block.Draw(buf)
	if o.Inner.Dx() < 24 || o.Inner.Dy() < 8 {
		return
	}
	x, y := o.Inner.Min.X, o.Inner.Min.Y
	line := func(text string, style ui.Style) {
		if y >= o.Inner.Max.Y {
			return
		}
		buf.SetString(fitText(text, o.Inner.Dx()), style, image.Pt(x, y))
		y++
	}

	description := "Advisory observer; failures do not affect the workflow."
	if o.onboarding {
		description = "Set up the optional advisory observer now, or choose Later."
	}
	line(o.theme.tr(description), o.theme.BodyMuted)
	y++
	fields := []struct {
		label    string
		value    string
		override bool
	}{
		{o.theme.tr("Base URL"), o.displayValue(observerEndpointField), o.view.EnvOverrideEndpoint},
		{o.theme.tr("Model"), o.displayValue(observerModelField), o.view.EnvOverrideModel},
		{o.theme.tr("API key (blank keeps saved key)"), o.displayValue(observerAPIKeyField), false},
	}
	for i, field := range fields {
		style := o.theme.BodyMuted
		if i == o.active {
			style = ui.Style{Fg: o.theme.Fg, Bg: o.theme.SelectBg, Modifier: ui.ModifierBold}
		}
		text := field.label + ": " + field.value
		if field.override {
			text += "  " + o.theme.tr("(controlled by environment)")
		}
		buf.SetString(strings.Repeat(" ", o.Inner.Dx()), style, image.Pt(x, y))
		buf.SetString(fitText(text, o.Inner.Dx()), style, image.Pt(x, y))
		y++
	}

	enabledStyle := o.theme.BodyMuted
	if o.active == observerEnabledField {
		enabledStyle = ui.Style{Fg: o.theme.Fg, Bg: o.theme.SelectBg, Modifier: ui.ModifierBold}
	}
	enabledText := o.theme.tr("Enabled") + ": " + o.theme.tr("OFF")
	if o.enabled {
		enabledText = o.theme.tr("Enabled") + ": " + o.theme.tr("ON")
	}
	if o.view.EnvOverride {
		enabledText += "  " + o.theme.tr("(controlled by environment)")
	}
	buf.SetString(strings.Repeat(" ", o.Inner.Dx()), enabledStyle, image.Pt(x, y))
	buf.SetString(fitText(enabledText, o.Inner.Dx()), enabledStyle, image.Pt(x, y))
	y += 2

	keyStatus := o.theme.tr("no key resolved")
	if o.view.HasKey {
		keyStatus = o.theme.tr("key available")
	}
	if o.view.KeyFile {
		keyStatus += " · " + o.theme.tr("key file present")
	}
	configured := o.theme.tr("never configured")
	if o.view.Configured {
		configured = o.theme.tr("configured")
	}
	line(o.theme.tr("Status")+": "+configured+" · "+keyStatus, o.theme.Timestamp)
	if o.view.EnvOverride || o.view.EnvOverrideEndpoint || o.view.EnvOverrideModel {
		line(o.theme.tr("DUO_OBSERVER* environment values take precedence."), o.theme.Warning)
	}
	hint := o.theme.tr("Tab/Shift+Tab move · Space toggles Enabled · Enter save · Esc cancel")
	if o.onboarding {
		hint = o.theme.tr("Enter configure · Esc Later")
	}
	buf.SetString(fitText(hint, o.Inner.Dx()), o.theme.Timestamp, image.Pt(o.Inner.Min.X, o.Inner.Max.Y-1))
}

func (a *App) handleObserverConfigKey(ev *tcell.EventKey) {
	o := a.observer
	switch ev.Key() {
	case tcell.KeyEsc:
		if o.onboarding {
			a.dismissObserverOnboarding()
			return
		}
		o.clearSecret()
		a.closeOverlay()
	case tcell.KeyTab:
		o.moveField(1)
	case tcell.KeyBacktab:
		o.moveField(-1)
	case tcell.KeyEnter:
		a.saveObserverConfig()
	case tcell.KeyLeft:
		if o.active == observerEnabledField {
			o.enabled = false
		} else if o.cursor > 0 {
			o.cursor--
		}
	case tcell.KeyRight:
		if o.active == observerEnabledField {
			o.enabled = true
		} else if field := o.field(); field != nil && o.cursor < len(*field) {
			o.cursor++
		}
	case tcell.KeyHome:
		if field := o.field(); field != nil {
			o.cursor = 0
		}
	case tcell.KeyEnd:
		if field := o.field(); field != nil {
			o.cursor = len(*field)
		}
	case tcell.KeyBackspace, tcell.KeyBackspace2:
		o.backspace()
	case tcell.KeyDelete:
		o.delete()
	case tcell.KeyCtrlU:
		if field := o.field(); field != nil {
			for i := range *field {
				(*field)[i] = 0
			}
			*field = nil
			o.cursor = 0
		}
	case tcell.KeyRune:
		if ev.Modifiers()&(tcell.ModCtrl|tcell.ModAlt) != 0 {
			return
		}
		if o.active == observerEnabledField {
			if strings.ContainsAny(strings.ToLower(ev.Str()), "y1") {
				o.enabled = true
			} else if strings.ContainsAny(strings.ToLower(ev.Str()), "n0") {
				o.enabled = false
			} else if ev.Str() == " " {
				o.enabled = !o.enabled
			}
			return
		}
		if ev.Str() != "" {
			o.insert(ev.Str())
		}
	}
}

func (a *App) openObserverOnboarding() {
	if a.svc == nil {
		return
	}
	a.closeMention()
	a.observer.beginOnboarding(a.live.Observer)
	a.overlay = overlayObserver
}

func (a *App) dismissObserverOnboarding() {
	if a.svc == nil {
		return
	}
	if err := a.svc.Do(context.Background(), session.SetObserver{Enabled: false}); err != nil {
		a.setStatus(err.Error(), true)
		return
	}
	a.refreshView()
	a.observer.clearSecret()
	a.observer.onboarding = false
	a.closeOverlay()
	a.setStatus(a.theme.tr("observer setup deferred"), false)
}

func (a *App) saveObserverConfig() {
	if a.svc == nil {
		return
	}
	endpoint, model := string(a.observer.endpoint), string(a.observer.model)
	chosen := observer.Config{
		Enabled:   a.observer.enabled,
		Endpoint:  endpoint,
		Model:     model,
		APIKeyEnv: a.live.Observer.APIKeyEnv,
	}.Normalized()
	if err := chosen.Validate(); err != nil {
		a.setStatus(err.Error(), true)
		return
	}
	if err := chosen.WithEnv().Validate(); err != nil {
		a.setStatus(err.Error(), true)
		return
	}
	if len(a.observer.apiKey) > 0 {
		if _, err := observer.SaveAPIKey(string(a.observer.apiKey)); err != nil {
			a.setStatus(a.theme.tr("could not save observer API key")+": "+err.Error(), true)
			return
		}
	}
	cmd := session.SetObserver{
		Enabled:   a.observer.enabled,
		Endpoint:  endpoint,
		Model:     model,
		APIKeyEnv: a.live.Observer.APIKeyEnv,
	}
	if err := a.svc.Do(context.Background(), cmd); err != nil {
		a.setStatus(err.Error(), true)
		return
	}
	a.refreshView()
	a.observer.clearSecret()
	a.observer.onboarding = false
	a.closeOverlay()
	a.setStatus(a.theme.tr("observer settings saved"), false)
}
