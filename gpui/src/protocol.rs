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
    pub started: bool,
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
    /// Fast-mode verification bound to the reviewed commit.
    #[serde(default)]
    pub verification: Verification,
    /// Per-step verification and delivery evidence in queue order.
    #[serde(default)]
    pub step_evidence: Vec<StepVerification>,
    /// Driver diagnostics. They are state, not timeline entries: they are drawn
    /// from the last view, so repeating them is idempotent.
    #[serde(default)]
    pub notices: Vec<String>,
    #[serde(default)]
    pub agents: BTreeMap<String, Agent>,
    #[serde(default)]
    pub available_drivers: Vec<String>,
}

/// One agent's projection.
#[derive(Debug, Clone, Default, PartialEq, Deserialize)]
pub struct Agent {
    #[serde(default)]
    pub present: bool,
    #[serde(default)]
    pub worktree_path: String,
    #[serde(default)]
    pub worktree_branch: String,
    #[serde(default)]
    pub worktree_head: String,
    #[serde(default)]
    pub diffstat: Diffstat,
    #[serde(default)]
    pub diff_preview: DiffPreview,
    #[serde(default)]
    pub ready_note: String,
    #[serde(default)]
    pub ready_evidence: String,
    #[serde(default)]
    pub connected: bool,
    #[serde(default)]
    pub process: String,
    #[serde(default)]
    pub driver: String,
    #[serde(default)]
    pub model: String,
    #[serde(default)]
    pub live_model_switch: bool,
    #[serde(default)]
    pub thinking: String,
    #[serde(default)]
    pub busy: bool,
    #[serde(default)]
    pub activity: String,
    #[serde(default)]
    pub work: WorkPreview,
    #[serde(default)]
    pub models: Vec<Model>,
    #[serde(default)]
    pub models_loaded: bool,
    #[serde(default)]
    pub models_error: String,
    #[serde(default)]
    pub native_open: bool,
}

/// Git summary for an agent worktree. `state` is unavailable, loading, ready or
/// error; an empty state is accepted from older hosts.
#[derive(Debug, Clone, Default, PartialEq, Deserialize)]
pub struct Diffstat {
    #[serde(default)]
    pub state: String,
    #[serde(default)]
    pub files: usize,
    #[serde(default)]
    pub insertions: usize,
    #[serde(default)]
    pub deletions: usize,
    #[serde(default)]
    pub untracked: usize,
    #[serde(default)]
    pub error: String,
}

/// Bounded, read-only patch excerpt for an agent worktree.
#[derive(Debug, Clone, Default, PartialEq, Deserialize)]
pub struct DiffPreview {
    #[serde(default)]
    pub state: String,
    #[serde(default)]
    pub text: String,
    #[serde(default)]
    pub truncated: bool,
    #[serde(default)]
    pub error: String,
}

/// Structured verifier result, including the exact commit and note.
#[derive(Debug, Clone, Default, PartialEq, Deserialize)]
pub struct Verification {
    #[serde(default)]
    pub status: String,
    #[serde(default)]
    pub head: String,
    #[serde(default)]
    pub note: String,
    #[serde(default)]
    pub at: String,
}

/// Verification and delivery evidence for one Step-mode queue entry.
#[derive(Debug, Clone, Default, PartialEq, Deserialize)]
pub struct StepVerification {
    #[serde(default)]
    pub index: usize,
    #[serde(default)]
    pub title: String,
    #[serde(default)]
    pub done: bool,
    #[serde(default)]
    pub head: String,
    #[serde(default)]
    pub verification: Verification,
    #[serde(default)]
    pub delivery_head: String,
    #[serde(default)]
    pub delivered_at: String,
}

#[derive(Debug, Clone, Default, PartialEq, Deserialize)]
pub struct WorkPreview {
    #[serde(default)]
    pub provider_active: bool,
    #[serde(default)]
    pub tool_depth: usize,
    #[serde(default)]
    pub human_attached: bool,
    #[serde(default)]
    pub last_activity: String,
    #[serde(default)]
    pub tool: String,
    #[serde(default)]
    pub tool_detail: String,
    #[serde(default)]
    pub tool_failed: bool,
    #[serde(default)]
    pub stream_tail: String,
    #[serde(default)]
    pub last_error: String,
    #[serde(default)]
    pub context_tokens: usize,
    #[serde(default)]
    pub context_window: usize,
    #[serde(default)]
    pub tokens_per_second: f64,
    #[serde(default)]
    pub tools: usize,
    #[serde(default)]
    pub turn_started: String,
    #[serde(default)]
    pub turn_elapsed_ms: u64,
    #[serde(default)]
    pub tool_started: String,
    #[serde(default)]
    pub tool_elapsed_ms: u64,
    #[serde(default)]
    pub token_speed_at: String,
    #[serde(default)]
    pub failures: usize,
    #[serde(default)]
    pub recent: Vec<ToolNote>,
}

#[derive(Debug, Clone, Default, PartialEq, Deserialize)]
pub struct ToolNote {
    #[serde(default)]
    pub name: String,
    #[serde(default)]
    pub detail: String,
    #[serde(default)]
    pub ok: bool,
    #[serde(default)]
    pub duration_ms: u64,
}

#[derive(Debug, Clone, Default, PartialEq, Deserialize)]
pub struct Model {
    #[serde(default)]
    pub provider: String,
    #[serde(default)]
    pub id: String,
    #[serde(default)]
    pub reference: String,
    #[serde(default)]
    pub thinking: bool,
    #[serde(default)]
    pub images: bool,
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
    #[serde(default)]
    pub provider: String,
    #[serde(default)]
    pub model: String,
    #[serde(default)]
    pub thinking: String,
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
    #[serde(skip_serializing_if = "Option::is_none")]
    pub provider: Option<String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub model: Option<String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub driver: Option<String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub path: Option<String>,
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
            provider: None,
            model: None,
            driver: None,
            path: None,
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

    pub fn open_project(path: impl Into<String>) -> Self {
        Self {
            path: Some(path.into()),
            ..Self::new("open_project")
        }
    }

    pub fn set_driver(agent: impl Into<String>, driver: impl Into<String>) -> Self {
        Self {
            agent: Some(agent.into()),
            driver: Some(driver.into()),
            ..Self::new("set_driver")
        }
    }

    pub fn open_native(agent: impl Into<String>) -> Self {
        Self {
            agent: Some(agent.into()),
            ..Self::new("open_native")
        }
    }

    pub fn close_native(agent: impl Into<String>) -> Self {
        Self {
            agent: Some(agent.into()),
            ..Self::new("close_native")
        }
    }

    pub fn set_model(
        agent: impl Into<String>,
        provider: impl Into<String>,
        model: impl Into<String>,
    ) -> Self {
        Self {
            agent: Some(agent.into()),
            provider: Some(provider.into()),
            model: Some(model.into()),
            ..Self::new("set_model")
        }
    }

    pub fn cycle_thinking(agent: impl Into<String>) -> Self {
        Self {
            agent: Some(agent.into()),
            ..Self::new("cycle_thinking")
        }
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
            "started": true,
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
            "verification": {"status":"passed", "head":"abc123", "note":"checks passed", "at":"2026-10-16T10:37:37Z"},
            "step_evidence": [{"index":1, "title":"implement parser", "done":true, "head":"stephead",
                               "verification":{"status":"passed", "head":"stephead", "note":"reviewed"},
                               "delivery_head":"landedhead", "delivered_at":"2026-10-16T10:00:00Z"}],
            "notices": ["no sqlite3"],
            "agents": {
                "austin": {"present": true, "worktree_path":"/tmp/duo/austin", "worktree_branch":"duo/austin", "worktree_head":"aaa",
                           "diffstat":{"state":"ready", "files":3, "insertions":12, "deletions":4, "untracked":1},
                           "diff_preview":{"state":"ready", "text":"-before\n+after\n", "truncated":true},
                           "ready_note":"reviewed", "ready_evidence":"go test ./internal/guiipc",
                           "connected": true, "process": "running",
                           "driver": "pi", "model": "deepseek/deepseek-flash", "live_model_switch": true,
                           "thinking": "high", "busy": true, "activity": "Bash: make check",
                           "work": {"tool":"bash", "tool_detail":"make check", "context_tokens":123,
                                    "context_window":456, "tokens_per_second":7.5, "tools":2,
                                    "turn_started":"2026-10-16T10:37:37Z", "turn_elapsed_ms":2500,
                                    "tool_started":"2026-10-16T10:37:38Z", "tool_elapsed_ms":1500,
                                    "recent":[{"name":"read","detail":"src/main.rs","ok":true,"duration_ms":42}]},
                           "models":[{"provider":"p", "id":"m", "reference":"p/m", "thinking":true,"images":false}],
                           "models_loaded":true},
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
        assert!(view.started);
        assert_eq!(view.step_cursor, 1);
        assert_eq!(view.steps_total, 6);
        assert_eq!(view.notices, vec!["no sqlite3".to_owned()]);
        assert_eq!(view.ready.get("austin"), Some(&true));
        assert_eq!(view.ready.get("tony"), Some(&false));
        assert_eq!(view.verification.status, "passed");
        assert_eq!(view.verification.head, "abc123");
        assert_eq!(view.verification.at, "2026-10-16T10:37:37Z");
        assert_eq!(view.step_evidence.len(), 1);
        assert_eq!(view.step_evidence[0].index, 1);
        assert!(view.step_evidence[0].done);
        assert_eq!(view.step_evidence[0].verification.status, "passed");
        assert_eq!(view.step_evidence[0].delivery_head, "landedhead");
        // Identity on the wire is lowercase; a host that sent "Austin" would make
        // the renderer's lookups miss, which is why the protocol fixes the spelling.
        assert_eq!(view.agents.get("austin").unwrap().driver, "pi");
        let austin = view.agents.get("austin").unwrap();
        assert_eq!(austin.worktree_branch, "duo/austin");
        assert_eq!(austin.diffstat.state, "ready");
        assert_eq!(austin.diffstat.files, 3);
        assert_eq!(austin.diffstat.insertions, 12);
        assert_eq!(austin.diffstat.deletions, 4);
        assert_eq!(austin.diffstat.untracked, 1);
        assert_eq!(austin.diff_preview.state, "ready");
        assert!(austin.diff_preview.text.contains("+after"));
        assert!(austin.diff_preview.truncated);
        assert_eq!(austin.ready_note, "reviewed");
        assert_eq!(austin.ready_evidence, "go test ./internal/guiipc");
        assert!(austin.live_model_switch);
        assert_eq!(austin.work.context_tokens, 123);
        assert_eq!(austin.work.turn_elapsed_ms, 2500);
        assert_eq!(austin.work.tool_elapsed_ms, 1500);
        assert_eq!(austin.work.recent[0].duration_ms, 42);
        assert_eq!(austin.models[0].reference, "p/m");
        assert!(austin.models_loaded);
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
        assert_eq!(view.verification, Verification::default());
        assert!(view.step_evidence.is_empty());

        // This payload shape is what an older Go host sends: the existing agent
        // fields remain readable and all newly introduced values default cleanly.
        let old_host = r#"{"type":"view","view":{"mode":"fast","agents":{"austin":{"present":true,"worktree_head":"abc"}}}}"#;
        let ServerMessage::View { view } = serde_json::from_str(old_host).unwrap() else {
            panic!("an old-host view frame did not parse as a view");
        };
        let austin = view.agents.get("austin").unwrap();
        assert!(austin.present);
        assert_eq!(austin.worktree_head, "abc");
        assert_eq!(austin.diffstat, Diffstat::default());
        assert_eq!(austin.diff_preview, DiffPreview::default());
        assert!(austin.ready_note.is_empty());
        assert!(austin.ready_evidence.is_empty());
        assert!(!austin.live_model_switch);
        assert_eq!(austin.work.turn_elapsed_ms, 0);
        assert_eq!(austin.work.tool_elapsed_ms, 0);
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
            ServerMessage::Closed {
                text: "session ended".to_owned()
            }
        );
        let error = r#"{"type":"error","text":"submit_task needs text"}"#;
        assert_eq!(
            serde_json::from_str::<ServerMessage>(error).unwrap(),
            ServerMessage::Error {
                text: "submit_task needs text".to_owned()
            }
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
            (
                ClientMessage::set_model("tony", "p", "m"),
                r#"{"type":"command","kind":"set_model","agent":"tony","provider":"p","model":"m"}"#,
            ),
            (
                ClientMessage::cycle_thinking("austin"),
                r#"{"type":"command","kind":"cycle_thinking","agent":"austin"}"#,
            ),
            (ClientMessage::quit(), r#"{"type":"command","kind":"quit"}"#),
        ] {
            assert_eq!(serde_json::to_string(&message).unwrap(), want);
        }
    }
}
