//! The window: state, input and rendering.
//!
//! It owns a copy of the last `View`, the timeline it has been sent, a text buffer
//! and a channel of frames from the host. It holds no session handle, so the window
//! cannot start, stop or sign anything: it sends a command and waits for the next
//! view, exactly like a terminal interface.

use std::io::Write as _;
use std::ops::Range;

use gpui::prelude::*;
use gpui::{
    div, px, relative, rgb, AnyElement, App, Bounds, Context, Element, ElementInputHandler, Entity,
    EntityInputHandler, FocusHandle, Focusable, FontWeight, GlobalElementId, KeyDownEvent,
    LayoutId, Render, Rgba, StatefulInteractiveElement, Style, UTF16Selection, Window,
    WindowAppearance,
};
use smol::channel::Receiver;

use crate::protocol::{ClientMessage, Event, ServerMessage, View};

/// The lowercase wire spellings. The host normalizes identity once, at its own
/// boundary, so this side uses these constants and never re-cases a value it
/// received.
const AGENT_AUSTIN: &str = "austin";
const AGENT_TONY: &str = "tony";

/// The one mode whose session accepts an escalation to Goal.
const MODE_FAST: &str = "fast";

/// The timeline keeps the most recent entries. A window is not an archive, and an
/// unbounded vector in a process that may run for hours is a leak.
const MAX_EVENTS: usize = 500;

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
    placeholder: Rgba,
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
                placeholder: rgb(0x768197),
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
                placeholder: rgb(0x6b7280),
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
    input: String,
    selection: Range<usize>,
    marked_range: Option<Range<usize>>,
    /// The last rejected command or the reason the session ended.
    banner: Option<String>,
    closed: bool,
    overview_open: bool,
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
            input: String::new(),
            selection: 0..0,
            marked_range: None,
            banner: None,
            closed: false,
            overview_open: false,
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

        if keystroke.modifiers.control {
            match keystroke.key.as_str() {
                "q" => {
                    self.send(ClientMessage::quit());
                    cx.quit();
                    return;
                }
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
            "enter" if self.marked_range.is_some() => {
                // Let the active IME confirm its candidate before a later Enter
                // submits the resulting task.
            }
            "enter" if keystroke.modifiers.shift => {
                replace_selection(&mut self.input, &mut self.selection, "\n");
            }
            "enter" => {
                let text = self.input.trim().to_owned();
                if !text.is_empty() {
                    self.input.clear();
                    self.selection = 0..0;
                    self.send(ClientMessage::submit_task(text));
                }
            }
            "escape" => {
                self.input.clear();
                self.selection = 0..0;
                self.marked_range = None;
            }
            "backspace" => {
                if self.selection.is_empty() && self.selection.start > 0 {
                    self.selection.start = previous_boundary(&self.input, self.selection.start);
                }
                replace_selection(&mut self.input, &mut self.selection, "");
            }
            "left" => move_cursor(self, false),
            "right" => move_cursor(self, true),
            // Text, including spaces and IME commits, arrives through GPUI's
            // InputHandler bridge. Handling key_char here would insert it twice.
            _ => {}
        }
        cx.notify();
    }
}

impl Focusable for DuoApp {
    fn focus_handle(&self, _: &gpui::App) -> FocusHandle {
        self.focus.clone()
    }
}

impl Render for DuoApp {
    fn render(&mut self, window: &mut Window, cx: &mut Context<Self>) -> impl IntoElement {
        let theme = Theme::from_appearance(window.appearance());
        div()
            .flex()
            .flex_col()
            .size_full()
            .bg(theme.background)
            .text_color(theme.foreground)
            .font_family("monospace")
            .track_focus(&self.focus)
            .on_key_down(
                cx.listener(|this, event: &KeyDownEvent, _window, cx| this.on_key(event, cx)),
            )
            .child(self.header(theme))
            .child(self.agents_row(theme))
            .children(self.overview_open.then(|| self.overview(theme)))
            .child(self.timeline(theme))
            .child(self.status_row(theme))
            .child(self.input_row(theme, cx))
    }
}

/// Registers GPUI's native text input bridge during paint. Text is rendered by the
/// row above; this small element exists to expose IME and clipboard input.
struct InputHandlerElement {
    app: Entity<DuoApp>,
    focus: FocusHandle,
}

impl gpui::IntoElement for InputHandlerElement {
    type Element = Self;
    fn into_element(self) -> Self {
        self
    }
}

impl Element for InputHandlerElement {
    type RequestLayoutState = ();
    type PrepaintState = ();

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
        style.size.height = px(1.).into();
        (window.request_layout(style, [], cx), ())
    }
    fn prepaint(
        &mut self,
        _: Option<&GlobalElementId>,
        _: Option<&gpui::InspectorElementId>,
        _: Bounds<gpui::Pixels>,
        _: &mut (),
        _: &mut Window,
        _: &mut App,
    ) {
    }
    fn paint(
        &mut self,
        _: Option<&GlobalElementId>,
        _: Option<&gpui::InspectorElementId>,
        bounds: Bounds<gpui::Pixels>,
        _: &mut (),
        _: &mut (),
        window: &mut Window,
        cx: &mut App,
    ) {
        window.handle_input(
            &self.focus,
            ElementInputHandler::new(bounds, self.app.clone()),
            cx,
        );
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
        let target = range
            .map(|range| utf8_range(&self.input, range))
            .or_else(|| self.marked_range.clone())
            .unwrap_or_else(|| self.selection.clone());
        self.input.replace_range(target.clone(), text);
        let end = target.start + text.len();
        self.selection = end..end;
        self.marked_range = None;
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
        cx.notify();
    }
    fn bounds_for_range(
        &mut self,
        _: Range<usize>,
        bounds: Bounds<gpui::Pixels>,
        _: &mut Window,
        _: &mut Context<Self>,
    ) -> Option<Bounds<gpui::Pixels>> {
        Some(bounds)
    }
    fn character_index_for_point(
        &mut self,
        _: gpui::Point<gpui::Pixels>,
        _: &mut Window,
        _: &mut Context<Self>,
    ) -> Option<usize> {
        Some(self.input.encode_utf16().count())
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

fn move_cursor(app: &mut DuoApp, right: bool) {
    let cursor = if app.selection.is_empty() {
        if right {
            app.input[app.selection.end..]
                .chars()
                .next()
                .map(|ch| app.selection.end + ch.len_utf8())
                .unwrap_or(app.selection.end)
        } else {
            previous_boundary(&app.input, app.selection.start)
        }
    } else if right {
        app.selection.end
    } else {
        app.selection.start
    };
    app.selection = cursor..cursor;
}

// Rendering helpers. Each returns AnyElement so a row can be built from mixed
// children without making the element type of `render` depend on its contents.

impl DuoApp {
    fn overview(&self, theme: Theme) -> AnyElement {
        let mut rows = Vec::new();
        if let Some(view) = self.view.as_ref() {
            rows.push(format!("Session {} · {}", view.session, view.repository));
            rows.push(format!(
                "{} @ {} · {} · {}",
                view.base_branch, view.base_commit, view.mode, view.phase
            ));
            if !view.status.is_empty() {
                rows.push(view.status.clone());
            }
            if !view.plan.is_empty() {
                rows.push(format!("Plan v{}\n{}", view.plan_version, view.plan));
            }
        } else {
            rows.push("Waiting for Duo…".to_owned());
        }
        div()
            .id("xduo-overview")
            .flex_none()
            .max_h(px(180.))
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

    fn header(&self, theme: Theme) -> AnyElement {
        let mut children: Vec<AnyElement> = Vec::new();

        let version = self
            .view
            .as_ref()
            .map(|view| view.version.clone())
            .unwrap_or_default();
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

    fn agents_row(&self, theme: Theme) -> AnyElement {
        div()
            .flex()
            .flex_none()
            .flex_row()
            .gap_3()
            .px_3()
            .py_2()
            .child(self.agent_card(AGENT_AUSTIN, "Austin", theme))
            .child(self.agent_card(AGENT_TONY, "Tony", theme))
            .into_any_element()
    }

    fn agent_card(&self, id: &str, title: &str, theme: Theme) -> AnyElement {
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

    fn timeline(&self, theme: Theme) -> AnyElement {
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

        for event in &self.events {
            children.push(self.timeline_row(event, theme));
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
            .children(children)
            .into_any_element()
    }

    fn timeline_row(&self, event: &Event, theme: Theme) -> AnyElement {
        let (speaker, color) = event_speaker(event, theme);
        let header = if event.label.is_empty() {
            speaker
        } else {
            event.label.clone()
        };
        div()
            .flex_none()
            .flex()
            .flex_row()
            .items_start()
            .gap_2()
            .child(
                div()
                    .w(px(130.))
                    .flex_none()
                    .text_sm()
                    .truncate()
                    .text_color(color)
                    .child(header),
            )
            .child(
                div()
                    .flex_1()
                    .min_w_0()
                    .text_sm()
                    .child(if event.text.is_empty() {
                        " ".to_owned()
                    } else {
                        event.text.clone()
                    }),
            )
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

    fn input_row(&self, theme: Theme, cx: &Context<Self>) -> AnyElement {
        let empty = self.input.is_empty();
        let text = if empty {
            "Type a task and press Enter…".to_owned()
        } else {
            let cursor = self.selection.end;
            format!("{}▏{}", &self.input[..cursor], &self.input[cursor..])
        };
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
                    .text_color(if empty {
                        theme.placeholder
                    } else {
                        theme.input
                    })
                    .child(text),
            )
            .child(InputHandlerElement {
                app: cx.entity(),
                focus: self.focus.clone(),
            })
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

#[cfg(test)]
mod tests {
    use super::{replace_selection, utf16_range, utf8_range, Theme};
    use gpui::WindowAppearance;

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
}
