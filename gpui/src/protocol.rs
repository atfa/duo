//! The wire contract with Duo Core, as specified by `docs/gui-protocol.md`.
//!
//! Nothing here knows about a session, a workflow or a window. These are the
//! frames, kept a direct transcription of the protocol document so the Go side and
//! this side cannot drift into two shapes.

use serde::{Deserialize, Serialize};
use std::collections::BTreeMap;

/// One frame from the session host, read from stdin.
#[derive(Debug, Clone, PartialEq, Deserialize)]
#[serde(tag = "type", rename_all = "snake_case")]
pub enum ServerMessage {
    View { view: View },
    Event { event: Event },
    Error { text: String },
    Closed { text: String },
}

/// The complete state of a session at one moment.
///
/// Every field defaults, so a field the host adds or omits is not a parse error:
/// the protocol ignores unknown fields and tolerates an absent one.
#[derive(Debug, Clone, Default, PartialEq, Deserialize)]
pub struct View {
    #[serde(default)]
    pub version: String,
    #[serde(default)]
    pub session: String,
    #[serde(default)]
    pub mode: String,
    #[serde(default)]
    pub phase: String,
    #[serde(default)]
    pub language: String,
    #[serde(default)]
    pub repository: String,
    #[serde(default)]
    pub base_branch: String,
    #[serde(default)]
    pub base_commit: String,
    #[serde(default)]
    pub status: String,
    #[serde(default)]
    pub plan_version: u64,
    #[serde(default)]
    pub plan: String,
    /// Keyed by the lowercase agent id (`austin`, `tony`); the host normalizes the
    /// spelling, so this side never re-cases a value.
    #[serde(default)]
    pub ready: BTreeMap<String, bool>,
    #[serde(default)]
    pub steps_total: usize,
    #[serde(default)]
    pub step_cursor: usize,
    #[serde(default)]
    pub delivery: String,
    /// Driver diagnostics. They are state, not timeline entries: they are drawn
    /// from the last view, so repeating them is idempotent.
    #[serde(default)]
    pub notices: Vec<String>,
    #[serde(default)]
    pub agents: BTreeMap<String, Agent>,
}

/// One agent's projection.
#[derive(Debug, Clone, Default, PartialEq, Deserialize)]
pub struct Agent {
    #[serde(default)]
    pub present: bool,
    #[serde(default)]
    pub connected: bool,
    #[serde(default)]
    pub process: String,
    #[serde(default)]
    pub driver: String,
    #[serde(default)]
    pub model: String,
    #[serde(default)]
    pub thinking: String,
    #[serde(default)]
    pub busy: bool,
    #[serde(default)]
    pub activity: String,
}

/// One timeline entry.
#[derive(Debug, Clone, Default, PartialEq, Deserialize)]
pub struct Event {
    #[serde(default)]
    pub time: String,
    #[serde(default)]
    pub kind: String,
    #[serde(default)]
    pub agent: String,
    #[serde(default)]
    pub peer: String,
    #[serde(default)]
    pub text: String,
    /// The timeline header, e.g. `Austin → Tony`. Empty means "use the speaker".
    #[serde(default)]
    pub label: String,
}

/// One command from the renderer, written to stdout.
#[derive(Debug, Clone, PartialEq, Serialize)]
pub struct ClientMessage {
    #[serde(rename = "type")]
    pub message_type: &'static str,
    pub kind: &'static str,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub text: Option<String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub agent: Option<String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub language: Option<String>,
}

impl ClientMessage {
    const TYPE: &'static str = "command";

    fn new(kind: &'static str) -> Self {
        Self {
            message_type: Self::TYPE,
            kind,
            text: None,
            agent: None,
            language: None,
        }
    }

    pub fn submit_task(text: impl Into<String>) -> Self {
        Self {
            text: Some(text.into()),
            ..Self::new("submit_task")
        }
    }

    pub fn escalate_to_goal() -> Self {
        Self::new("escalate_to_goal")
    }

    pub fn restart_agent(agent: impl Into<String>) -> Self {
        Self {
            agent: Some(agent.into()),
            ..Self::new("restart_agent")
        }
    }

    pub fn set_language(language: impl Into<String>) -> Self {
        Self {
            language: Some(language.into()),
            ..Self::new("set_language")
        }
    }

    pub fn quit() -> Self {
        Self::new("quit")
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    /// A frame exactly as the Go host writes it, including the fields serde would
    /// otherwise have to guess at.
    const VIEW_FRAME: &str = r##"{
        "type": "view",
        "view": {
            "version": "v0.11.0",
            "session": "20261016-103737-393c1e00",
            "mode": "step",
            "phase": "EXECUTE",
            "language": "zh",
            "repository": "/home/atfa/ai/Projects/duo",
            "base_branch": "main",
            "base_commit": "d434ddbfc6",
            "status": "phase EXECUTE",
            "plan_version": 4,
            "plan": "# plan",
            "ready": {"austin": true, "tony": false},
            "steps_total": 6,
            "step_cursor": 1,
            "delivery": "",
            "notices": ["no sqlite3"],
            "agents": {
                "austin": {"present": true, "connected": true, "process": "running",
                           "driver": "pi", "model": "deepseek/deepseek-flash",
                           "thinking": "high", "busy": true, "activity": "Bash: make check"},
                "tony": {"present": true, "connected": false, "process": "running",
                         "driver": "pi", "model": "", "thinking": "", "busy": false, "activity": ""}
            }
        }
    }"##;

    #[test]
    fn parses_a_full_view_frame() {
        let ServerMessage::View { view } = serde_json::from_str(VIEW_FRAME).unwrap() else {
            panic!("a view frame did not parse as a view");
        };
        assert_eq!(view.version, "v0.11.0");
        assert_eq!(view.mode, "step");
        assert_eq!(view.step_cursor, 1);
        assert_eq!(view.steps_total, 6);
        assert_eq!(view.notices, vec!["no sqlite3".to_owned()]);
        assert_eq!(view.ready.get("austin"), Some(&true));
        assert_eq!(view.ready.get("tony"), Some(&false));
        // Identity on the wire is lowercase; a host that sent "Austin" would make
        // the renderer's lookups miss, which is why the protocol fixes the spelling.
        assert_eq!(view.agents.get("austin").unwrap().driver, "pi");
        assert!(view.agents.get("Austin").is_none());
    }

    #[test]
    fn tolerate_unknown_and_missing_fields() {
        let frame = r#"{"type":"view","view":{"mode":"fast","future_field":42}}"#;
        let ServerMessage::View { view } = serde_json::from_str(frame).unwrap() else {
            panic!("a view frame did not parse as a view");
        };
        assert_eq!(view.mode, "fast");
        assert!(view.agents.is_empty());
        assert!(view.notices.is_empty());
    }

    #[test]
    fn parses_event_and_lifecycle_frames() {
        let event = r#"{"type":"event","event":{"time":"2026-10-16T02:51:08Z","kind":"peer",
            "agent":"austin","peer":"tony","text":"hello","label":"Austin → Tony"}}"#;
        let ServerMessage::Event { event } = serde_json::from_str(event).unwrap() else {
            panic!("an event frame did not parse as an event");
        };
        assert_eq!(event.agent, "austin");
        assert_eq!(event.label, "Austin → Tony");

        let closed = r#"{"type":"closed","text":"session ended"}"#;
        assert_eq!(
            serde_json::from_str::<ServerMessage>(closed).unwrap(),
            ServerMessage::Closed { text: "session ended".to_owned() }
        );
        let error = r#"{"type":"error","text":"submit_task needs text"}"#;
        assert_eq!(
            serde_json::from_str::<ServerMessage>(error).unwrap(),
            ServerMessage::Error { text: "submit_task needs text".to_owned() }
        );
    }

    #[test]
    fn a_command_frame_carries_only_what_it_needs() {
        // The whole command surface, compared as exact bytes: the string form is also
        // what proves the optional fields are omitted rather than sent as null.
        for (message, want) in [
            (
                ClientMessage::submit_task("ship it"),
                r#"{"type":"command","kind":"submit_task","text":"ship it"}"#,
            ),
            (
                ClientMessage::escalate_to_goal(),
                r#"{"type":"command","kind":"escalate_to_goal"}"#,
            ),
            (
                ClientMessage::restart_agent("austin"),
                r#"{"type":"command","kind":"restart_agent","agent":"austin"}"#,
            ),
            (
                ClientMessage::set_language("zh"),
                r#"{"type":"command","kind":"set_language","language":"zh"}"#,
            ),
            (ClientMessage::quit(), r#"{"type":"command","kind":"quit"}"#),
        ] {
            assert_eq!(serde_json::to_string(&message).unwrap(), want);
        }
    }
}
