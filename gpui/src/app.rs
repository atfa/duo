//! The window: state, input and rendering.
//!
//! It owns a copy of the last `View`, the timeline it has been sent, a text buffer
//! and a channel of frames from the host. It holds no session handle, so the window
//! cannot start, stop or sign anything: it sends a command and waits for the next
//! view, exactly like a terminal interface.

use std::hash::{DefaultHasher, Hash, Hasher};
use std::io::Write as _;
use std::ops::Range;
use std::path::PathBuf;
use std::time::{Duration, Instant};

use gpui::prelude::*;
use gpui::{
    canvas, div, fill, point, px, relative, rgb, rgba, size, AnyElement, App, Bounds,
    ClipboardItem, Context, CursorStyle, Decorations, Element, ElementInputHandler, Entity,
    EntityInputHandler, FocusHandle, Focusable, FontWeight, GlobalElementId, HitboxBehavior,
    KeyDownEvent, LayoutId, MouseButton, MouseDownEvent, MouseMoveEvent, MouseUpEvent, PaintQuad,
    Pixels, Point, Render, ResizeEdge, Rgba, ScrollHandle, Size, StatefulInteractiveElement, Style,
    TextRun, UTF16Selection, Window, WindowAppearance, WrappedLine,
};
use smol::channel::Receiver;

use crate::commands::{
    classify_submission, search_help, slash_completions, SlashAction, SlashCommand, Submission,
};
use crate::protocol::{Agent, ClientMessage, Event, Model, ServerMessage, View, WorkPreview};
use crate::timeline::{parse_markdown, Block, Span};

/// The lowercase wire spellings. The host normalizes identity once, at its own
/// boundary, so this side uses these constants and never re-cases a value it
/// received.
const AGENT_AUSTIN: &str = "austin";
const AGENT_TONY: &str = "tony";

/// The one mode whose session accepts an escalation to Goal.
const MODE_FAST: &str = "fast";
const COMPOSER_PLACEHOLDER: &str = "Type a task and press Enter…";

/// The timeline keeps the most recent entries. A window is not an archive, and an
/// unbounded vector in a process that may run for hours is a leak.
const MAX_EVENTS: usize = 500;

#[derive(Clone, Debug, PartialEq, Eq)]
struct ModelPicker {
    agent: String,
    filter: String,
    selected: usize,
}

#[derive(Clone, Debug)]
struct PendingModelChange {
    agent: String,
    model: Model,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
enum ModelSwitchDecision {
    AlreadyCurrent,
    Apply,
    ConfirmRestart,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
enum CatalogState {
    Loading,
    Error,
    Empty,
    NoMatches,
    Ready,
}

fn matching_models(agent: &Agent, filter: &str) -> Vec<Model> {
    let filter = filter.trim().to_lowercase();
    agent
        .models
        .iter()
        .filter(|model| {
            filter.is_empty()
                || model.reference.to_lowercase().contains(&filter)
                || model.provider.to_lowercase().contains(&filter)
                || model.id.to_lowercase().contains(&filter)
        })
        .cloned()
        .collect()
}

fn catalog_state(agent: Option<&Agent>, filter: &str) -> CatalogState {
    let Some(agent) = agent else {
        return CatalogState::Loading;
    };
    if !agent.models_loaded {
        return CatalogState::Loading;
    }
    if !agent.models_error.is_empty() {
        return CatalogState::Error;
    }
    if agent.models.is_empty() {
        return CatalogState::Empty;
    }
    if matching_models(agent, filter).is_empty() {
        return CatalogState::NoMatches;
    }
    CatalogState::Ready
}

fn model_selection_command(agent: &str, model: &Model) -> ClientMessage {
    ClientMessage::set_model(agent, model.provider.clone(), model.id.clone())
}

fn model_switch_decision(agent: &Agent, model: &Model) -> ModelSwitchDecision {
    if agent.model == model.reference || agent.model == model.id {
        ModelSwitchDecision::AlreadyCurrent
    } else if agent.live_model_switch {
        ModelSwitchDecision::Apply
    } else {
        ModelSwitchDecision::ConfirmRestart
    }
}

fn model_restart_warning(agent: &str, model: &Model) -> String {
    let agent = match agent {
        AGENT_AUSTIN => "Austin",
        AGENT_TONY => "Tony",
        other => other,
    };
    format!(
        "Changing {agent} to {} will restart {agent} and interrupt its current turn. Press Enter to confirm or Esc to cancel.",
        model.reference
    )
}

fn reset_slash_palette_after_text_edit(
    dismissed: &mut bool,
    selected: &mut usize,
    selection_explicit: &mut bool,
) {
    *dismissed = false;
    *selected = 0;
    *selection_explicit = false;
}

#[derive(Clone, Copy)]
struct Theme {
    light: bool,
    background: Rgba,
    surface: Rgba,
    foreground: Rgba,
    muted: Rgba,
    faint: Rgba,
    border: Rgba,
    secondary: Rgba,
    input: Rgba,
    hint: Rgba,
    warning: Rgba,
    error: Rgba,
}

impl Theme {
    fn from_appearance(appearance: WindowAppearance) -> Self {
        let light = matches!(
            appearance,
            WindowAppearance::Light | WindowAppearance::VibrantLight
        );
        if light {
            Self {
                light,
                background: rgb(0xf5f7fb),
                surface: rgb(0xffffff),
                foreground: rgb(0x202735),
                muted: rgb(0x515d72),
                faint: rgb(0x657187),
                border: rgb(0xd8deea),
                secondary: rgb(0x3f4c60),
                input: rgb(0x202735),
                hint: rgb(0x707c90),
                warning: rgb(0x8a5a00),
                error: rgb(0xb33b3b),
            }
        } else {
            Self {
                light,
                background: rgb(0x0f1116),
                surface: rgb(0x151922),
                foreground: rgb(0xd8dbe2),
                muted: rgb(0x8b93a5),
                faint: rgb(0x6b7280),
                border: rgb(0x252a35),
                secondary: rgb(0xa9b1c1),
                input: rgb(0xe6e9ef),
                hint: rgb(0x5b6472),
                warning: rgb(0xd9a441),
                error: rgb(0xe06c6c),
            }
        }
    }

    fn speaker(self, agent: &str) -> Rgba {
        match (self.light, agent) {
            (false, AGENT_AUSTIN) => rgb(0x6fc7d8),
            (true, AGENT_AUSTIN) => rgb(0x166579),
            (false, AGENT_TONY) => rgb(0xd08cd8),
            (true, AGENT_TONY) => rgb(0x874091),
            (false, "duo") => rgb(0xe0c063),
            (true, "duo") => rgb(0x846500),
            (false, "human") => rgb(0x7ec97e),
            (true, "human") => rgb(0x33763e),
            _ => self.muted,
        }
    }

    fn mode(self, mode: &str) -> Rgba {
        match (self.light, mode) {
            (false, "FAST") => rgb(0x6fc7d8),
            (true, "FAST") => rgb(0xb5e3eb),
            (false, "STEP") => rgb(0x7ec97e),
            (true, "STEP") => rgb(0xc8e8c8),
            (false, _) => rgb(0xd08cd8),
            (true, _) => rgb(0xead1ed),
        }
    }
}

pub struct DuoApp {
    view: Option<View>,
    events: Vec<Event>,
    selected_event: Option<usize>,
    timeline_focused: bool,
    timeline_scroll: ScrollHandle,
    input: String,
    selection: Range<usize>,
    marked_range: Option<Range<usize>>,
    last_layout: Vec<ComposerVisualLine>,
    last_bounds: Option<Bounds<Pixels>>,
    selecting: bool,
    selection_anchor: usize,
    history: Vec<String>,
    history_index: Option<usize>,
    history_draft: Option<String>,
    /// The last rejected command or the reason the session ended.
    banner: Option<String>,
    closed: bool,
    overview_open: bool,
    timestamps: bool,
    help_open: bool,
    help_query: String,
    slash_dismissed: bool,
    slash_selected: usize,
    slash_selection_explicit: bool,
    quit_deadline: Option<Instant>,
    model_picker: Option<ModelPicker>,
    pending_model_change: Option<PendingModelChange>,
    work_preview_open: bool,
    focus: FocusHandle,
}

impl DuoApp {
    pub fn new(rx: Receiver<ServerMessage>, cx: &mut Context<Self>) -> Self {
        cx.spawn(async move |this, cx| {
            while let Ok(message) = rx.recv().await {
                // A failed update means the window is gone, and there is nothing
                // left to deliver to.
                if this.update(cx, |app, cx| app.apply(message, cx)).is_err() {
                    break;
                }
            }
        })
        .detach();

        Self {
            view: None,
            events: Vec::new(),
            selected_event: None,
            timeline_focused: false,
            timeline_scroll: ScrollHandle::new(),
            input: String::new(),
            selection: 0..0,
            marked_range: None,
            last_layout: Vec::new(),
            last_bounds: None,
            selecting: false,
            selection_anchor: 0,
            history: load_history(),
            history_index: None,
            history_draft: None,
            banner: None,
            closed: false,
            overview_open: false,
            timestamps: false,
            help_open: false,
            help_query: String::new(),
            slash_dismissed: false,
            slash_selected: 0,
            slash_selection_explicit: false,
            quit_deadline: None,
            model_picker: None,
            pending_model_change: None,
            work_preview_open: false,
            focus: cx.focus_handle(),
        }
    }

    fn apply(&mut self, message: ServerMessage, cx: &mut Context<Self>) {
        match message {
            ServerMessage::View { view } => self.view = Some(view),
            ServerMessage::Event { event } => {
                self.events.push(event);
                if self.events.len() > MAX_EVENTS {
                    let excess = self.events.len() - MAX_EVENTS;
                    self.events.drain(..excess);
                    self.selected_event = selection_after_front_drain(self.selected_event, excess);
                }
            }
            ServerMessage::Error { text } => self.banner = Some(text),
            ServerMessage::Closed { text } => {
                self.closed = true;
                self.banner = Some(text);
            }
        }
        cx.notify();
    }

    /// Writes one command frame. stdout carries the protocol and nothing else, so
    /// this is the only thing that ever writes to it.
    fn send(&self, message: ClientMessage) {
        let Ok(line) = serde_json::to_string(&message) else {
            return;
        };
        let mut out = std::io::stdout().lock();
        if writeln!(out, "{line}").is_ok() {
            let _ = out.flush();
        }
    }

    fn on_key(&mut self, event: &KeyDownEvent, cx: &mut Context<Self>) {
        let keystroke = &event.keystroke;

        // This safety shortcut remains available while either overlay owns the
        // keyboard, including when an unfinished session needs confirmation.
        if keystroke.modifiers.control && keystroke.key.eq_ignore_ascii_case("q") {
            if self.request_quit(Instant::now()) {
                self.send(ClientMessage::quit());
                cx.quit();
            } else {
                cx.notify();
            }
            return;
        }

        if self.model_picker.is_some() {
            self.on_model_picker_key(event, cx);
            return;
        }

        if self.help_open {
            self.on_help_key(event, cx);
            return;
        }

        if self.marked_range.is_none() && self.slash_menu_open() {
            match (keystroke.key.as_str(), keystroke.modifiers.shift) {
                ("up", false) => {
                    self.move_slash_selection(-1);
                    cx.notify();
                    return;
                }
                ("down", false) => {
                    self.move_slash_selection(1);
                    cx.notify();
                    return;
                }
                ("tab", false) => {
                    self.complete_slash_command();
                    cx.notify();
                    return;
                }
                ("enter", false) => {
                    if self.activate_slash_palette(cx) {
                        cx.notify();
                        return;
                    }
                }
                ("escape", _) => {
                    self.slash_dismissed = true;
                    cx.notify();
                    return;
                }
                _ => {}
            }
        }

        if self.timeline_focused {
            let visible = self
                .timeline_scroll
                .bottom_item()
                .saturating_sub(self.timeline_scroll.top_item())
                .saturating_add(1)
                .max(1);
            let delta = match keystroke.key.as_str() {
                "escape" => {
                    self.timeline_focused = false;
                    cx.notify();
                    return;
                }
                "up" => Some(-1),
                "down" => Some(1),
                "pageup" => Some(-(visible as isize)),
                "pagedown" => Some(visible as isize),
                "home" => Some(isize::MIN),
                "end" => Some(isize::MAX),
                _ => None,
            };
            if let Some(delta) = delta {
                self.move_timeline_selection(delta);
                cx.notify();
                return;
            }
        }

        if self.timeline_focused
            && (keystroke.modifiers.control || keystroke.modifiers.platform)
            && keystroke.key.eq_ignore_ascii_case("c")
        {
            self.copy_selected_event(cx);
            cx.notify();
            return;
        }

        // Select the whole composer with the platform's standard shortcut.
        // GPUI reports Command on macOS and Control on Linux/Windows.
        if (keystroke.modifiers.control || keystroke.modifiers.platform)
            && keystroke.key.eq_ignore_ascii_case("a")
        {
            self.selection = 0..self.input.len();
            self.selection_anchor = 0;
            cx.notify();
            return;
        }

        if keystroke.modifiers.control {
            match keystroke.key.as_str() {
                "q" => unreachable!("Ctrl+Q is handled before overlays"),
                "g" => {
                    // Escalating to Goal is a Fast-only verb: a Goal or Step session
                    // refuses it, so offering the key there would be a key that only
                    // ever produces an error line.
                    if self.view.as_ref().map(|view| view.mode.as_str()) == Some(MODE_FAST) {
                        self.send(ClientMessage::escalate_to_goal());
                    }
                }
                "r" => self.send(ClientMessage::restart_agent(AGENT_AUSTIN)),
                "y" => self.send(ClientMessage::restart_agent(AGENT_TONY)),
                "o" => self.overview_open = !self.overview_open,
                "p" => self.work_preview_open = !self.work_preview_open,
                "m" => self.open_model_picker(if keystroke.modifiers.shift {
                    AGENT_TONY
                } else {
                    AGENT_AUSTIN
                }),
                "t" if keystroke.modifiers.alt => {
                    self.send(ClientMessage::cycle_thinking(AGENT_AUSTIN));
                }
                "t" if keystroke.modifiers.shift => {
                    self.send(ClientMessage::cycle_thinking(AGENT_TONY));
                }
                "t" => self.timestamps = !self.timestamps,
                "x" => self.copy_latest_austin(cx),
                "h" => self.toggle_help(),
                "l" => {
                    // Toggle, because the human cannot type the target language into a
                    // window that has no menu; the host records the choice for later
                    // sessions.
                    let next = match self.view.as_ref().map(|view| view.language.as_str()) {
                        Some("zh") => "en",
                        _ => "zh",
                    };
                    self.send(ClientMessage::set_language(next));
                }
                _ => return,
            }
            cx.notify();
            return;
        }

        match keystroke.key.as_str() {
            "f1" => self.toggle_help(),
            "enter" if self.marked_range.is_some() => {
                // Let the active IME confirm its candidate before a later Enter
                // submits the resulting task.
            }
            "enter" if keystroke.modifiers.shift => {
                self.reset_history_navigation();
                self.slash_dismissed = false;
                replace_selection(&mut self.input, &mut self.selection, "\n");
            }
            "up" if keystroke.modifiers.shift && self.marked_range.is_none() => {
                self.move_composer_visual_line(-1, true);
            }
            "down" if keystroke.modifiers.shift && self.marked_range.is_none() => {
                self.move_composer_visual_line(1, true);
            }
            "up" if self.marked_range.is_none() => self.vertical_or_history(true),
            "down" if self.marked_range.is_none() => self.vertical_or_history(false),
            "enter" => {
                if self.activate_exact_slash_command(cx) {
                    cx.notify();
                    return;
                }
                let text = self.input.trim().to_owned();
                if !text.is_empty() {
                    self.push_history(text.clone());
                    self.input.clear();
                    self.selection = 0..0;
                    self.history_index = None;
                    self.history_draft = None;
                    self.slash_dismissed = false;
                    self.send(ClientMessage::submit_task(text));
                }
            }
            "escape" => {
                self.reset_history_navigation();
                self.input.clear();
                self.selection = 0..0;
                self.marked_range = None;
                self.slash_dismissed = false;
            }
            "backspace" => {
                self.reset_history_navigation();
                self.slash_dismissed = false;
                self.slash_selected = 0;
                self.slash_selection_explicit = false;
                if self.selection.is_empty() && self.selection.start > 0 {
                    self.selection.start = previous_boundary(&self.input, self.selection.start);
                }
                replace_selection(&mut self.input, &mut self.selection, "");
            }
            "left" => move_cursor_with_selection(self, false, keystroke.modifiers.shift),
            "right" => move_cursor_with_selection(self, true, keystroke.modifiers.shift),
            "home" => move_to_line_edge(self, false, keystroke.modifiers.shift),
            "end" => move_to_line_edge(self, true, keystroke.modifiers.shift),
            // Text, including spaces and IME commits, arrives through GPUI's
            // InputHandler bridge. Handling key_char here would insert it twice.
            _ => {}
        }
        cx.notify();
    }

    fn toggle_help(&mut self) {
        self.help_open = !self.help_open;
        if self.help_open {
            self.help_query.clear();
            self.slash_dismissed = true;
        }
    }

    fn on_help_key(&mut self, event: &KeyDownEvent, cx: &mut Context<Self>) {
        let key = event.keystroke.key.as_str();
        if event.keystroke.modifiers.control && key.eq_ignore_ascii_case("h") || key == "f1" {
            self.help_open = false;
        } else {
            match key {
                "escape" => self.help_open = false,
                "backspace" | "delete" => {
                    self.help_query.pop();
                }
                "up" | "down" => {}
                _ if !event.keystroke.modifiers.control && !event.keystroke.modifiers.alt => {
                    if self.marked_range.is_none() {
                        if let Some(chars) = event.keystroke.key_char.as_deref() {
                            if !chars.is_empty() && !chars.chars().any(char::is_control) {
                                self.help_query.push_str(chars);
                            }
                        }
                    }
                }
                _ => {}
            }
        }
        cx.notify();
    }

    fn is_fast(&self) -> bool {
        self.view.as_ref().map(|view| view.mode.as_str()) == Some(MODE_FAST)
    }

    fn slash_matches(&self) -> Vec<&'static SlashCommand> {
        slash_completions(&self.input, self.is_fast())
    }

    fn slash_menu_open(&self) -> bool {
        !self.help_open
            && self.model_picker.is_none()
            && !self.slash_dismissed
            && !self.slash_matches().is_empty()
    }

    fn move_slash_selection(&mut self, delta: isize) {
        let count = self.slash_matches().len();
        if count == 0 {
            self.slash_selected = 0;
            self.slash_selection_explicit = false;
            return;
        }
        self.slash_selected =
            (self.slash_selected as isize + delta).rem_euclid(count as isize) as usize;
        self.slash_selection_explicit = true;
    }

    fn complete_slash_command(&mut self) {
        let matches = self.slash_matches();
        let Some(command) = matches.get(self.slash_selected.min(matches.len().saturating_sub(1)))
        else {
            return;
        };
        self.input = command.name.to_owned();
        self.selection = self.input.len()..self.input.len();
        self.marked_range = None;
        self.slash_dismissed = false;
        self.slash_selection_explicit = false;
        self.reset_history_navigation();
    }

    fn activate_exact_slash_command(&mut self, cx: &mut Context<Self>) -> bool {
        match classify_submission(&self.input, self.is_fast()) {
            Submission::Command { command, .. } => self.run_slash_action(command, cx),
            Submission::Task(_) => false,
        }
    }

    fn activate_slash_palette(&mut self, cx: &mut Context<Self>) -> bool {
        if self.activate_exact_slash_command(cx) {
            return true;
        }
        if !self.slash_selection_explicit {
            return false;
        }
        let matches = self.slash_matches();
        let Some(command) = matches.get(self.slash_selected.min(matches.len().saturating_sub(1)))
        else {
            return false;
        };
        self.run_slash_action(command, cx)
    }

    fn run_slash_action(&mut self, command: &'static SlashCommand, cx: &mut Context<Self>) -> bool {
        if command.action == SlashAction::EscalateToGoal && !self.is_fast() {
            return false;
        }
        if command.action == SlashAction::Quit {
            if self.request_quit(Instant::now()) {
                self.send(ClientMessage::quit());
                cx.quit();
            }
            return true;
        }

        self.input.clear();
        self.selection = 0..0;
        self.marked_range = None;
        self.reset_history_navigation();
        self.slash_dismissed = false;
        self.slash_selection_explicit = false;
        match command.action {
            SlashAction::EscalateToGoal => self.send(ClientMessage::escalate_to_goal()),
            SlashAction::RestartAustin => self.send(ClientMessage::restart_agent(AGENT_AUSTIN)),
            SlashAction::RestartTony => self.send(ClientMessage::restart_agent(AGENT_TONY)),
            SlashAction::ToggleOverview => self.overview_open = !self.overview_open,
            SlashAction::ToggleWorkPreview => self.work_preview_open = !self.work_preview_open,
            SlashAction::ToggleTimestamps => self.timestamps = !self.timestamps,
            SlashAction::ChooseAustinModel => self.open_model_picker(AGENT_AUSTIN),
            SlashAction::ChooseTonyModel => self.open_model_picker(AGENT_TONY),
            SlashAction::CycleAustinThinking => self.cycle_agent_thinking(AGENT_AUSTIN, cx),
            SlashAction::CycleTonyThinking => self.cycle_agent_thinking(AGENT_TONY, cx),
            SlashAction::ToggleLanguage => {
                let language = match self.view.as_ref().map(|view| view.language.as_str()) {
                    Some("zh") => "en",
                    _ => "zh",
                };
                self.send(ClientMessage::set_language(language));
            }
            SlashAction::CopyLatestAustin => self.copy_latest_austin(cx),
            SlashAction::ToggleHelp => self.toggle_help(),
            SlashAction::Quit => unreachable!(),
        }
        true
    }

    fn move_timeline_selection(&mut self, delta: isize) {
        self.selected_event =
            timeline_navigation_target(self.selected_event, self.events.len(), delta);
        if let Some(index) = self.selected_event {
            let notice_count = self.view.as_ref().map_or(0, |view| view.notices.len());
            self.timeline_scroll.scroll_to_item(notice_count + index);
        }
    }
}

impl DuoApp {
    fn open_model_picker(&mut self, agent: &str) {
        self.help_open = false;
        self.pending_model_change = None;
        let current = self
            .view
            .as_ref()
            .and_then(|view| view.agents.get(agent))
            .map(|agent| agent.model.as_str())
            .unwrap_or_default();
        let selected = self
            .view
            .as_ref()
            .and_then(|view| view.agents.get(agent))
            .map(|agent| {
                agent
                    .models
                    .iter()
                    .position(|model| model.reference == current || model.id == current)
                    .unwrap_or_default()
            })
            .unwrap_or_default();
        self.model_picker = Some(ModelPicker {
            agent: agent.to_owned(),
            filter: String::new(),
            selected,
        });
        self.banner = None;
    }

    fn on_model_picker_key(&mut self, event: &KeyDownEvent, cx: &mut Context<Self>) {
        let key = event.keystroke.key.as_str();
        if event.keystroke.modifiers.control && key == "q" {
            if self.request_quit(Instant::now()) {
                self.model_picker = None;
                self.send(ClientMessage::quit());
                cx.quit();
            } else {
                cx.notify();
            }
            return;
        }
        if event.keystroke.modifiers.control && key == "m" {
            self.model_picker = None;
            self.pending_model_change = None;
            cx.notify();
            return;
        }
        match key {
            "escape" if self.pending_model_change.is_some() => {
                self.pending_model_change = None;
            }
            "escape" => self.model_picker = None,
            "enter" if self.pending_model_change.is_some() => self.confirm_model_change(cx),
            "enter" => self.select_model(cx),
            "up" => self.move_model_selection(-1),
            "down" => self.move_model_selection(1),
            "tab" => {
                self.pending_model_change = None;
                if let Some(picker) = self.model_picker.as_mut() {
                    picker.agent = if picker.agent == AGENT_AUSTIN {
                        AGENT_TONY.to_owned()
                    } else {
                        AGENT_AUSTIN.to_owned()
                    };
                    let current_agent = picker.agent.clone();
                    let current = self
                        .view
                        .as_ref()
                        .and_then(|view| view.agents.get(&current_agent))
                        .map(|agent| agent.model.as_str())
                        .unwrap_or_default();
                    picker.selected = self
                        .view
                        .as_ref()
                        .and_then(|view| view.agents.get(&current_agent))
                        .and_then(|agent| {
                            matching_models(agent, &picker.filter)
                                .iter()
                                .position(|model| model.reference == current || model.id == current)
                        })
                        .unwrap_or_default();
                }
            }
            "backspace" | "delete" => {
                self.pending_model_change = None;
                if let Some(picker) = self.model_picker.as_mut() {
                    picker.filter.pop();
                    picker.selected = 0;
                }
            }
            _ if !event.keystroke.modifiers.control && !event.keystroke.modifiers.alt => {
                if let Some(chars) = event.keystroke.key_char.as_deref() {
                    if !chars.is_empty() && !chars.chars().any(char::is_control) {
                        self.pending_model_change = None;
                        if let Some(picker) = self.model_picker.as_mut() {
                            picker.filter.push_str(chars);
                            picker.selected = 0;
                        }
                    }
                }
            }
            _ => {}
        }
        cx.notify();
    }

    fn move_model_selection(&mut self, delta: isize) {
        let Some(picker) = self.model_picker.as_mut() else {
            return;
        };
        let previous = picker.selected;
        let count = self
            .view
            .as_ref()
            .and_then(|view| view.agents.get(&picker.agent))
            .map(|agent| matching_models(agent, &picker.filter).len())
            .unwrap_or_default();
        picker.selected = picker
            .selected
            .saturating_add_signed(delta)
            .min(count.saturating_sub(1));
        if picker.selected != previous {
            self.pending_model_change = None;
        }
    }

    fn select_model(&mut self, cx: &mut Context<Self>) {
        if self.pending_model_change.is_some() {
            self.confirm_model_change(cx);
            return;
        }
        let Some(picker) = self.model_picker.as_ref() else {
            return;
        };
        self.select_model_at(picker.selected, cx);
    }

    fn select_model_at(&mut self, index: usize, cx: &mut Context<Self>) {
        let Some(picker) = self.model_picker.as_ref() else {
            return;
        };
        let agent = picker.agent.clone();
        let choice = self
            .view
            .as_ref()
            .and_then(|view| view.agents.get(&agent))
            .map(|agent_view| {
                matching_models(agent_view, &picker.filter)
                    .into_iter()
                    .nth(index)
                    .map(|model| (agent_view.clone(), model))
            })
            .flatten();
        if let Some((agent_view, model)) = choice {
            match model_switch_decision(&agent_view, &model) {
                ModelSwitchDecision::AlreadyCurrent => {
                    self.pending_model_change = None;
                    self.banner = Some(format!("{agent} already uses {}", model.reference));
                    self.model_picker = None;
                }
                ModelSwitchDecision::Apply => {
                    self.apply_model_choice(agent, model, false);
                }
                ModelSwitchDecision::ConfirmRestart => {
                    self.pending_model_change = Some(PendingModelChange { agent, model });
                    self.banner = None;
                }
            }
        }
        cx.notify();
    }

    fn apply_model_choice(&mut self, agent: String, model: Model, restarts: bool) {
        self.send(model_selection_command(&agent, &model));
        self.banner = Some(if restarts {
            format!(
                "Requested {agent} model {}; {agent} will restart",
                model.reference
            )
        } else {
            format!("Requested {agent} model {}", model.reference)
        });
        self.pending_model_change = None;
        self.model_picker = None;
    }

    fn confirm_model_change(&mut self, cx: &mut Context<Self>) {
        let Some(pending) = self.pending_model_change.clone() else {
            return;
        };
        self.apply_model_choice(pending.agent, pending.model, true);
        cx.notify();
    }

    fn cycle_agent_thinking(&mut self, agent: &str, cx: &mut Context<Self>) {
        if self
            .view
            .as_ref()
            .and_then(|view| view.agents.get(agent))
            .is_some_and(|agent| agent.present)
        {
            self.send(ClientMessage::cycle_thinking(agent));
            self.banner = Some(format!("Requested {agent} thinking-level change"));
            cx.notify();
        }
    }

    fn needs_quit_confirmation(&self) -> bool {
        self.view
            .as_ref()
            .is_some_and(|view| needs_quit_confirmation(view.started, view.phase.as_str()))
    }

    fn request_quit(&mut self, now: Instant) -> bool {
        if !self.needs_quit_confirmation() {
            self.quit_deadline = None;
            return true;
        }
        if confirm_quit(&mut self.quit_deadline, now) {
            self.banner = None;
            return true;
        }
        self.banner = Some(
            "Session unfinished. Press Ctrl+Q or close again within 3 seconds to quit.".into(),
        );
        false
    }

    fn copy_latest_austin(&mut self, cx: &mut Context<Self>) {
        let latest = latest_austin_message(&self.events);
        if let Some(event) = latest {
            cx.write_to_clipboard(ClipboardItem::new_string(event.text.clone()));
            self.banner = Some("Copied Austin's latest message to the clipboard.".into());
        } else {
            self.banner = Some("Austin has no message to the human yet.".into());
        }
    }

    fn copy_selected_event(&mut self, cx: &mut Context<Self>) {
        if let Some(text) = selected_event_text(&self.events, self.selected_event) {
            cx.write_to_clipboard(ClipboardItem::new_string(text.to_owned()));
            self.banner = Some("Copied the selected message to the clipboard.".into());
        }
    }

    pub fn on_window_close(&mut self, cx: &mut Context<Self>) -> bool {
        if self.request_quit(Instant::now()) {
            self.send(ClientMessage::quit());
            true
        } else {
            cx.notify();
            false
        }
    }
}

const QUIT_CONFIRM_WINDOW: Duration = Duration::from_secs(3);

fn confirm_quit(deadline: &mut Option<Instant>, now: Instant) -> bool {
    if deadline.is_some_and(|until| now <= until) {
        *deadline = None;
        true
    } else {
        *deadline = Some(now + QUIT_CONFIRM_WINDOW);
        false
    }
}

fn needs_quit_confirmation(started: bool, phase: &str) -> bool {
    started && !phase.eq_ignore_ascii_case("DONE")
}

fn latest_austin_message(events: &[Event]) -> Option<&Event> {
    events.iter().rev().find(|event| {
        event.agent == AGENT_AUSTIN
            && event.kind == "assistant"
            && (event.label.is_empty() || event.label == "Austin → Human")
            && !event.text.trim().is_empty()
    })
}

fn selected_event_text(events: &[Event], selected: Option<usize>) -> Option<&str> {
    events.get(selected?).map(|event| event.text.as_str())
}

fn selection_after_front_drain(selected: Option<usize>, drained: usize) -> Option<usize> {
    selected.and_then(|index| index.checked_sub(drained))
}

fn timeline_navigation_target(
    selected: Option<usize>,
    event_count: usize,
    delta: isize,
) -> Option<usize> {
    if event_count == 0 {
        return None;
    }
    let target = match (selected, delta) {
        (_, isize::MIN) => 0,
        (_, isize::MAX) => event_count - 1,
        (None, delta) if delta < 0 => event_count - 1,
        (None, _) => 0,
        (Some(current), delta) if delta < 0 => current.saturating_sub(delta.unsigned_abs()),
        (Some(current), delta) => current.saturating_add(delta as usize).min(event_count - 1),
    };
    Some(target.min(event_count - 1))
}

fn resize_edge(position: Point<Pixels>, size: Size<Pixels>, inset: Pixels) -> Option<ResizeEdge> {
    let left = position.x <= inset;
    let right = position.x >= size.width - inset;
    let top = position.y <= inset;
    let bottom = position.y >= size.height - inset;
    match (top, right, bottom, left) {
        (true, _, _, true) => Some(ResizeEdge::TopLeft),
        (true, true, _, _) => Some(ResizeEdge::TopRight),
        (_, true, true, _) => Some(ResizeEdge::BottomRight),
        (_, _, true, true) => Some(ResizeEdge::BottomLeft),
        (true, _, _, _) => Some(ResizeEdge::Top),
        (_, true, _, _) => Some(ResizeEdge::Right),
        (_, _, true, _) => Some(ResizeEdge::Bottom),
        (_, _, _, true) => Some(ResizeEdge::Left),
        _ => None,
    }
}

fn resize_cursor_layer() -> impl IntoElement {
    canvas(
        |_bounds, window, _| {
            window.insert_hitbox(
                Bounds::new(
                    point(px(0.), px(0.)),
                    window.window_bounds().get_bounds().size,
                ),
                HitboxBehavior::Normal,
            )
        },
        |_bounds, hitbox, window, _| {
            let size = window.window_bounds().get_bounds().size;
            let Some(edge) = resize_edge(window.mouse_position(), size, px(6.)) else {
                return;
            };
            let cursor = match edge {
                ResizeEdge::Top | ResizeEdge::Bottom => CursorStyle::ResizeUpDown,
                ResizeEdge::Left | ResizeEdge::Right => CursorStyle::ResizeLeftRight,
                ResizeEdge::TopLeft | ResizeEdge::BottomRight => CursorStyle::ResizeUpLeftDownRight,
                ResizeEdge::TopRight | ResizeEdge::BottomLeft => CursorStyle::ResizeUpRightDownLeft,
            };
            window.set_cursor_style(cursor, &hitbox);
        },
    )
    .absolute()
    .size_full()
}

impl DuoApp {
    fn reset_history_navigation(&mut self) {
        self.history_index = None;
        self.history_draft = None;
    }

    fn push_history(&mut self, task: String) {
        if self.history.last() != Some(&task) {
            self.history.push(task);
            if self.history.len() > 100 {
                self.history.remove(0);
            }
            append_history(self.history.last().unwrap());
        }
        self.reset_history_navigation();
    }

    fn vertical_or_history(&mut self, older: bool) {
        if self.move_composer_line(if older { -1 } else { 1 }) {
            return;
        }
        self.history_move(older);
    }

    fn move_composer_line(&mut self, delta: isize) -> bool {
        let row = line_index_for_offset(&self.input, self.selection.end);
        let row_count = self.input.split('\n').count();
        let Some(target_row) = row.checked_add_signed(delta).filter(|row| *row < row_count) else {
            return false;
        };
        let (start, end) = line_range(&self.input, row);
        let column = self.input[start..self.selection.end.min(end)]
            .chars()
            .count();
        let (target_start, target_end) = line_range(&self.input, target_row);
        let target = self.input[target_start..target_end]
            .char_indices()
            .nth(column)
            .map(|(offset, _)| target_start + offset)
            .unwrap_or(target_end);
        self.selection = target..target;
        true
    }

    fn move_composer_visual_line(&mut self, delta: isize, extend: bool) -> bool {
        if self.last_layout.is_empty() {
            return false;
        }
        let active = if self.selection.is_empty() {
            self.selection.end
        } else if self.selection_anchor == self.selection.start {
            self.selection.end
        } else {
            self.selection.start
        };
        let Some(current) = visual_line_for_offset(&self.last_layout, active) else {
            return false;
        };
        let Some(target_row) = current
            .row
            .checked_add_signed(delta)
            .filter(|row| *row < self.last_layout.len())
        else {
            return false;
        };
        let target = &self.last_layout[target_row];
        let Some(bounds) = self.last_bounds else {
            return false;
        };
        let line_height = bounds.size.height / self.last_layout.len() as f32;
        let Some(current_position) = position_for_visual_index(
            &current.line,
            active.saturating_sub(current.offset),
            current.wrap_row,
            line_height,
        ) else {
            return false;
        };
        let local = target
            .line
            .closest_index_for_position(
                point(current_position.x, line_height * target.wrap_row as f32),
                line_height,
            )
            .unwrap_or_else(|index| index)
            .clamp(target.start - target.offset, target.end - target.offset);
        let cursor = target.offset + local;
        if extend {
            if self.selection.is_empty() {
                self.selection_anchor = active;
            }
            self.selection = self.selection_anchor.min(cursor)..self.selection_anchor.max(cursor);
        } else {
            self.selection = cursor..cursor;
            self.selection_anchor = cursor;
        }
        true
    }

    fn history_move(&mut self, older: bool) {
        if self.history.is_empty() {
            return;
        }
        if older && self.history_index.is_none() {
            self.history_draft = Some(self.input.clone());
        }
        let index = match (older, self.history_index) {
            (true, None) => self.history.len() - 1,
            (true, Some(0)) => 0,
            (true, Some(index)) => index - 1,
            (false, None) => return,
            (false, Some(index)) if index + 1 >= self.history.len() => {
                self.history_index = None;
                self.input = self.history_draft.take().unwrap_or_default();
                self.selection = self.input.len()..self.input.len();
                self.slash_dismissed = false;
                self.slash_selection_explicit = false;
                self.slash_selected = 0;
                return;
            }
            (false, Some(index)) => index + 1,
        };
        self.history_index = Some(index);
        self.input = self.history[index].clone();
        self.selection = self.input.len()..self.input.len();
        self.slash_dismissed = false;
        self.slash_selection_explicit = false;
    }

    fn mouse_index(&self, position: gpui::Point<Pixels>) -> usize {
        let Some(bounds) = self.last_bounds else {
            return 0;
        };
        let row_height = bounds.size.height / self.last_layout.len().max(1) as f32;
        let row = (((position.y - bounds.top()) / row_height).floor() as usize)
            .min(self.last_layout.len().saturating_sub(1));
        let Some(visual) = self.last_layout.get(row) else {
            return 0;
        };
        let local = visual
            .line
            .closest_index_for_position(
                gpui::point(
                    position.x - bounds.left(),
                    row_height * visual.wrap_row as f32,
                ),
                row_height,
            )
            .unwrap_or_else(|index| index)
            .clamp(visual.start - visual.offset, visual.end - visual.offset);
        (visual.offset + local).min(self.input.len())
    }

    fn mouse_down(&mut self, event: &MouseDownEvent, window: &mut Window, cx: &mut Context<Self>) {
        self.timeline_focused = false;
        self.selecting = true;
        let cursor = self.mouse_index(event.position);
        self.selection = if event.modifiers.shift {
            self.selection.start.min(cursor)..self.selection.end.max(cursor)
        } else {
            cursor..cursor
        };
        self.selection_anchor = if event.modifiers.shift {
            self.selection.start
        } else {
            cursor
        };
        window.focus(&self.focus);
        cx.notify();
    }

    fn mouse_move(&mut self, event: &MouseMoveEvent, _: &mut Window, cx: &mut Context<Self>) {
        if self.selecting {
            let cursor = self.mouse_index(event.position);
            let anchor = self.selection_anchor;
            self.selection = anchor.min(cursor)..anchor.max(cursor);
            cx.notify();
        }
    }

    fn mouse_up(&mut self, _: &MouseUpEvent, _: &mut Window, _: &mut Context<Self>) {
        self.selecting = false;
    }
}

struct ComposerElement {
    app: Entity<DuoApp>,
    input_enabled: bool,
}
struct ComposerPaint {
    lines: Vec<(usize, WrappedLine)>,
    visual_lines: Vec<ComposerVisualLine>,
    cursor: Option<PaintQuad>,
    selection: Vec<PaintQuad>,
}

#[derive(Clone)]
struct ComposerVisualLine {
    start: usize,
    end: usize,
    offset: usize,
    line: WrappedLine,
    line_index: usize,
    row: usize,
    wrap_row: usize,
}

fn shape_composer_lines(
    text: &str,
    window: &Window,
    wrap_width: Pixels,
) -> Vec<(usize, WrappedLine)> {
    let style = window.text_style();
    let font_size = style.font_size.to_pixels(window.rem_size());
    let mut shaped = Vec::new();
    let mut offset = 0;
    for part in text.split('\n') {
        let run = TextRun {
            len: part.len(),
            font: style.font(),
            color: if text == COMPOSER_PLACEHOLDER {
                style.color.opacity(0.45)
            } else {
                style.color
            },
            background_color: None,
            underline: None,
            strikethrough: None,
        };
        let lines = window
            .text_system()
            .shape_text(
                part.to_owned().into(),
                font_size,
                &[run],
                Some(wrap_width.max(px(1.))),
                None,
            )
            .expect("failed to shape composer text");
        for line in lines {
            shaped.push((offset, line));
        }
        offset += part.len() + 1;
    }
    shaped
}

fn composer_visual_lines(
    lines: &[(usize, WrappedLine)],
    line_height: Pixels,
) -> Vec<ComposerVisualLine> {
    let mut visual = Vec::new();
    let mut row = 0;
    for (line_index, (offset, line)) in lines.iter().enumerate() {
        let ranges = wrapped_byte_ranges(line, line_height);
        for (wrap_row, range) in ranges.into_iter().enumerate() {
            visual.push(ComposerVisualLine {
                start: offset + range.start,
                end: offset + range.end,
                offset: *offset,
                line: line.clone(),
                line_index,
                row,
                wrap_row,
            });
            row += 1;
        }
    }
    visual
}

fn wrapped_byte_ranges(line: &WrappedLine, line_height: Pixels) -> Vec<Range<usize>> {
    let row_count = line.wrap_boundaries().len() + 1;
    let mut starts = Vec::with_capacity(row_count);
    let mut ends = Vec::with_capacity(row_count);
    for row in 0..row_count {
        let y = line_height * row as f32;
        starts.push(
            line.closest_index_for_position(point(px(-1.), y), line_height)
                .unwrap_or_else(|index| index),
        );
        ends.push(
            line.closest_index_for_position(point(px(1_000_000.), y), line_height)
                .unwrap_or_else(|index| index),
        );
    }
    visual_ranges_from_boundaries(line.len(), starts, ends)
}

fn visual_line_for_offset(
    lines: &[ComposerVisualLine],
    offset: usize,
) -> Option<&ComposerVisualLine> {
    let ranges = lines
        .iter()
        .map(|line| line.start..line.end)
        .collect::<Vec<_>>();
    range_index_for_offset(&ranges, offset).and_then(|index| lines.get(index))
}

fn range_index_for_offset(ranges: &[Range<usize>], offset: usize) -> Option<usize> {
    ranges
        .iter()
        .position(|range| {
            offset >= range.start
                && (offset < range.end || (range.is_empty() && offset == range.start))
        })
        .or_else(|| {
            ranges
                .len()
                .checked_sub(1)
                .filter(|index| offset == ranges[*index].end)
        })
}

fn position_for_visual_index(
    line: &WrappedLine,
    index: usize,
    wrap_row: usize,
    line_height: Pixels,
) -> Option<Point<Pixels>> {
    let ranges = wrapped_byte_ranges(line, line_height);
    if wrap_row > 0
        && ranges
            .get(wrap_row)
            .is_some_and(|range| index == range.start)
    {
        Some(point(px(0.), line_height * wrap_row as f32))
    } else {
        line.position_for_index(index, line_height)
    }
}

fn visual_ranges_from_boundaries(
    _length: usize,
    starts: Vec<usize>,
    ends: Vec<usize>,
) -> Vec<Range<usize>> {
    starts
        .into_iter()
        .zip(ends)
        .map(|(start, end)| start.min(_length)..end.min(_length))
        .collect()
}

impl gpui::IntoElement for ComposerElement {
    type Element = Self;
    fn into_element(self) -> Self {
        self
    }
}

impl Element for ComposerElement {
    type RequestLayoutState = ();
    type PrepaintState = ComposerPaint;
    fn id(&self) -> Option<gpui::ElementId> {
        None
    }
    fn source_location(&self) -> Option<&'static core::panic::Location<'static>> {
        None
    }
    fn request_layout(
        &mut self,
        _: Option<&GlobalElementId>,
        _: Option<&gpui::InspectorElementId>,
        window: &mut Window,
        cx: &mut App,
    ) -> (LayoutId, ()) {
        let mut style = Style::default();
        style.size.width = relative(1.).into();
        let input = self.app.read(cx).input.clone();
        let text = if input.is_empty() {
            COMPOSER_PLACEHOLDER
        } else {
            input.as_str()
        };
        let wrap_width = (window.viewport_size().width - px(24.)).max(px(1.));
        let rows = shape_composer_lines(text, window, wrap_width)
            .iter()
            .map(|(_, line)| line.wrap_boundaries().len() + 1)
            .sum::<usize>()
            .max(1);
        style.size.height = (window.line_height() * rows as f32).into();
        (window.request_layout(style, [], cx), ())
    }
    fn prepaint(
        &mut self,
        _: Option<&GlobalElementId>,
        _: Option<&gpui::InspectorElementId>,
        bounds: Bounds<Pixels>,
        _: &mut (),
        window: &mut Window,
        cx: &mut App,
    ) -> ComposerPaint {
        let app = self.app.read(cx);
        let empty = app.input.is_empty();
        let display_text = if empty {
            COMPOSER_PLACEHOLDER
        } else {
            app.input.as_str()
        };
        let line_height = window.line_height();
        let lines = shape_composer_lines(display_text, window, bounds.size.width);
        let visual_lines = composer_visual_lines(&lines, line_height);
        let mut selection = Vec::new();
        if !empty && !app.selection.is_empty() {
            for visual in &visual_lines {
                let selected_start = app.selection.start.max(visual.start).min(visual.end);
                let selected_end = app.selection.end.max(visual.start).min(visual.end);
                if selected_start < selected_end {
                    let (offset, line) = &lines[visual.line_index];
                    let local_start = selected_start - offset;
                    let local_end = selected_end - offset;
                    let start_position =
                        position_for_visual_index(line, local_start, visual.wrap_row, line_height)
                            .unwrap();
                    let end_position =
                        position_for_visual_index(line, local_end, visual.wrap_row, line_height)
                            .unwrap();
                    selection.push(fill(
                        Bounds::from_corners(
                            point(
                                bounds.left() + start_position.x,
                                bounds.top()
                                    + line_height * (visual.row - visual.wrap_row) as f32
                                    + start_position.y,
                            ),
                            point(
                                bounds.left() + end_position.x,
                                bounds.top()
                                    + line_height * (visual.row - visual.wrap_row) as f32
                                    + end_position.y
                                    + line_height,
                            ),
                        ),
                        rgba(0x665b8cff),
                    ));
                }
            }
        }
        let cursor = if app.selection.is_empty() {
            let index = if empty { 0 } else { app.selection.end };
            let visual = visual_line_for_offset(&visual_lines, index)
                .unwrap_or_else(|| visual_lines.last().unwrap());
            let (offset, line) = &lines[visual.line_index];
            let position = position_for_visual_index(
                line,
                index.saturating_sub(*offset),
                visual.wrap_row,
                line_height,
            )
            .unwrap();
            Some(fill(
                Bounds::new(
                    point(
                        bounds.left() + position.x,
                        bounds.top()
                            + line_height * (visual.row - visual.wrap_row) as f32
                            + position.y,
                    ),
                    size(px(1.5), line_height),
                ),
                theme_caret(),
            ))
        } else {
            None
        };
        ComposerPaint {
            lines,
            visual_lines,
            cursor,
            selection,
        }
    }
    fn paint(
        &mut self,
        _: Option<&GlobalElementId>,
        _: Option<&gpui::InspectorElementId>,
        bounds: Bounds<Pixels>,
        _: &mut (),
        state: &mut ComposerPaint,
        window: &mut Window,
        cx: &mut App,
    ) {
        let focus = self.app.read(cx).focus.clone();
        if self.input_enabled {
            window.handle_input(
                &focus,
                ElementInputHandler::new(bounds, self.app.clone()),
                cx,
            );
        }
        for selection in state.selection.drain(..) {
            window.paint_quad(selection);
        }
        let mut row = 0;
        for (_, line) in &state.lines {
            line.paint(
                point(
                    bounds.left(),
                    bounds.top() + window.line_height() * row as f32,
                ),
                window.line_height(),
                gpui::TextAlign::Left,
                Some(bounds),
                window,
                cx,
            )
            .ok();
            row += line.wrap_boundaries().len() + 1;
        }
        if self.input_enabled && focus.is_focused(window) {
            if let Some(cursor) = state.cursor.take() {
                window.paint_quad(cursor);
            }
        }
        self.app.update(cx, |app, _| {
            app.last_layout = state.visual_lines.clone();
            app.last_bounds = Some(bounds);
        });
    }
}

fn theme_caret() -> Rgba {
    rgb(0x6fc7d8)
}

fn line_range(text: &str, row: usize) -> (usize, usize) {
    let mut start = 0;
    for (index, line) in text.split('\n').enumerate() {
        let end = start + line.len();
        if index == row {
            return (start, end);
        }
        start = end + 1;
    }
    (text.len(), text.len())
}

fn line_index_for_offset(text: &str, offset: usize) -> usize {
    text[..offset.min(text.len())]
        .bytes()
        .filter(|byte| *byte == b'\n')
        .count()
}

fn history_path() -> Option<PathBuf> {
    if let Some(path) = std::env::var_os("DUO_HISTORY_FILE") {
        return Some(PathBuf::from(path));
    }
    let home = std::env::var_os("HOME").or_else(|| std::env::var_os("USERPROFILE"))?;
    Some(PathBuf::from(home).join(".duo").join("history"))
}

fn load_history() -> Vec<String> {
    history_path()
        .and_then(|path| std::fs::read_to_string(path).ok())
        .map(|contents| decode_history(&contents))
        .unwrap_or_default()
}

fn decode_history(contents: &str) -> Vec<String> {
    let mut history: Vec<String> = contents
        .lines()
        .map(|line| line.trim_end_matches('\r').replace("\\n", "\n"))
        .filter(|line| !line.is_empty())
        .collect();
    if history.len() > 100 {
        history.drain(..history.len() - 100);
    }
    history
}

fn append_history(task: &str) {
    let Some(path) = history_path() else {
        return;
    };
    let Some(parent) = path.parent() else {
        return;
    };
    if std::fs::create_dir_all(parent).is_err() {
        return;
    }
    let mut options = std::fs::OpenOptions::new();
    options.create(true).append(true);
    #[cfg(unix)]
    {
        use std::os::unix::fs::OpenOptionsExt as _;
        options.mode(0o600);
    }
    let escaped = encode_history_entry(task);
    if let Ok(mut file) = options.open(path) {
        let _ = file.write_all(escaped.as_bytes());
    }
}

fn encode_history_entry(task: &str) -> String {
    task.replace('\n', "\\n") + "\n"
}

impl Focusable for DuoApp {
    fn focus_handle(&self, _: &gpui::App) -> FocusHandle {
        self.focus.clone()
    }
}

impl Render for DuoApp {
    fn render(&mut self, window: &mut Window, cx: &mut Context<Self>) -> impl IntoElement {
        let theme = Theme::from_appearance(window.appearance());
        let client_chrome = matches!(window.window_decorations(), Decorations::Client { .. });
        let maximized = window.is_maximized();
        let mut root = div()
            .relative()
            .size_full()
            .bg(theme.background)
            .text_color(theme.foreground)
            .font_family("monospace")
            .track_focus(&self.focus)
            .when(client_chrome, |element| {
                element
                    .border_1()
                    .border_color(theme.border)
                    .on_mouse_down(
                        MouseButton::Left,
                        cx.listener(|_, event: &MouseDownEvent, window, cx| {
                            let size = window.window_bounds().get_bounds().size;
                            if let Some(edge) = resize_edge(event.position, size, px(6.)) {
                                window.start_window_resize(edge);
                                cx.stop_propagation();
                            }
                        }),
                    )
                    .on_mouse_move(|_, window, _| window.refresh())
            });
        if client_chrome {
            root = root.child(resize_cursor_layer());
        }
        root.child(
            div()
                .flex()
                .flex_col()
                .size_full()
                .on_key_down(
                    cx.listener(|this, event: &KeyDownEvent, _window, cx| this.on_key(event, cx)),
                )
                .children(client_chrome.then(|| self.client_titlebar(theme, maximized, cx)))
                .child(self.header(theme, !client_chrome))
                .child(self.agents_row(theme, cx))
                .children(self.overview_open.then(|| self.overview(theme)))
                .children(self.help_open.then(|| self.help_overlay(theme)))
                .children(self.slash_menu_open().then(|| self.slash_palette(theme)))
                .children(
                    self.model_picker
                        .as_ref()
                        .map(|picker| self.model_picker_overlay(theme, picker, cx)),
                )
                .child(self.timeline(theme, cx))
                .child(self.status_row(theme))
                .child(self.input_row(theme, cx)),
        )
    }
}

impl EntityInputHandler for DuoApp {
    fn text_for_range(
        &mut self,
        range: Range<usize>,
        adjusted: &mut Option<Range<usize>>,
        _: &mut Window,
        _: &mut Context<Self>,
    ) -> Option<String> {
        let range = utf8_range(&self.input, range);
        *adjusted = Some(utf16_range(&self.input, range.clone()));
        Some(self.input[range].to_owned())
    }
    fn selected_text_range(
        &mut self,
        _: bool,
        _: &mut Window,
        _: &mut Context<Self>,
    ) -> Option<UTF16Selection> {
        Some(UTF16Selection {
            range: utf16_range(&self.input, self.selection.clone()),
            reversed: false,
        })
    }
    fn marked_text_range(&self, _: &mut Window, _: &mut Context<Self>) -> Option<Range<usize>> {
        self.marked_range
            .clone()
            .map(|range| utf16_range(&self.input, range))
    }
    fn unmark_text(&mut self, _: &mut Window, cx: &mut Context<Self>) {
        self.marked_range = None;
        cx.notify();
    }
    fn replace_text_in_range(
        &mut self,
        range: Option<Range<usize>>,
        text: &str,
        _: &mut Window,
        cx: &mut Context<Self>,
    ) {
        self.reset_history_navigation();
        let target = range
            .map(|range| utf8_range(&self.input, range))
            .or_else(|| self.marked_range.clone())
            .unwrap_or_else(|| self.selection.clone());
        self.input.replace_range(target.clone(), text);
        let end = target.start + text.len();
        self.selection = end..end;
        self.marked_range = None;
        reset_slash_palette_after_text_edit(
            &mut self.slash_dismissed,
            &mut self.slash_selected,
            &mut self.slash_selection_explicit,
        );
        cx.notify();
    }
    fn replace_and_mark_text_in_range(
        &mut self,
        range: Option<Range<usize>>,
        text: &str,
        selected: Option<Range<usize>>,
        _: &mut Window,
        cx: &mut Context<Self>,
    ) {
        self.reset_history_navigation();
        let target = range
            .map(|range| utf8_range(&self.input, range))
            .or_else(|| self.marked_range.clone())
            .unwrap_or_else(|| self.selection.clone());
        let start = target.start;
        self.input.replace_range(target, text);
        self.marked_range = (!text.is_empty()).then_some(start..start + text.len());
        self.selection = selected
            .map(|range| {
                let range = utf8_range(text, range);
                start + range.start..start + range.end
            })
            .unwrap_or(start + text.len()..start + text.len());
        reset_slash_palette_after_text_edit(
            &mut self.slash_dismissed,
            &mut self.slash_selected,
            &mut self.slash_selection_explicit,
        );
        cx.notify();
    }
    fn bounds_for_range(
        &mut self,
        range: Range<usize>,
        bounds: Bounds<gpui::Pixels>,
        _: &mut Window,
        _: &mut Context<Self>,
    ) -> Option<Bounds<gpui::Pixels>> {
        let range = utf8_range(&self.input, range);
        let line_height = bounds.size.height / self.last_layout.len().max(1) as f32;
        let visual = visual_line_for_offset(&self.last_layout, range.start)?;
        let start = range.start.clamp(visual.start, visual.end) - visual.offset;
        let end = range.end.clamp(visual.start, visual.end) - visual.offset;
        let start_position =
            position_for_visual_index(&visual.line, start, visual.wrap_row, line_height)?;
        let end_position =
            position_for_visual_index(&visual.line, end, visual.wrap_row, line_height)?;
        let paragraph_y = bounds.top() + line_height * (visual.row - visual.wrap_row) as f32;
        Some(Bounds::from_corners(
            point(
                bounds.left() + start_position.x,
                paragraph_y + start_position.y,
            ),
            point(
                bounds.left() + end_position.x,
                paragraph_y + end_position.y + line_height,
            ),
        ))
    }
    fn character_index_for_point(
        &mut self,
        point: gpui::Point<gpui::Pixels>,
        _: &mut Window,
        _: &mut Context<Self>,
    ) -> Option<usize> {
        let bounds = self.last_bounds?;
        let line_height = bounds.size.height / self.last_layout.len().max(1) as f32;
        let row = (((point.y - bounds.top()) / line_height).floor() as usize)
            .min(self.last_layout.len().saturating_sub(1));
        let visual = self.last_layout.get(row)?;
        let local = visual
            .line
            .closest_index_for_position(
                gpui::point(
                    point.x - bounds.left(),
                    line_height * visual.wrap_row as f32,
                ),
                line_height,
            )
            .unwrap_or_else(|index| index)
            .clamp(visual.start - visual.offset, visual.end - visual.offset);
        let index = (visual.offset + local).min(self.input.len());
        Some(utf16_range(&self.input, index..index).start)
    }
}

fn utf8_range(text: &str, range: Range<usize>) -> Range<usize> {
    let offset = |units| {
        let mut count = 0;
        for (byte, ch) in text.char_indices() {
            if count >= units {
                return byte;
            }
            count += ch.len_utf16();
        }
        text.len()
    };
    offset(range.start)..offset(range.end)
}

fn utf16_range(text: &str, range: Range<usize>) -> Range<usize> {
    let units = |byte: usize| text[..byte.min(text.len())].encode_utf16().count();
    units(range.start)..units(range.end)
}

fn replace_selection(text: &mut String, selection: &mut Range<usize>, replacement: &str) {
    text.replace_range(selection.clone(), replacement);
    let cursor = selection.start + replacement.len();
    *selection = cursor..cursor;
}

fn previous_boundary(text: &str, offset: usize) -> usize {
    text[..offset]
        .char_indices()
        .last()
        .map(|(index, _)| index)
        .unwrap_or(0)
}

fn move_cursor_with_selection(app: &mut DuoApp, right: bool, extend: bool) {
    let active = if app.selection.is_empty() {
        app.selection.end
    } else if app.selection_anchor == app.selection.start {
        app.selection.end
    } else {
        app.selection.start
    };
    let cursor = if right {
        app.input[active..]
            .chars()
            .next()
            .map(|ch| active + ch.len_utf8())
            .unwrap_or(active)
    } else {
        previous_boundary(&app.input, active)
    };
    if extend {
        if app.selection.is_empty() {
            app.selection_anchor = app.selection.start;
        }
        app.selection = app.selection_anchor.min(cursor)..app.selection_anchor.max(cursor);
    } else {
        app.selection = cursor..cursor;
        app.selection_anchor = cursor;
    }
}

fn move_to_line_edge(app: &mut DuoApp, end: bool, extend: bool) {
    let cursor = if app.selection.is_empty() || app.selection_anchor == app.selection.start {
        app.selection.end
    } else {
        app.selection.start
    };
    let target = visual_line_for_offset(&app.last_layout, cursor)
        .map(|visual| if end { visual.end } else { visual.start })
        .unwrap_or_else(|| {
            let (start, finish) = line_range(&app.input, line_index_for_offset(&app.input, cursor));
            if end {
                finish
            } else {
                start
            }
        });
    if extend {
        if app.selection.is_empty() {
            app.selection_anchor = cursor;
        }
        app.selection = app.selection_anchor.min(target)..app.selection_anchor.max(target);
    } else {
        app.selection = target..target;
        app.selection_anchor = target;
    }
}

// Rendering helpers. Each returns AnyElement so a row can be built from mixed
// children without making the element type of `render` depend on its contents.

impl DuoApp {
    fn overview(&self, theme: Theme) -> AnyElement {
        let rows = self
            .view
            .as_ref()
            .map(overview_rows)
            .unwrap_or_else(|| vec!["Waiting for Duo…".to_owned()]);
        div()
            .id("xduo-overview")
            .flex_none()
            .max_h(px(240.))
            .overflow_y_scroll()
            .px_3()
            .py_2()
            .border_b_1()
            .border_color(theme.border)
            .bg(theme.surface)
            .child(
                div()
                    .font_weight(FontWeight::BOLD)
                    .child("Session overview"),
            )
            .child(div().flex().flex_col().gap_1().text_sm().children(rows))
            .into_any_element()
    }

    fn client_titlebar(&self, theme: Theme, maximized: bool, cx: &Context<Self>) -> AnyElement {
        div()
            .flex_none()
            .flex()
            .flex_row()
            .items_center()
            .h(px(36.))
            .gap_2()
            .px_2()
            .border_b_1()
            .border_color(theme.border)
            .bg(theme.surface)
            .child(
                div()
                    .w(px(24.))
                    .h(px(24.))
                    .rounded_md()
                    .bg(rgb(0x182f93))
                    .text_color(rgb(0xffffff))
                    .font_weight(FontWeight::BOLD)
                    .flex()
                    .items_center()
                    .justify_center()
                    .child("d²"),
            )
            .child(
                div()
                    .flex_1()
                    .h_full()
                    .flex()
                    .items_center()
                    .text_color(theme.foreground)
                    .text_sm()
                    .child("xduo")
                    .on_mouse_down(MouseButton::Left, |event, window, cx| {
                        // Preserve the top resize strip along the border.
                        if event.position.y > px(6.) {
                            window.start_window_move();
                            cx.stop_propagation();
                        }
                    }),
            )
            .child(
                div()
                    .id("xduo-minimize")
                    .w(px(36.))
                    .h(px(28.))
                    .flex()
                    .items_center()
                    .justify_center()
                    .text_color(theme.foreground)
                    .child("—")
                    .on_mouse_down(MouseButton::Left, |_, _, cx| cx.stop_propagation())
                    .on_click(|_, window, _| window.minimize_window()),
            )
            .child(
                div()
                    .id("xduo-maximize")
                    .w(px(36.))
                    .h(px(28.))
                    .flex()
                    .items_center()
                    .justify_center()
                    .text_color(theme.foreground)
                    .child(if maximized { "❐" } else { "□" })
                    .on_mouse_down(MouseButton::Left, |_, _, cx| cx.stop_propagation())
                    .on_click(|_, window, _| window.zoom_window()),
            )
            .child(
                div()
                    .id("xduo-close")
                    .w(px(40.))
                    .h(px(28.))
                    .rounded_sm()
                    .flex()
                    .items_center()
                    .justify_center()
                    .text_color(theme.foreground)
                    .child("×")
                    .on_mouse_down(MouseButton::Left, |_, _, cx| cx.stop_propagation())
                    .on_click(cx.listener(|this, _, window, cx| {
                        if this.on_window_close(cx) {
                            window.remove_window();
                            cx.quit();
                        }
                    })),
            )
            .into_any_element()
    }

    fn header(&self, theme: Theme, show_mark: bool) -> AnyElement {
        let mut children: Vec<AnyElement> = Vec::new();

        let version = self
            .view
            .as_ref()
            .map(|view| view.version.clone())
            .unwrap_or_default();
        if show_mark {
            children.push(
                div()
                    .w(px(24.))
                    .h(px(24.))
                    .flex_none()
                    .rounded_md()
                    .bg(rgb(0x182f93))
                    .text_color(rgb(0xffffff))
                    .font_weight(FontWeight::BOLD)
                    .flex()
                    .items_center()
                    .justify_center()
                    .child("d²")
                    .into_any_element(),
            );
        }
        children.push(
            div()
                .font_weight(FontWeight::BOLD)
                .child(format!("xduo · Duo {version}"))
                .into_any_element(),
        );

        if let Some(view) = self.view.as_ref() {
            let mode = view.mode.to_uppercase();
            children.push(
                div()
                    .px_2()
                    .py_1()
                    .rounded_md()
                    .bg(theme.mode(&mode))
                    .text_color(if theme.light {
                        rgb(0x202735)
                    } else {
                        rgb(0x0f1116)
                    })
                    .font_weight(FontWeight::BOLD)
                    .child(mode)
                    .into_any_element(),
            );
            children.push(
                div()
                    .text_color(theme.muted)
                    .child(view.phase.clone())
                    .into_any_element(),
            );
            children.push(
                div()
                    .text_color(theme.faint)
                    .child(format!(
                        "{} · {} @ {}",
                        view.session, view.repository, view.base_branch
                    ))
                    .into_any_element(),
            );
        }

        if self.closed {
            children.push(
                div()
                    .text_color(theme.error)
                    .child("closed")
                    .into_any_element(),
            );
        }

        div()
            .flex()
            .flex_none()
            .flex_row()
            .items_center()
            .gap_3()
            .px_3()
            .py_2()
            .border_b_1()
            .border_color(theme.border)
            .children(children)
            .into_any_element()
    }

    fn agents_row(&self, theme: Theme, cx: &Context<Self>) -> AnyElement {
        div()
            .flex()
            .flex_none()
            .flex_row()
            .gap_3()
            .px_3()
            .py_2()
            .child(self.agent_card(AGENT_AUSTIN, "Austin", theme, cx))
            .child(self.agent_card(AGENT_TONY, "Tony", theme, cx))
            .into_any_element()
    }

    fn agent_card(&self, id: &str, title: &str, theme: Theme, cx: &Context<Self>) -> AnyElement {
        let agent = self.view.as_ref().and_then(|view| view.agents.get(id));
        let color = theme.speaker(id);

        let (detail, activity, state) = match agent {
            Some(agent) => (
                format!(
                    "{} · {} · {}",
                    agent.driver,
                    or_dash(&agent.model),
                    agent.process
                ),
                if agent.activity.is_empty() {
                    String::new()
                } else {
                    agent.activity.clone()
                },
                if agent.busy {
                    "● busy".to_owned()
                } else if agent.connected {
                    "● online".to_owned()
                } else if agent.present {
                    "○ idle".to_owned()
                } else {
                    "○ not configured".to_owned()
                },
            ),
            None => (
                "waiting for the session".to_owned(),
                String::new(),
                "○ unknown".to_owned(),
            ),
        };

        let mut rows: Vec<AnyElement> = vec![
            div()
                .flex()
                .flex_row()
                .items_center()
                .gap_2()
                .child(
                    div()
                        .text_color(color)
                        .font_weight(FontWeight::BOLD)
                        .child(title.to_owned()),
                )
                .child(div().text_color(theme.muted).text_sm().child(state))
                .into_any_element(),
            div()
                .text_color(theme.muted)
                .text_sm()
                .child(detail)
                .into_any_element(),
        ];
        let model_label = agent
            .map(|agent| or_dash(&agent.model))
            .unwrap_or("—")
            .to_owned();
        let thinking_label = agent
            .map(|agent| or_dash(&agent.thinking))
            .unwrap_or("—")
            .to_owned();
        let model_agent = id.to_owned();
        let thinking_agent = id.to_owned();
        let mut controls = div().flex().flex_row().items_center().gap_2();
        controls = controls.child(
            div()
                .px_2()
                .py_1()
                .rounded_md()
                .bg(theme.background)
                .cursor(CursorStyle::PointingHand)
                .text_sm()
                .child(format!("Model · {model_label}"))
                .on_mouse_down(
                    MouseButton::Left,
                    cx.listener(move |this, _: &MouseDownEvent, window, cx| {
                        window.focus(&this.focus);
                        this.open_model_picker(&model_agent);
                        cx.notify();
                    }),
                ),
        );
        let thinking_control = div()
            .px_2()
            .py_1()
            .rounded_md()
            .bg(theme.background)
            .text_sm()
            .child(format!("Thinking · {thinking_label}"));
        let thinking_control = if agent.is_some_and(|agent| agent.present) {
            thinking_control
                .cursor(CursorStyle::PointingHand)
                .on_mouse_down(
                    MouseButton::Left,
                    cx.listener(move |this, _: &MouseDownEvent, _, cx| {
                        this.cycle_agent_thinking(&thinking_agent, cx);
                    }),
                )
        } else {
            thinking_control.text_color(theme.faint)
        };
        controls = controls.child(thinking_control);
        rows.push(controls.into_any_element());
        if !activity.is_empty() {
            rows.push(
                div()
                    .text_color(theme.secondary)
                    .text_sm()
                    .truncate()
                    .child(activity)
                    .into_any_element(),
            );
        }
        if self.work_preview_open {
            if let Some(agent) = agent {
                rows.extend(agent_work_rows(agent, theme));
            }
        }

        div()
            .flex_1()
            .min_w_0()
            .flex()
            .flex_col()
            .gap_1()
            .px_3()
            .py_2()
            .bg(theme.surface)
            .rounded_md()
            .border_1()
            .border_color(theme.border)
            .children(rows)
            .into_any_element()
    }

    fn timeline(&self, theme: Theme, cx: &Context<Self>) -> AnyElement {
        let mut children: Vec<AnyElement> = Vec::new();

        // Notices are state, not history: they come from the last view, so they are
        // drawn every frame rather than appended once.
        if let Some(view) = self.view.as_ref() {
            for notice in &view.notices {
                children.push(
                    div()
                        .flex_none()
                        .flex()
                        .flex_row()
                        .gap_2()
                        .child(
                            div()
                                .w(px(130.))
                                .flex_none()
                                .text_sm()
                                .text_color(theme.warning)
                                .child("notice"),
                        )
                        .child(
                            div()
                                .flex_1()
                                .min_w_0()
                                .text_sm()
                                .text_color(theme.warning)
                                .child(notice.clone()),
                        )
                        .into_any_element(),
                );
            }
        }

        let notice_count = children.len();
        for (index, event) in self.events.iter().enumerate() {
            children.push(self.timeline_row(index, event, theme, notice_count, cx));
        }

        if children.is_empty() {
            children.push(
                div()
                    .text_color(theme.faint)
                    .child("Waiting for Duo…")
                    .into_any_element(),
            );
        }

        div()
            .flex_1()
            .min_h_0()
            .flex()
            .flex_col()
            .gap_1()
            .px_3()
            .py_2()
            .id("duo-timeline")
            .overflow_y_scroll()
            .track_scroll(&self.timeline_scroll)
            .children(children)
            .into_any_element()
    }

    fn timeline_row(
        &self,
        index: usize,
        event: &Event,
        theme: Theme,
        notice_count: usize,
        cx: &Context<Self>,
    ) -> AnyElement {
        let (speaker, color) = event_speaker(event, theme);
        let mut header = if event.label.is_empty() {
            speaker
        } else {
            event.label.clone()
        };
        if self.timestamps {
            if let Some(time) = event.time.split_once('T').map(|(_, time)| time) {
                let time = time.trim_end_matches('Z').split('.').next().unwrap_or(time);
                header.push_str(" · ");
                header.push_str(time);
            }
        }
        let body = markdown_elements(&parse_markdown(&event.text), theme);
        let body = if body.is_empty() {
            div().text_sm().child(" ").into_any_element()
        } else {
            div()
                .flex()
                .flex_col()
                .gap_1()
                .text_sm()
                .children(body)
                .into_any_element()
        };
        let row = div()
            .flex_none()
            .flex()
            .flex_row()
            .items_start()
            .gap_2()
            .when(self.selected_event == Some(index), |element| {
                element.bg(theme.background).rounded_sm()
            })
            .child(
                div()
                    .w(px(130.))
                    .flex_none()
                    .text_sm()
                    .truncate()
                    .text_color(color)
                    .child(header),
            )
            .child(div().flex_1().min_w_0().child(body))
            .cursor(CursorStyle::PointingHand)
            .on_mouse_down(
                MouseButton::Left,
                cx.listener(move |this, _, window, cx| {
                    this.timeline_focused = true;
                    this.selected_event = Some(index);
                    this.timeline_scroll.scroll_to_item(notice_count + index);
                    window.focus(&this.focus);
                    cx.notify();
                }),
            );
        row.into_any_element()
    }

    fn model_picker_overlay(
        &self,
        theme: Theme,
        picker: &ModelPicker,
        cx: &Context<Self>,
    ) -> AnyElement {
        let agent = self
            .view
            .as_ref()
            .and_then(|view| view.agents.get(&picker.agent));
        let state = catalog_state(agent, &picker.filter);
        let models = agent
            .map(|agent| matching_models(agent, &picker.filter))
            .unwrap_or_default();
        let selected = picker.selected.min(models.len().saturating_sub(1));
        let mut rows = Vec::new();
        match state {
            CatalogState::Loading => rows.push(
                div()
                    .text_color(theme.muted)
                    .child("Loading model catalog…")
                    .into_any_element(),
            ),
            CatalogState::Error => rows.push(
                div()
                    .text_color(theme.error)
                    .child(format!(
                        "Catalog unavailable: {}",
                        agent
                            .map(|agent| agent.models_error.as_str())
                            .unwrap_or("unknown error")
                    ))
                    .into_any_element(),
            ),
            CatalogState::Empty => rows.push(
                div()
                    .text_color(theme.muted)
                    .child("This driver returned no model choices.")
                    .into_any_element(),
            ),
            CatalogState::NoMatches => rows.push(
                div()
                    .text_color(theme.muted)
                    .child("No models match this filter.")
                    .into_any_element(),
            ),
            CatalogState::Ready => {
                for (index, model) in models.iter().enumerate() {
                    let label = format!("{}  ·  {}/{}", model.reference, model.provider, model.id);
                    let features = [
                        model.thinking.then_some("thinking"),
                        model.images.then_some("images"),
                    ]
                    .into_iter()
                    .flatten()
                    .collect::<Vec<_>>()
                    .join(" · ");
                    let agent_id = picker.agent.clone();
                    rows.push(
                        div()
                            .flex()
                            .flex_row()
                            .items_center()
                            .justify_between()
                            .gap_2()
                            .px_2()
                            .py_1()
                            .rounded_md()
                            .bg(if index == selected {
                                theme.mode("FAST")
                            } else {
                                theme.surface
                            })
                            .text_color(if index == selected && !theme.light {
                                rgb(0x0f1116)
                            } else {
                                theme.foreground
                            })
                            .cursor(CursorStyle::PointingHand)
                            .child(div().flex_1().min_w_0().truncate().child(label))
                            .child(
                                div()
                                    .text_xs()
                                    .text_color(if index == selected {
                                        theme.secondary
                                    } else {
                                        theme.muted
                                    })
                                    .child(features),
                            )
                            .on_mouse_down(
                                MouseButton::Left,
                                cx.listener(move |this, _: &MouseDownEvent, _, cx| {
                                    if let Some(picker) = this.model_picker.as_mut() {
                                        picker.agent = agent_id.clone();
                                        picker.selected = index;
                                    }
                                    this.select_model_at(index, cx);
                                }),
                            )
                            .into_any_element(),
                    );
                }
            }
        }
        let current = agent.map(|agent| or_dash(&agent.model)).unwrap_or("—");
        let pending_confirmation = self
            .pending_model_change
            .as_ref()
            .filter(|pending| pending.agent == picker.agent)
            .map(|pending| {
                div()
                    .flex()
                    .flex_col()
                    .gap_1()
                    .child(
                        div()
                            .px_2()
                            .py_2()
                            .rounded_md()
                            .bg(theme.background)
                            .text_sm()
                            .text_color(theme.warning)
                            .child(model_restart_warning(&pending.agent, &pending.model))
                            .into_any_element(),
                    )
                    .child(
                        div()
                            .flex()
                            .flex_row()
                            .gap_2()
                            .child(
                                div()
                                    .px_2()
                                    .py_1()
                                    .rounded_md()
                                    .bg(theme.warning)
                                    .text_sm()
                                    .text_color(if theme.light {
                                        rgb(0xffffff)
                                    } else {
                                        rgb(0x16120a)
                                    })
                                    .cursor(CursorStyle::PointingHand)
                                    .child("Confirm restart · Enter")
                                    .on_mouse_down(
                                        MouseButton::Left,
                                        cx.listener(|this, _: &MouseDownEvent, _, cx| {
                                            this.confirm_model_change(cx);
                                        }),
                                    )
                                    .into_any_element(),
                            )
                            .child(
                                div()
                                    .px_2()
                                    .py_1()
                                    .rounded_md()
                                    .bg(theme.background)
                                    .text_sm()
                                    .text_color(theme.muted)
                                    .cursor(CursorStyle::PointingHand)
                                    .child("Cancel · Esc")
                                    .on_mouse_down(
                                        MouseButton::Left,
                                        cx.listener(|this, _: &MouseDownEvent, _, cx| {
                                            this.pending_model_change = None;
                                            cx.notify();
                                        }),
                                    )
                                    .into_any_element(),
                            )
                            .into_any_element(),
                    )
                    .into_any_element()
            });
        div()
            .flex_none()
            .flex()
            .flex_col()
            .gap_1()
            .px_3()
            .py_2()
            .max_h(px(300.))
            .id("xduo-model-picker")
            .overflow_y_scroll()
            .border_1()
            .border_color(theme.border)
            .rounded_md()
            .bg(theme.surface)
            .child(
                div()
                    .flex()
                    .flex_row()
                    .justify_between()
                    .font_weight(FontWeight::BOLD)
                    .child(format!("Choose model · {}", picker.agent))
                    .child(
                        div()
                            .font_weight(FontWeight::NORMAL)
                            .text_color(theme.muted)
                            .child(format!("Current: {current}")),
                    ),
            )
            .child(
                div()
                    .text_sm()
                    .text_color(theme.secondary)
                    .child(format!("Filter: {}▏", picker.filter)),
            )
            .children(pending_confirmation)
            .children(rows)
            .child(div().text_xs().text_color(theme.hint).child(
                if self.pending_model_change.is_some() {
                    "Enter confirms restart · Esc cancels confirmation"
                } else {
                    "Type to filter · ↑/↓ choose · Enter apply · Tab switch agent · Esc close"
                },
            ))
            .into_any_element()
    }

    fn status_row(&self, theme: Theme) -> AnyElement {
        let mut parts: Vec<String> = Vec::new();
        if let Some(view) = self.view.as_ref() {
            parts.push(format!("plan v{}", view.plan_version));
            parts.push(format!(
                "austin {} · tony {}",
                ready_mark(view, AGENT_AUSTIN),
                ready_mark(view, AGENT_TONY)
            ));
            if view.steps_total > 0 {
                parts.push(format!("step {}/{}", view.step_cursor, view.steps_total));
            }
            if !view.delivery.is_empty() {
                parts.push(format!("delivery {}", view.delivery));
            }
        }
        if let Some(banner) = self.banner.as_ref() {
            parts.push(banner.clone());
        }
        div()
            .flex_none()
            .px_3()
            .py_1()
            .bg(theme.surface)
            .text_color(theme.muted)
            .text_sm()
            .child(parts.join("  ·  "))
            .into_any_element()
    }

    fn help_overlay(&self, theme: Theme) -> AnyElement {
        let entries = search_help(&self.help_query, self.is_fast());
        let visible = entries.iter().take(8).map(|entry| {
            div()
                .flex()
                .flex_row()
                .gap_3()
                .child(
                    div()
                        .w(px(260.))
                        .text_color(theme.foreground)
                        .child(entry.shortcut),
                )
                .child(div().text_color(theme.muted).child(entry.description))
                .into_any_element()
        });
        div()
            .flex_none()
            .px_3()
            .py_2()
            .border_b_1()
            .border_color(theme.border)
            .bg(theme.surface)
            .text_sm()
            .child("Keyboard help · type to search · F1 or Ctrl+H closes · Ctrl+Q quits")
            .child(format!(
                "Search: {}_  ·  {} match(es)",
                self.help_query,
                entries.len()
            ))
            .children(visible)
            .children((entries.len() > 8).then(|| {
                div()
                    .text_color(theme.hint)
                    .child(format!("Showing 8 of {} matches", entries.len()))
            }))
            .into_any_element()
    }

    fn slash_palette(&self, theme: Theme) -> AnyElement {
        let matches = self.slash_matches();
        let total = matches.len();
        let selected = self.slash_selected.min(total.saturating_sub(1));
        let visible_count = total.min(6);
        let start = selected.saturating_sub(visible_count - 1);
        let rows = matches
            .iter()
            .enumerate()
            .skip(start)
            .take(visible_count)
            .map(|(index, command)| {
                let (marker, color) = if index == selected {
                    ("▶", theme.foreground)
                } else {
                    (" ", theme.muted)
                };
                div()
                    .flex()
                    .flex_row()
                    .gap_3()
                    .child(
                        div()
                            .w(px(210.))
                            .text_color(color)
                            .child(format!("{marker} {}", command.name)),
                    )
                    .child(div().text_color(theme.muted).child(command.description))
                    .into_any_element()
            });
        div()
            .flex_none()
            .px_3()
            .py_2()
            .border_b_1()
            .border_color(theme.border)
            .bg(theme.surface)
            .text_sm()
            .child(format!(
                "Commands ({}/{}) · ↑/↓ select · Tab complete · Enter run · Esc close",
                selected + 1,
                total
            ))
            .children(rows)
            .into_any_element()
    }

    fn input_row(&self, theme: Theme, cx: &Context<Self>) -> AnyElement {
        div()
            .flex_none()
            .flex()
            .flex_col()
            .gap_1()
            .px_3()
            .py_2()
            .border_t_1()
            .border_color(theme.border)
            .child(
                div()
                    .text_color(theme.input)
                    .cursor(CursorStyle::IBeam)
                    .on_mouse_down(MouseButton::Left, cx.listener(Self::mouse_down))
                    .on_mouse_up(MouseButton::Left, cx.listener(Self::mouse_up))
                    .on_mouse_up_out(MouseButton::Left, cx.listener(Self::mouse_up))
                    .on_mouse_move(cx.listener(Self::mouse_move))
                    .child(ComposerElement {
                        app: cx.entity(),
                        input_enabled: self.model_picker.is_none() && !self.help_open,
                    }),
            )
            .child(
                div()
                    .text_xs()
                    .text_color(theme.hint)
                    .child(hint_line(self.view.as_ref())),
            )
            .into_any_element()
    }
}

fn or_dash(value: &str) -> &str {
    if value.is_empty() {
        "—"
    } else {
        value
    }
}

const OVERVIEW_TEXT_LIMIT: usize = 240;
const PLAN_TEXT_LIMIT: usize = 600;
const STEP_EVIDENCE_LIMIT: usize = 8;
const STREAM_TAIL_TEXT_LIMIT: usize = 4000;

fn overview_rows(view: &View) -> Vec<String> {
    let mut rows = vec![format!(
        "Session {} · {}",
        truncate_text(&view.session, 80),
        truncate_text(&view.repository, 180)
    )];
    rows.push(format!(
        "{} @ {} · {} · {}",
        truncate_text(&view.base_branch, 80),
        truncate_text(&view.base_commit, 48),
        truncate_text(&view.mode, 32),
        truncate_text(&view.phase, 48)
    ));
    if !view.status.is_empty() {
        rows.push(format!(
            "Status: {}",
            truncate_text(&view.status, OVERVIEW_TEXT_LIMIT)
        ));
    }

    for (id, name) in [(AGENT_AUSTIN, "Austin"), (AGENT_TONY, "Tony")] {
        let Some(agent) = view.agents.get(id) else {
            rows.push(format!("{name}: no worktree"));
            continue;
        };
        if !agent.present || agent.worktree_path.is_empty() {
            rows.push(format!("{name}: no worktree"));
        } else {
            rows.push(format!(
                "{name}: {} · {} @ {}",
                truncate_text(&agent.worktree_branch, 80),
                truncate_text(&agent.worktree_head, 48),
                truncate_text(&agent.worktree_path, 160),
            ));
            rows.push(diffstat_summary(name, agent));
        }
        if !agent.ready_note.is_empty() {
            rows.push(format!(
                "{name} ready: {}",
                truncate_text(&agent.ready_note, OVERVIEW_TEXT_LIMIT)
            ));
        }
        if !agent.ready_evidence.is_empty() {
            rows.push(format!(
                "{name} evidence: {}",
                truncate_text(&agent.ready_evidence, OVERVIEW_TEXT_LIMIT)
            ));
        }
    }

    if !view.plan.is_empty() {
        rows.push(format!(
            "Plan v{}: {}",
            view.plan_version,
            truncate_text(&view.plan, PLAN_TEXT_LIMIT)
        ));
    }

    if !view.verification.status.is_empty()
        || !view.verification.head.is_empty()
        || !view.verification.note.is_empty()
        || !view.verification.at.is_empty()
    {
        rows.push(verification_summary(
            "Verification",
            &view.verification.status,
            &view.verification.head,
            &view.verification.note,
            &view.verification.at,
        ));
    }

    let step_window =
        step_evidence_window(&view.step_evidence, view.step_cursor, STEP_EVIDENCE_LIMIT);
    if step_window.start > 0 {
        rows.push(format!(
            "… {} earlier step evidence entries",
            step_window.start
        ));
    }
    for (offset, step) in view.step_evidence[step_window.clone()].iter().enumerate() {
        let title = if step.title.is_empty() {
            "(untitled)"
        } else {
            step.title.as_str()
        };
        let state = if step.done { "done" } else { "pending" };
        let is_current = step.index == view.step_cursor
            || (step.index == 0 && step_window.start + offset + 1 == view.step_cursor);
        let mut summary = format!(
            "Step {} {} · {}{}",
            step.index,
            truncate_text(title, 96),
            state,
            if is_current { " · current" } else { "" }
        );
        if !step.verification.status.is_empty() || !step.verification.note.is_empty() {
            summary.push_str(" · ");
            summary.push_str(&verification_summary(
                "verify",
                &step.verification.status,
                &step.verification.head,
                &step.verification.note,
                &step.verification.at,
            ));
        } else if !step.head.is_empty() {
            summary.push_str(&format!(" · head {}", truncate_text(&step.head, 48)));
        }
        if !step.delivery_head.is_empty() {
            summary.push_str(&format!(
                " · delivered {}",
                truncate_text(&step.delivery_head, 48)
            ));
        }
        rows.push(summary);
    }
    if step_window.end < view.step_evidence.len() {
        rows.push(format!(
            "… {} later step evidence entries",
            view.step_evidence.len() - step_window.end
        ));
    }
    rows
}

fn diffstat_summary(name: &str, agent: &Agent) -> String {
    let diffstat = &agent.diffstat;
    match diffstat.state.to_ascii_lowercase().as_str() {
        "ready" => format!(
            "{name} diff: {} files · +{} −{} · {} untracked",
            diffstat.files, diffstat.insertions, diffstat.deletions, diffstat.untracked
        ),
        "loading" => format!("{name} diff: loading…"),
        "error" => format!(
            "{name} diff error: {}",
            if diffstat.error.is_empty() {
                "unavailable".to_owned()
            } else {
                truncate_text(&diffstat.error, OVERVIEW_TEXT_LIMIT)
            }
        ),
        "unavailable" | "" => format!("{name} diff: unavailable"),
        state => format!("{name} diff: {}", truncate_text(state, 48)),
    }
}

fn verification_summary(label: &str, status: &str, head: &str, note: &str, at: &str) -> String {
    let mut parts = Vec::new();
    if !status.is_empty() {
        parts.push(status.to_owned());
    }
    if !head.is_empty() {
        parts.push(format!("head {}", truncate_text(head, 48)));
    }
    if !note.is_empty() {
        parts.push(truncate_text(note, OVERVIEW_TEXT_LIMIT));
    }
    if !at.is_empty() {
        parts.push(truncate_text(at, 48));
    }
    if parts.is_empty() {
        format!("{label}: no evidence")
    } else {
        format!("{label}: {}", parts.join(" · "))
    }
}

fn step_evidence_window(
    steps: &[crate::protocol::StepVerification],
    cursor: usize,
    limit: usize,
) -> Range<usize> {
    if limit == 0 || steps.is_empty() {
        return 0..0;
    }
    if steps.len() <= limit {
        return 0..steps.len();
    }
    let current = steps
        .iter()
        .position(|step| step.index == cursor)
        .unwrap_or_else(|| cursor.saturating_sub(1).min(steps.len() - 1));
    let start = current.saturating_sub(limit / 2).min(steps.len() - limit);
    start..start + limit
}

fn truncate_text(text: &str, max_chars: usize) -> String {
    let mut chars = text.chars();
    let prefix: String = chars.by_ref().take(max_chars).collect();
    if chars.next().is_some() {
        if max_chars == 0 {
            String::new()
        } else {
            let mut truncated: String = prefix.chars().take(max_chars - 1).collect();
            truncated.push('…');
            truncated
        }
    } else {
        prefix
    }
}

fn work_elapsed_label(work: &WorkPreview) -> Option<String> {
    let mut parts = Vec::new();
    if !work.turn_started.is_empty() || work.turn_elapsed_ms > 0 {
        parts.push(format!("turn {}", format_duration(work.turn_elapsed_ms)));
    }
    if !work.tool_started.is_empty() || work.tool_elapsed_ms > 0 {
        parts.push(format!("tool {}", format_duration(work.tool_elapsed_ms)));
    }
    (!parts.is_empty()).then(|| format!("Elapsed · {}", parts.join(" · ")))
}

fn agent_work_rows(agent: &Agent, theme: Theme) -> Vec<AnyElement> {
    let work = &agent.work;
    let mut rows = Vec::new();
    rows.push(
        div()
            .font_weight(FontWeight::BOLD)
            .text_sm()
            .child("Work preview")
            .into_any_element(),
    );
    if let Some(elapsed) = work_elapsed_label(work) {
        rows.push(
            div()
                .text_xs()
                .text_color(theme.muted)
                .child(elapsed)
                .into_any_element(),
        );
    }
    rows.push(
        div()
            .text_sm()
            .text_color(theme.muted)
            .child(format!(
                "{} · tool depth {} · {}",
                if work.provider_active {
                    "provider active"
                } else {
                    "provider idle"
                },
                work.tool_depth,
                if work.human_attached {
                    "human attached"
                } else {
                    "human detached"
                }
            ))
            .into_any_element(),
    );
    if !work.last_activity.is_empty() {
        rows.push(
            div()
                .text_sm()
                .text_color(theme.secondary)
                .child(format!("Activity: {}", work.last_activity))
                .into_any_element(),
        );
    }
    if !work.tool.is_empty() || !work.tool_detail.is_empty() {
        rows.push(
            div()
                .text_sm()
                .text_color(if work.tool_failed {
                    theme.error
                } else {
                    theme.secondary
                })
                .child(format!(
                    "Tool{}: {}{}{}",
                    if work.tool_failed { " failed" } else { "" },
                    or_dash(&work.tool),
                    if work.tool_detail.is_empty() {
                        String::new()
                    } else {
                        format!(" · {}", work.tool_detail)
                    },
                    if work.tool_started.is_empty() {
                        String::new()
                    } else {
                        format!(" · started {}", work.tool_started)
                    }
                ))
                .into_any_element(),
        );
    }
    if !work.last_error.is_empty() {
        rows.push(
            div()
                .text_sm()
                .text_color(theme.error)
                .child(format!("Error: {}", work.last_error))
                .into_any_element(),
        );
    }
    if !work.stream_tail.is_empty() {
        let mut stream_tail_hasher = DefaultHasher::new();
        agent.worktree_path.hash(&mut stream_tail_hasher);
        let stream_tail_id = stream_tail_hasher.finish();
        let tail = truncate_text(&work.stream_tail, STREAM_TAIL_TEXT_LIMIT);
        let tail_lines = tail
            .split('\n')
            .map(|line| line.to_owned())
            .collect::<Vec<_>>();
        rows.push(
            div()
                .text_xs()
                .text_color(theme.muted)
                .child("Latest output")
                .into_any_element(),
        );
        rows.push(
            div()
                .id(("xduo-stream-tail", stream_tail_id))
                .max_h(px(112.))
                .overflow_y_scroll()
                .bg(theme.background)
                .rounded_sm()
                .px_2()
                .py_1()
                .child(
                    div()
                        .flex()
                        .flex_col()
                        .children(tail_lines.into_iter().map(|line| {
                            div()
                                .w_full()
                                .min_w_0()
                                .whitespace_normal()
                                .text_sm()
                                .text_color(theme.secondary)
                                .child(if line.is_empty() {
                                    " ".to_owned()
                                } else {
                                    line
                                })
                        })),
                )
                .into_any_element(),
        );
    }
    if work.context_tokens > 0 || work.context_window > 0 || work.tokens_per_second > 0.0 {
        rows.push(
            div()
                .text_xs()
                .text_color(theme.muted)
                .child(format!(
                    "Context {}/{} tokens · {:.1} tokens/s",
                    work.context_tokens, work.context_window, work.tokens_per_second
                ))
                .into_any_element(),
        );
    }
    rows.push(
        div()
            .text_xs()
            .text_color(theme.muted)
            .child(format!(
                "{} tools · {} failures{}",
                work.tools,
                work.failures,
                if work.turn_started.is_empty() {
                    String::new()
                } else {
                    format!(" · turn started {}", work.turn_started)
                }
            ))
            .into_any_element(),
    );
    for note in work.recent.iter().rev().take(4).rev() {
        rows.push(
            div()
                .text_xs()
                .text_color(if note.ok { theme.muted } else { theme.error })
                .child(format!(
                    "{} {} · {}{}",
                    if note.ok { "✓" } else { "✗" },
                    note.name,
                    format_duration(note.duration_ms),
                    if note.detail.is_empty() {
                        String::new()
                    } else {
                        format!(" · {}", note.detail)
                    }
                ))
                .into_any_element(),
        );
    }
    rows
}

fn format_duration(duration_ms: u64) -> String {
    if duration_ms < 1000 {
        format!("{duration_ms} ms")
    } else {
        let tenths = duration_ms.saturating_add(50) / 100;
        format!("{:.1} s", tenths as f64 / 10.0)
    }
}

/// hint_line names the keys this session actually answers. Goal escalation is left
/// out of every other mode, because the key would only ever be refused there.
fn hint_line(view: Option<&View>) -> String {
    let mut keys = vec!["Enter send", "Shift+Enter newline", "Esc clear"];
    if view.map(|view| view.mode.as_str()) == Some(MODE_FAST) {
        keys.push("Ctrl+G goal");
    }
    keys.extend([
        "Ctrl+L language",
        "Ctrl+R/Y restart Austin/Tony",
        "Ctrl+O overview",
        "Ctrl+P work preview",
        "Ctrl+M / Shift+Ctrl+M model picker",
        "Alt+Ctrl+T / Shift+Ctrl+T thinking",
        "Ctrl+T timestamps",
        "Ctrl+X copy Austin",
        "Click message · arrows/Page/Home/End navigate · Ctrl+C copy selection",
        "F1 help",
        "Ctrl+Q quit",
    ]);
    keys.join(" · ")
}

fn ready_mark(view: &View, agent: &str) -> &'static str {
    if view.ready.get(agent).copied().unwrap_or(false) {
        "signed"
    } else {
        "pending"
    }
}

fn event_speaker(event: &Event, theme: Theme) -> (String, Rgba) {
    if !event.agent.is_empty() {
        return (event.agent.clone(), theme.speaker(&event.agent));
    }
    match event.kind.as_str() {
        "error" => ("error".to_owned(), theme.error),
        "verdict" => ("verdict".to_owned(), theme.warning),
        "user" => ("human".to_owned(), theme.speaker("human")),
        _ => ("duo".to_owned(), theme.speaker("duo")),
    }
}

fn markdown_elements(blocks: &[Block], theme: Theme) -> Vec<AnyElement> {
    blocks
        .iter()
        .map(|block| match block {
            Block::Paragraph(lines) => div()
                .flex()
                .flex_col()
                .gap_0()
                .children(lines.iter().map(|spans| inline_line(spans, theme)))
                .into_any_element(),
            Block::Heading { level, spans } => div()
                .font_weight(FontWeight::BOLD)
                .text_color(theme.foreground)
                .when(*level == 1, |element| element.text_2xl())
                .when(*level == 2, |element| element.text_xl())
                .when(*level >= 3, |element| element.text_lg())
                .child(inline_line(spans, theme))
                .into_any_element(),
            Block::ListItem(spans) => div()
                .flex()
                .flex_row()
                .gap_1()
                .child(div().flex_none().child("•"))
                .child(inline_line(spans, theme))
                .into_any_element(),
            Block::Code(code) => div()
                .flex()
                .flex_col()
                .bg(theme.background)
                .px_1()
                .children(code.split('\n').map(|line| {
                    div()
                        .text_sm()
                        .text_color(theme.secondary)
                        .child(if line.is_empty() {
                            " ".to_owned()
                        } else {
                            line.to_owned()
                        })
                }))
                .into_any_element(),
        })
        .collect()
}

fn inline_line(spans: &[Span], theme: Theme) -> AnyElement {
    div()
        .flex()
        .flex_row()
        .flex_wrap()
        .flex_1()
        .min_w_0()
        .children(spans.iter().map(|span| {
            div()
                .flex_shrink()
                .min_w_0()
                .when(span.style.bold, |element| {
                    element.font_weight(FontWeight::BOLD)
                })
                .when(span.style.italic, |element| element.italic())
                .when(span.style.link, |element| {
                    element.text_color(theme.secondary).underline()
                })
                .when(span.style.code, |element| {
                    element.bg(theme.background).rounded_sm().px_1()
                })
                .child(span.text.clone())
        }))
        .into_any_element()
}

#[cfg(test)]
mod tests {
    use super::{
        catalog_state, confirm_quit, decode_history, encode_history_entry, format_duration,
        latest_austin_message, line_index_for_offset, line_range, matching_models,
        model_restart_warning, model_selection_command, model_switch_decision,
        needs_quit_confirmation, overview_rows, range_index_for_offset, replace_selection,
        reset_slash_palette_after_text_edit, resize_edge, selected_event_text,
        selection_after_front_drain, step_evidence_window, timeline_navigation_target,
        truncate_text, utf16_range, utf8_range, visual_ranges_from_boundaries, work_elapsed_label,
        Agent, CatalogState, Model, ModelSwitchDecision, Theme, AGENT_AUSTIN, AGENT_TONY,
        PLAN_TEXT_LIMIT, QUIT_CONFIRM_WINDOW, STEP_EVIDENCE_LIMIT,
    };
    use crate::protocol::{Diffstat, Event, StepVerification, Verification, View, WorkPreview};
    use gpui::{point, px, size, ResizeEdge, WindowAppearance};
    use std::time::{Duration, Instant};

    #[test]
    fn wrapped_visual_ranges_preserve_byte_offsets_at_each_wrap() {
        assert_eq!(
            visual_ranges_from_boundaries(10, vec![0, 3, 7], vec![3, 7, 10]),
            vec![0..3, 3..7, 7..10]
        );
        assert_eq!(
            visual_ranges_from_boundaries(3, vec![0, 2], vec![2, 3]),
            vec![0..2, 2..3]
        );
        let rows = vec![0..3, 3..7, 7..10];
        assert_eq!(range_index_for_offset(&rows, 2), Some(0));
        assert_eq!(range_index_for_offset(&rows, 3), Some(1));
        assert_eq!(range_index_for_offset(&rows, 10), Some(2));
        assert_eq!(range_index_for_offset(&[0..0, 1..1], 1), Some(1));
    }

    #[test]
    fn theme_palette_tracks_light_and_dark_appearance() {
        let light = Theme::from_appearance(WindowAppearance::Light);
        let dark = Theme::from_appearance(WindowAppearance::Dark);

        assert_ne!(light.background, dark.background);
        assert_ne!(light.foreground, dark.foreground);
        assert_ne!(light.surface, dark.surface);
        assert_ne!(light.speaker("austin"), dark.speaker("austin"));
    }

    #[test]
    fn composer_ranges_and_replacement_preserve_unicode_boundaries() {
        let text = "a界🙂z";
        assert_eq!(utf8_range(text, 1..4), 1.."a界🙂".len());
        assert_eq!(utf16_range(text, "a界".len()..text.len()), 2..5);

        let mut text = text.to_owned();
        let mut selection = 1.."a界".len();
        replace_selection(&mut text, &mut selection, "文");
        assert_eq!(text, "a文🙂z");
        assert_eq!(selection, "a文".len().."a文".len());
    }

    #[test]
    fn persistent_history_encoding_matches_tui_format() {
        let encoded = format!(
            "{}{}",
            encode_history_entry("inspect 界面"),
            encode_history_entry("first line\nsecond 🙂")
        );
        assert_eq!(
            decode_history(&encoded),
            ["inspect 界面", "first line\nsecond 🙂"]
        );
    }

    #[test]
    fn composer_line_mapping_tracks_newlines_and_trailing_empty_line() {
        let text = "first\n界🙂\n";
        assert_eq!(line_range(text, 0), (0, 5));
        assert_eq!(line_range(text, 1), (6, 13));
        assert_eq!(line_range(text, 2), (14, 14));
        assert_eq!(line_index_for_offset(text, 6), 1);
        assert_eq!(line_index_for_offset(text, text.len()), 2);
    }

    #[test]
    fn model_catalog_states_distinguish_loading_error_empty_and_filter_miss() {
        let mut agent = Agent::default();
        assert_eq!(catalog_state(Some(&agent), ""), CatalogState::Loading);

        agent.models_loaded = true;
        agent.models_error = "driver has no model catalog".into();
        assert_eq!(catalog_state(Some(&agent), ""), CatalogState::Error);

        agent.models_error.clear();
        assert_eq!(catalog_state(Some(&agent), ""), CatalogState::Empty);

        agent.models.push(Model {
            provider: "provider-x".into(),
            id: "model-7".into(),
            reference: "provider-x/model-7".into(),
            thinking: true,
            images: false,
        });
        assert_eq!(
            catalog_state(Some(&agent), "missing"),
            CatalogState::NoMatches
        );
        assert_eq!(catalog_state(Some(&agent), "MODEL-7"), CatalogState::Ready);
        assert_eq!(matching_models(&agent, "provider-x").len(), 1);
    }

    #[test]
    fn model_selection_uses_catalog_provider_and_id() {
        let model = Model {
            provider: "real-provider".into(),
            id: "actual-model-id".into(),
            reference: "display/reference".into(),
            thinking: false,
            images: true,
        };
        let command = model_selection_command("tony", &model);
        assert_eq!(command.kind, "set_model");
        assert_eq!(command.agent.as_deref(), Some("tony"));
        assert_eq!(command.provider.as_deref(), Some("real-provider"));
        assert_eq!(command.model.as_deref(), Some("actual-model-id"));
        assert_eq!(format_duration(1250), "1.3 s");
    }

    #[test]
    fn model_switch_decision_avoids_noop_and_requires_non_live_confirmation() {
        let target = Model {
            provider: "provider-x".into(),
            id: "model-7".into(),
            reference: "provider-x/model-7".into(),
            ..Default::default()
        };
        let mut agent = Agent {
            model: "provider-x/model-7".into(),
            ..Default::default()
        };
        assert_eq!(
            model_switch_decision(&agent, &target),
            ModelSwitchDecision::AlreadyCurrent
        );

        agent.model = "provider-y/model-3".into();
        agent.live_model_switch = true;
        assert_eq!(
            model_switch_decision(&agent, &target),
            ModelSwitchDecision::Apply
        );

        agent.live_model_switch = false;
        assert_eq!(
            model_switch_decision(&agent, &target),
            ModelSwitchDecision::ConfirmRestart
        );
        assert_eq!(
            model_restart_warning("austin", &target),
            "Changing Austin to provider-x/model-7 will restart Austin and interrupt its current turn. Press Enter to confirm or Esc to cancel."
        );
    }

    #[test]
    fn text_edits_reopen_a_dismissed_slash_palette_and_clear_selection() {
        let mut dismissed = true;
        let mut selected = 4;
        let mut selection_explicit = true;

        reset_slash_palette_after_text_edit(&mut dismissed, &mut selected, &mut selection_explicit);
        assert!(!dismissed);
        assert_eq!(selected, 0);
        assert!(!selection_explicit);
    }

    #[test]
    fn overview_reports_diffstat_evidence_and_verification_states() {
        let mut view = View::default();
        let mut austin = Agent {
            present: true,
            worktree_path: "/tmp/austin".into(),
            diffstat: Diffstat {
                state: "loading".into(),
                ..Default::default()
            },
            ready_note: "reviewed".into(),
            ready_evidence: "cargo test".into(),
            ..Default::default()
        };
        view.agents.insert(AGENT_AUSTIN.into(), austin.clone());
        view.agents.insert(
            AGENT_TONY.into(),
            Agent {
                present: true,
                worktree_path: "/tmp/tony".into(),
                diffstat: Diffstat {
                    state: "error".into(),
                    error: "git status failed".into(),
                    ..Default::default()
                },
                ..Default::default()
            },
        );
        view.verification = Verification {
            status: "passed".into(),
            head: "abc123".into(),
            note: "checks passed".into(),
            ..Default::default()
        };
        view.step_evidence.push(StepVerification {
            index: 2,
            title: "ship it".into(),
            done: true,
            verification: Verification {
                status: "passed".into(),
                note: "all checks".into(),
                ..Default::default()
            },
            delivery_head: "def456".into(),
            ..Default::default()
        });

        let rows = overview_rows(&view);
        assert!(rows.iter().any(|row| row == "Austin diff: loading…"));
        assert!(rows
            .iter()
            .any(|row| row.contains("Austin ready: reviewed")));
        assert!(rows
            .iter()
            .any(|row| row.contains("Austin evidence: cargo test")));
        assert!(rows
            .iter()
            .any(|row| row.contains("Tony diff error: git status failed")));
        assert!(rows
            .iter()
            .any(|row| row.contains("Verification: passed · head abc123 · checks passed")));
        assert!(rows
            .iter()
            .any(|row| row.contains("Step 2 ship it · done") && row.contains("delivered def456")));

        austin.present = false;
        austin.diffstat.state = "ready".into();
        view.agents.insert(AGENT_AUSTIN.into(), austin);
        let rows = overview_rows(&view);
        assert!(rows.iter().any(|row| row == "Austin: no worktree"));
    }

    #[test]
    fn overview_and_work_elapsed_text_are_bounded_and_unicode_safe() {
        assert_eq!(truncate_text("界🙂abc", 3), "界🙂…");
        assert_eq!(truncate_text("abc", 3), "abc");
        assert_eq!(truncate_text("abc", 0), "");
        assert_eq!(work_elapsed_label(&WorkPreview::default()), None);
        assert_eq!(
            work_elapsed_label(&WorkPreview {
                turn_elapsed_ms: 1250,
                tool_elapsed_ms: 500,
                ..Default::default()
            }),
            Some("Elapsed · turn 1.3 s · tool 500 ms".into())
        );

        let mut view = View::default();
        view.plan = "界".repeat(PLAN_TEXT_LIMIT + 10);
        assert!(overview_rows(&view)
            .iter()
            .any(|row| row.contains(&format!("{}…", "界".repeat(PLAN_TEXT_LIMIT - 1)))));
        assert!(!overview_rows(&view)
            .iter()
            .any(|row| row == "Verification: not run"));
    }

    #[test]
    fn long_step_overview_window_keeps_the_current_step_visible() {
        let steps = (1..=20)
            .map(|index| StepVerification {
                index,
                title: format!("step {index}"),
                ..Default::default()
            })
            .collect::<Vec<_>>();
        let window = step_evidence_window(&steps, 17, STEP_EVIDENCE_LIMIT);
        assert!(window.contains(&16));
        assert_eq!(window.len(), STEP_EVIDENCE_LIMIT);

        let view = View {
            step_cursor: 17,
            step_evidence: steps,
            ..Default::default()
        };
        let rows = overview_rows(&view);
        assert!(rows
            .iter()
            .any(|row| row == "Step 17 step 17 · pending · current"));
        assert!(rows
            .iter()
            .any(|row| row == "… 12 earlier step evidence entries"));
        assert!(!rows
            .iter()
            .any(|row| row.contains("later step evidence entries")));
    }

    #[test]
    fn unfinished_quit_requires_second_request_within_three_seconds() {
        let now = Instant::now();
        let mut deadline = None;
        assert!(!confirm_quit(&mut deadline, now));
        assert_eq!(deadline, Some(now + QUIT_CONFIRM_WINDOW));
        assert!(confirm_quit(&mut deadline, now + Duration::from_secs(2)));
        assert_eq!(deadline, None);

        assert!(!confirm_quit(&mut deadline, now));
        assert!(!confirm_quit(
            &mut deadline,
            now + QUIT_CONFIRM_WINDOW + Duration::from_millis(1)
        ));
        assert_eq!(
            deadline,
            Some(now + QUIT_CONFIRM_WINDOW + Duration::from_millis(1) + QUIT_CONFIRM_WINDOW)
        );
    }

    #[test]
    fn only_started_non_done_sessions_need_quit_confirmation() {
        assert!(!needs_quit_confirmation(false, "EXECUTE"));
        assert!(!needs_quit_confirmation(true, "DONE"));
        assert!(!needs_quit_confirmation(true, "done"));
        assert!(needs_quit_confirmation(true, "EXECUTE"));
    }

    #[test]
    fn client_window_edges_pick_resize_direction() {
        let size = size(px(800.), px(600.));
        assert_eq!(
            resize_edge(point(px(2.), px(2.)), size, px(6.)),
            Some(ResizeEdge::TopLeft)
        );
        assert_eq!(
            resize_edge(point(px(798.), px(598.)), size, px(6.)),
            Some(ResizeEdge::BottomRight)
        );
        assert_eq!(resize_edge(point(px(400.), px(300.)), size, px(6.)), None);
    }

    #[test]
    fn copy_target_is_latest_austin_assistant_message_to_human() {
        let events = vec![
            Event {
                agent: "austin".into(),
                kind: "assistant".into(),
                text: "first".into(),
                ..Default::default()
            },
            Event {
                agent: "austin".into(),
                label: "Austin → Tony".into(),
                kind: "assistant".into(),
                text: "to peer".into(),
                ..Default::default()
            },
            Event {
                agent: "tony".into(),
                kind: "assistant".into(),
                text: "Tony".into(),
                ..Default::default()
            },
            Event {
                agent: "austin".into(),
                kind: "assistant".into(),
                text: "latest".into(),
                ..Default::default()
            },
            Event {
                agent: "austin".into(),
                label: "Austin → Tony".into(),
                kind: "assistant".into(),
                text: "peer history with empty peer field".into(),
                ..Default::default()
            },
            Event {
                agent: "austin".into(),
                label: "Austin → Human".into(),
                kind: "assistant".into(),
                text: " \n\t ".into(),
                ..Default::default()
            },
        ];
        assert_eq!(
            latest_austin_message(&events).map(|event| event.text.as_str()),
            Some("latest")
        );
    }

    #[test]
    fn timeline_selection_tracks_front_trimming_and_copies_original_text() {
        assert_eq!(selection_after_front_drain(Some(0), 1), None);
        assert_eq!(selection_after_front_drain(Some(2), 1), Some(1));
        let event = Event {
            text: "**粗体**\n```\nraw <b>HTML</b>\n```".into(),
            ..Default::default()
        };
        assert_eq!(
            selected_event_text(&[event], Some(0)),
            Some("**粗体**\n```\nraw <b>HTML</b>\n```")
        );
        assert_eq!(selected_event_text(&[], Some(0)), None);
    }

    #[test]
    fn timeline_navigation_clamps_and_supports_home_end() {
        assert_eq!(timeline_navigation_target(None, 3, 1), Some(0));
        assert_eq!(timeline_navigation_target(None, 3, -1), Some(2));
        assert_eq!(timeline_navigation_target(Some(1), 3, -1), Some(0));
        assert_eq!(timeline_navigation_target(Some(1), 3, 1), Some(2));
        assert_eq!(timeline_navigation_target(Some(1), 3, isize::MIN), Some(0));
        assert_eq!(timeline_navigation_target(Some(1), 3, isize::MAX), Some(2));
        assert_eq!(timeline_navigation_target(Some(0), 0, 1), None);
    }
}
