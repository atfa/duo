package gotui

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/atfa/duo/internal/project"
	"github.com/atfa/duo/internal/protocol"
	"github.com/atfa/duo/internal/sessionstore"
	"github.com/gdamore/tcell/v3"
)

func requireChineseStatus(t *testing.T, app *App, want string) {
	t.Helper()
	if !strings.Contains(app.status, want) {
		t.Fatalf("status = %q, want it to contain %q", app.status, want)
	}
}

// TestChineseStatusFeedback drives the real handlers in a zh session and checks
// the human-visible status, not just the translation table. Values that belong
// to the agents, drivers, models or the core stay verbatim.
func TestChineseStatusFeedback(t *testing.T) {
	fake := newFakeSession()
	fake.view.Language = "zh"
	fake.history = nil // /copy must report that there is nothing to copy.
	app := New(fake)
	if *app.theme.Lang != "zh" {
		t.Fatalf("theme language = %q, want zh", *app.theme.Lang)
	}

	app.runSlash("/clear")
	requireChineseStatus(t, app, "屏幕已重绘")

	app.runSlash("/preview")
	requireChineseStatus(t, app, "已隐藏工作流侧边栏")
	app.handleKey(tcell.NewEventKey(tcell.KeyCtrlP, "", tcell.ModNone))
	requireChineseStatus(t, app, "已显示工作流侧边栏")

	app.runSlash("/timestamps")
	requireChineseStatus(t, app, "已隐藏消息时间戳")
	app.handleKey(tcell.NewEventKey(tcell.KeyCtrlG, "", tcell.ModNone))
	requireChineseStatus(t, app, "已显示消息时间戳")

	app.runSlash("/status")
	requireChineseStatus(t, app, "状态已输出到时间线")

	app.runSlash("/mode fast")
	requireChineseStatus(t, app, "已处于 FAST 模式")
	app.runSlash("/mode fast goal")
	requireChineseStatus(t, app, "用法：/mode")

	app.runSlash("/copy")
	requireChineseStatus(t, app, "尚未向人类发送消息")

	// A copy that fails keeps the core error verbatim and translates the prefix.
	fake.history = []sessionstore.TUIEntry{{Pane: "Austin", Text: "answer", Label: "Austin → Human"}}
	copier := New(fake)
	copier.clipboard = func(string) error { return errors.New("boom") }
	copier.runSlash("/copy")
	requireChineseStatus(t, copier, "复制失败：boom")

	// Submitting in FAST and outside FAST must read differently in Chinese.
	copier.composer.SetText("task")
	copier.submit()
	requireChineseStatus(t, copier, "已发送给 Austin")

	fake.mu.Lock()
	fake.view.Project.Mode = project.ModeGoal
	fake.mu.Unlock()
	copier.refreshView()
	copier.composer.SetText("task")
	copier.submit()
	requireChineseStatus(t, copier, "已发送给 Austin 和 Tony")

	// Image paste: unavailable and error branches.
	copier.pasteImage = nil
	copier.pasteImageFromClipboard()
	requireChineseStatus(t, copier, "剪贴板图片粘贴不可用")
	copier.pasteImage = func() (string, error) { return "", errors.New("no tool") }
	copier.pasteImageFromClipboard()
	requireChineseStatus(t, copier, "未粘贴图片：no tool")

	// Restart and driver/model changes keep the id and translate the sentence.
	copier.restartAgent(protocol.Austin)
	requireChineseStatus(t, copier, "Austin 已重启")

	fake.mu.Lock()
	fake.view.Project.Started = false
	fake.mu.Unlock()
	copier.refreshView()
	copier.openDriverPicker()
	copier.applyDriver(true)
	requireChineseStatus(t, copier, "驱动 →")

	copier.openPicker()
	select {
	case result := <-copier.asyncCh:
		copier.handleAsync(result)
	case <-time.After(2 * time.Second):
		t.Fatal("model catalog did not load")
	}
	copier.applyPickerModel(true)
	requireChineseStatus(t, copier, "模型 →")
}

// TestStatusMessagesHaveChineseTranslations guards the paths that are awkward to
// drive directly (terminal errors, opened links, view preferences) without
// replacing the behavioural test above.
func TestStatusMessagesHaveChineseTranslations(t *testing.T) {
	keys := []string{
		"sent to Austin",
		"sent to Austin and Tony",
		"screen redrawn",
		"status reported in timeline",
		"workflow sidebar shown",
		"workflow sidebar hidden",
		"timestamps shown",
		"timestamps hidden",
		"already in %s mode",
		"usage: /mode [fast|goal|step]",
		"mode → %s; pause is still active — use /continue or /unpause",
		"mode → %s; agents not resumed yet",
		"%s driver → %s (new process starts idle)",
		"%s model → %s",
		"%s restarted",
		"Austin has not sent a message to the human yet",
		"copy failed: %s",
		"clipboard image paste is unavailable; use Ctrl+A / Ctrl+T to paste in the agent's native terminal",
		"no image pasted: %s; use Ctrl+A / Ctrl+T to paste in the agent's native terminal",
		"no image pasted; use Ctrl+A / Ctrl+T to paste in the agent's native terminal",
		"pasted image path %s",
		"terminal error: %s",
		"Ctrl+Q quits · Esc closes dialogs",
		"open failed: %s",
		"opened %s",
		"detached from %s",
		"unknown command %q (type / to see commands)",
		"could not read TUI view preference: %s",
		"could not save TUI view preference: %s",
		"Austin and Tony resumed in %s mode",
	}
	for _, key := range keys {
		if zhUI[key] == "" {
			t.Errorf("no zh translation for status %q", key)
		}
	}
}

// The /status fallback is Duo's own text, so a zh session must not print the
// English "no status reported" into the timeline.
func TestChineseStatusFallbackWhenNoStatusReported(t *testing.T) {
	fake := newFakeSession()
	fake.view.Language = "zh"
	fake.statusText = "  "
	app := New(fake)
	app.runSlash("/status")
	if len(app.entries) == 0 {
		t.Fatal("no timeline entry was added")
	}
	last := app.entries[len(app.entries)-1]
	if !strings.Contains(last.text, "未报告状态") {
		t.Fatalf("fallback entry = %q, want the zh text", last.text)
	}
	if strings.Contains(last.text, "no status reported") {
		t.Fatalf("fallback entry leaked English: %q", last.text)
	}
}
