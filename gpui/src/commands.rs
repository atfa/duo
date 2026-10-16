//! Searchable keyboard help and slash-command metadata for the GUI.
//!
//! This module is deliberately independent of GPUI and the wire protocol. The
//! renderer can use the returned action to update local UI state or send the
//! corresponding `ClientMessage`.

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct HelpEntry {
    pub shortcut: &'static str,
    pub description: &'static str,
    /// Search terms include the action name and common wording not shown in the UI.
    pub keywords: &'static str,
    pub requires_fast: bool,
}

pub static HELP_ENTRIES: &[HelpEntry] = &[
    HelpEntry {
        shortcut: "Enter",
        description: "Send task",
        keywords: "submit send task composer",
        requires_fast: false,
    },
    HelpEntry {
        shortcut: "Shift+Enter",
        description: "Insert newline",
        keywords: "multiline newline line break",
        requires_fast: false,
    },
    HelpEntry {
        shortcut: "Escape",
        description: "Clear composer or close help",
        keywords: "clear cancel dismiss",
        requires_fast: false,
    },
    HelpEntry {
        shortcut: "Ctrl+Q",
        description: "Quit Duo",
        keywords: "exit close quit",
        requires_fast: false,
    },
    HelpEntry {
        shortcut: "Ctrl+G",
        description: "Escalate Fast session to Goal",
        keywords: "goal escalate mode",
        requires_fast: true,
    },
    HelpEntry {
        shortcut: "Ctrl+T",
        description: "Toggle timestamps",
        keywords: "time timestamp messages",
        requires_fast: false,
    },
    HelpEntry {
        shortcut: "Ctrl+P",
        description: "Toggle work preview",
        keywords: "preview tools activity",
        requires_fast: false,
    },
    HelpEntry {
        shortcut: "Ctrl+O",
        description: "Toggle session overview",
        keywords: "overview session diffstat status",
        requires_fast: false,
    },
    HelpEntry {
        shortcut: "Ctrl+M",
        description: "Choose Austin model",
        keywords: "model picker austin",
        requires_fast: false,
    },
    HelpEntry {
        shortcut: "Ctrl+Shift+M",
        description: "Choose Tony model",
        keywords: "model picker tony",
        requires_fast: false,
    },
    HelpEntry {
        shortcut: "Ctrl+Alt+T",
        description: "Cycle Austin thinking level",
        keywords: "thinking reasoning austin",
        requires_fast: false,
    },
    HelpEntry {
        shortcut: "Ctrl+Shift+T",
        description: "Cycle Tony thinking level",
        keywords: "thinking reasoning tony",
        requires_fast: false,
    },
    HelpEntry {
        shortcut: "Ctrl+R",
        description: "Restart Austin",
        keywords: "restart agent austin",
        requires_fast: false,
    },
    HelpEntry {
        shortcut: "Ctrl+Y",
        description: "Restart Tony",
        keywords: "restart agent tony",
        requires_fast: false,
    },
    HelpEntry {
        shortcut: "Ctrl+X",
        description: "Copy Austin's latest message",
        keywords: "copy clipboard austin latest",
        requires_fast: false,
    },
    HelpEntry {
        shortcut: "Ctrl+L",
        description: "Toggle Duo language",
        keywords: "language locale zh en",
        requires_fast: false,
    },
    HelpEntry {
        shortcut: "F1 / Ctrl+H",
        description: "Toggle keyboard help",
        keywords: "help keyboard shortcuts",
        requires_fast: false,
    },
    HelpEntry {
        shortcut: "Click, arrows, PageUp/Down, Home/End",
        description: "Select and navigate messages",
        keywords: "timeline history message selection",
        requires_fast: false,
    },
    HelpEntry {
        shortcut: "Ctrl+C",
        description: "Copy selected message",
        keywords: "timeline clipboard copy",
        requires_fast: false,
    },
];

/// Return help rows whose visible text or search keywords contain `query`.
/// Matching is Unicode lowercase based and insensitive to ASCII case.
pub fn search_help(query: &str, is_fast: bool) -> Vec<&'static HelpEntry> {
    let query = query.trim().to_lowercase();
    HELP_ENTRIES
        .iter()
        .filter(|entry| {
            (is_fast || !entry.requires_fast)
                && (query.is_empty()
                    || entry.shortcut.to_lowercase().contains(&query)
                    || entry.description.to_lowercase().contains(&query)
                    || entry.keywords.to_lowercase().contains(&query))
        })
        .collect()
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum SlashAction {
    EscalateToGoal,
    RestartAustin,
    RestartTony,
    ToggleOverview,
    ToggleWorkPreview,
    ToggleTimestamps,
    ChooseAustinModel,
    ChooseTonyModel,
    CycleAustinThinking,
    CycleTonyThinking,
    ToggleLanguage,
    CopyLatestAustin,
    ToggleHelp,
    Quit,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct SlashCommand {
    /// Canonical name, including the leading slash.
    pub name: &'static str,
    pub description: &'static str,
    pub action: SlashAction,
}

pub static SLASH_COMMANDS: &[SlashCommand] = &[
    SlashCommand {
        name: "/goal",
        description: "Escalate a Fast session to Goal",
        action: SlashAction::EscalateToGoal,
    },
    SlashCommand {
        name: "/restart-austin",
        description: "Restart Austin",
        action: SlashAction::RestartAustin,
    },
    SlashCommand {
        name: "/restart-tony",
        description: "Restart Tony",
        action: SlashAction::RestartTony,
    },
    SlashCommand {
        name: "/overview",
        description: "Toggle session overview",
        action: SlashAction::ToggleOverview,
    },
    SlashCommand {
        name: "/preview",
        description: "Toggle work preview",
        action: SlashAction::ToggleWorkPreview,
    },
    SlashCommand {
        name: "/timestamps",
        description: "Toggle message timestamps",
        action: SlashAction::ToggleTimestamps,
    },
    SlashCommand {
        name: "/model-austin",
        description: "Open Austin model picker",
        action: SlashAction::ChooseAustinModel,
    },
    SlashCommand {
        name: "/model-tony",
        description: "Open Tony model picker",
        action: SlashAction::ChooseTonyModel,
    },
    SlashCommand {
        name: "/thinking-austin",
        description: "Cycle Austin thinking level",
        action: SlashAction::CycleAustinThinking,
    },
    SlashCommand {
        name: "/thinking-tony",
        description: "Cycle Tony thinking level",
        action: SlashAction::CycleTonyThinking,
    },
    SlashCommand {
        name: "/language",
        description: "Toggle Duo language",
        action: SlashAction::ToggleLanguage,
    },
    SlashCommand {
        name: "/copy",
        description: "Copy Austin's latest message",
        action: SlashAction::CopyLatestAustin,
    },
    SlashCommand {
        name: "/help",
        description: "Toggle keyboard help",
        action: SlashAction::ToggleHelp,
    },
    SlashCommand {
        name: "/quit",
        description: "Quit Duo",
        action: SlashAction::Quit,
    },
];

/// A known slash command is returned for execution only after submission. The
/// command palette should use `slash_completions` while the user is typing.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Submission<'a> {
    Task(&'a str),
    Command {
        command: &'static SlashCommand,
        arguments: &'a str,
    },
}

/// Return known commands matching a slash token at the start of the composer.
/// Leading horizontal whitespace is allowed. Double slash and path-like tokens
/// (for example `/usr/bin/tool`) are left as ordinary task text.
pub fn slash_completions(input: &str, is_fast: bool) -> Vec<&'static SlashCommand> {
    let Some(token) = slash_token(input) else {
        return Vec::new();
    };
    let composer = input.trim_start_matches(|ch| ch == ' ' || ch == '\t');
    let token_end = composer.find(char::is_whitespace).unwrap_or(composer.len());
    if !composer[token_end..].trim().is_empty() {
        return Vec::new();
    }
    let needle = token.to_lowercase();
    SLASH_COMMANDS
        .iter()
        .filter(|command| {
            command.name.to_lowercase().starts_with(&needle)
                && (is_fast || command.action != SlashAction::EscalateToGoal)
        })
        .collect()
}

/// Classify submitted text. Only an exact, known first token is a command;
/// unknown slash text remains a task and can be sent to the host unchanged.
pub fn classify_submission(input: &str, is_fast: bool) -> Submission<'_> {
    let Some((token, arguments)) = command_parts(input) else {
        return Submission::Task(input);
    };
    let Some(command) = SLASH_COMMANDS
        .iter()
        .find(|command| command.name.eq_ignore_ascii_case(token))
    else {
        return Submission::Task(input);
    };
    // Every current GUI action is argument-free. Keeping argument-bearing text as
    // a task avoids silently dropping input (for example `/language zh`).
    if !arguments.is_empty() {
        return Submission::Task(input);
    }
    if command.action == SlashAction::EscalateToGoal && !is_fast {
        return Submission::Task(input);
    }
    Submission::Command { command, arguments }
}

fn slash_token(input: &str) -> Option<&str> {
    let input = input.trim_start_matches(|ch| ch == ' ' || ch == '\t');
    if !input.starts_with('/') || input.starts_with("//") {
        return None;
    }
    let token_end = input.find(char::is_whitespace).unwrap_or(input.len());
    let token = &input[..token_end];
    if token.len() > 1 && token[1..].contains('/') {
        return None;
    }
    Some(token)
}

fn command_parts(input: &str) -> Option<(&str, &str)> {
    let input = input.trim_start_matches(|ch| ch == ' ' || ch == '\t');
    let token_end = input.find(char::is_whitespace).unwrap_or(input.len());
    let token = &input[..token_end];
    if slash_token(input)? != token {
        return None;
    }
    Some((token, input[token_end..].trim()))
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn help_search_matches_visible_text_and_hidden_terms_case_insensitively() {
        let rows = search_help("AuStIn", false);
        assert!(rows.iter().any(|row| row.shortcut == "Ctrl+R"));
        assert!(rows.iter().any(|row| row.shortcut == "Ctrl+M"));
        assert!(!search_help("   ", false).is_empty());
        assert!(search_help("no such action", false).is_empty());
        assert!(search_help("goal", false).is_empty());
        assert_eq!(search_help("goal", true).len(), 1);
    }

    #[test]
    fn slash_completions_are_prefix_only_and_exclude_paths_and_double_slash() {
        assert_eq!(slash_completions("/ov", false)[0].name, "/overview");
        assert!(slash_completions("//overview", true).is_empty());
        assert!(slash_completions("/usr/bin/tool", true).is_empty());
        assert!(slash_completions("please /ov", true).is_empty());
        assert!(slash_completions("/language zh", true).is_empty());
        assert_eq!(slash_completions("  /GO", true)[0].name, "/goal");
        assert!(slash_completions("/go", false).is_empty());
    }

    #[test]
    fn completion_does_not_execute_and_unknown_slash_text_stays_a_task() {
        assert_eq!(slash_completions("/overview", false).len(), 1);
        let Submission::Command { command, arguments } = classify_submission("/OVERVIEW", false)
        else {
            panic!("known command was not recognized");
        };
        assert_eq!(command.action, SlashAction::ToggleOverview);
        assert_eq!(arguments, "");
        assert_eq!(
            classify_submission("/invented action", false),
            Submission::Task("/invented action")
        );
        assert_eq!(
            classify_submission("/usr/bin/tool", false),
            Submission::Task("/usr/bin/tool")
        );
        assert_eq!(
            classify_submission("// comment", false),
            Submission::Task("// comment")
        );
        assert_eq!(
            classify_submission("/goal", false),
            Submission::Task("/goal")
        );
        assert!(
            matches!(classify_submission("/goal", true), Submission::Command { command, .. } if command.action == SlashAction::EscalateToGoal)
        );
    }
}
