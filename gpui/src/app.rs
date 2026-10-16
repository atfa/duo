//! The window: state, input and rendering.
//!
//! It owns a copy of the last `View`, the timeline it has been sent, a text buffer
//! and a channel of frames from the host. It holds no session handle, so the window
//! cannot start, stop or sign anything: it sends a command and waits for the next
//! view, exactly like a terminal interface.

use std::io::Write as _;

use gpui::prelude::*;
use gpui::{
    div, px, rgb, AnyElement, Context, FocusHandle, Focusable, FontWeight, KeyDownEvent, Render,
    Rgba, StatefulInteractiveElement, Window,
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

pub struct DuoApp {
    view: Option<View>,
    events: Vec<Event>,
    input: String,
    /// The last rejected command or the reason the session ended.
    banner: Option<String>,
    closed: bool,
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
            banner: None,
            closed: false,
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
            "enter" => {
                let text = self.input.trim().to_owned();
                if !text.is_empty() {
                    self.input.clear();
                    self.send(ClientMessage::submit_task(text));
                }
            }
            "escape" => self.input.clear(),
            "backspace" => {
                self.input.pop();
            }
            "space" => self.input.push(' '),
            _ => {
                // A modified key is a binding, not text; alt-graph and IME input
                // arrive through key_char on an unmodified keystroke.
                if !keystroke.modifiers.alt && !keystroke.modifiers.platform {
                    if let Some(ch) = keystroke.key_char.as_deref() {
                        if !ch.is_empty() && !ch.chars().any(char::is_control) {
                            self.input.push_str(ch);
                        }
                    }
                }
            }
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
    fn render(&mut self, _window: &mut Window, cx: &mut Context<Self>) -> impl IntoElement {
        div()
            .flex()
            .flex_col()
            .size_full()
            .bg(rgb(0x0f1116))
            .text_color(rgb(0xd8dbe2))
            .font_family("monospace")
            .track_focus(&self.focus)
            .on_key_down(
                cx.listener(|this, event: &KeyDownEvent, _window, cx| this.on_key(event, cx)),
            )
            .child(self.header())
            .child(self.agents_row())
            .child(self.timeline())
            .child(self.status_row())
            .child(self.input_row())
    }
}

// Rendering helpers. Each returns AnyElement so a row can be built from mixed
// children without making the element type of `render` depend on its contents.

impl DuoApp {
    fn header(&self) -> AnyElement {
        let mut children: Vec<AnyElement> = Vec::new();

        let version = self
            .view
            .as_ref()
            .map(|view| view.version.clone())
            .unwrap_or_default();
        children.push(
            div()
                .font_weight(FontWeight::BOLD)
                .child(format!("Duo {version}"))
                .into_any_element(),
        );

        if let Some(view) = self.view.as_ref() {
            let mode = view.mode.to_uppercase();
            children.push(
                div()
                    .px_2()
                    .py_1()
                    .rounded_md()
                    .bg(mode_color(&mode))
                    .text_color(rgb(0x0f1116))
                    .font_weight(FontWeight::BOLD)
                    .child(mode)
                    .into_any_element(),
            );
            children.push(
                div()
                    .text_color(rgb(0x8b93a5))
                    .child(view.phase.clone())
                    .into_any_element(),
            );
            children.push(
                div()
                    .text_color(rgb(0x6b7280))
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
                    .text_color(rgb(0xe06c6c))
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
            .border_color(rgb(0x252a35))
            .children(children)
            .into_any_element()
    }

    fn agents_row(&self) -> AnyElement {
        div()
            .flex()
            .flex_none()
            .flex_row()
            .gap_3()
            .px_3()
            .py_2()
            .child(self.agent_card(AGENT_AUSTIN, "Austin"))
            .child(self.agent_card(AGENT_TONY, "Tony"))
            .into_any_element()
    }

    fn agent_card(&self, id: &str, title: &str) -> AnyElement {
        let agent = self.view.as_ref().and_then(|view| view.agents.get(id));
        let color = speaker_color(id);

        let (detail, activity, state) = match agent {
            Some(agent) => (
                format!("{} · {} · {}", agent.driver, or_dash(&agent.model), agent.process),
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
            None => ("waiting for the session".to_owned(), String::new(), "○ unknown".to_owned()),
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
                .child(
                    div()
                        .text_color(rgb(0x8b93a5))
                        .text_sm()
                        .child(state),
                )
                .into_any_element(),
            div()
                .text_color(rgb(0x8b93a5))
                .text_sm()
                .child(detail)
                .into_any_element(),
        ];
        if !activity.is_empty() {
            rows.push(
                div()
                    .text_color(rgb(0xa9b1c1))
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
            .bg(rgb(0x151922))
            .rounded_md()
            .border_1()
            .border_color(rgb(0x252a35))
            .children(rows)
            .into_any_element()
    }

    fn timeline(&self) -> AnyElement {
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
                                .text_color(rgb(0xd9a441))
                                .child("notice"),
                        )
                        .child(
                            div()
                                .flex_1()
                                .min_w_0()
                                .text_sm()
                                .text_color(rgb(0xd9a441))
                                .child(notice.clone()),
                        )
                        .into_any_element(),
                );
            }
        }

        for event in &self.events {
            children.push(self.timeline_row(event));
        }

        if children.is_empty() {
            children.push(
                div()
                    .text_color(rgb(0x6b7280))
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

    fn timeline_row(&self, event: &Event) -> AnyElement {
        let (speaker, color) = event_speaker(event);
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

    fn status_row(&self) -> AnyElement {
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
            .bg(rgb(0x151922))
            .text_color(rgb(0x8b93a5))
            .text_sm()
            .child(parts.join("  ·  "))
            .into_any_element()
    }

    fn input_row(&self) -> AnyElement {
        let empty = self.input.is_empty();
        let text = if empty {
            "Type a task and press Enter…".to_owned()
        } else {
            // A caret stands in for a text cursor: the MVP has no IME or selection,
            // and pretending otherwise would be worse than showing none.
            format!("{}▏", self.input)
        };
        div()
            .flex_none()
            .flex()
            .flex_col()
            .gap_1()
            .px_3()
            .py_2()
            .border_t_1()
            .border_color(rgb(0x252a35))
            .child(
                div()
                    .text_color(if empty { rgb(0x6b7280) } else { rgb(0xe6e9ef) })
                    .child(text),
            )
            .child(
                div()
                    .text_xs()
                    .text_color(rgb(0x5b6472))
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
    let mut keys = vec!["Enter send", "Esc clear"];
    if view.map(|view| view.mode.as_str()) == Some(MODE_FAST) {
        keys.push("Ctrl+G goal");
    }
    keys.extend(["Ctrl+L language", "Ctrl+R restart Austin", "Ctrl+Q quit"]);
    keys.join(" · ")
}

fn ready_mark(view: &View, agent: &str) -> &'static str {
    if view.ready.get(agent).copied().unwrap_or(false) {
        "signed"
    } else {
        "pending"
    }
}

fn mode_color(mode: &str) -> Rgba {
    match mode {
        "FAST" => rgb(0x6fc7d8),
        "STEP" => rgb(0x7ec97e),
        _ => rgb(0xd08cd8),
    }
}

fn speaker_color(agent: &str) -> Rgba {
    match agent {
        AGENT_AUSTIN => rgb(0x6fc7d8),
        AGENT_TONY => rgb(0xd08cd8),
        "duo" => rgb(0xe0c063),
        "human" => rgb(0x7ec97e),
        _ => rgb(0x8b93a5),
    }
}

fn event_speaker(event: &Event) -> (String, Rgba) {
    if !event.agent.is_empty() {
        return (event.agent.clone(), speaker_color(&event.agent));
    }
    match event.kind.as_str() {
        "error" => ("error".to_owned(), rgb(0xe06c6c)),
        "verdict" => ("verdict".to_owned(), rgb(0xd9a441)),
        "user" => ("human".to_owned(), rgb(0x7ec97e)),
        _ => ("duo".to_owned(), rgb(0xe0c063)),
    }
}
