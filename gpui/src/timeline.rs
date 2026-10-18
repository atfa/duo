//! Small, deliberately limited Markdown parsing for timeline messages.
//!
//! This module only describes display structure. Callers should keep the
//! original message text for copying; parsing never rewrites the source value.

#[derive(Clone, Copy, Debug, Default, PartialEq, Eq)]
pub struct TextStyle {
    pub bold: bool,
    pub italic: bool,
    pub code: bool,
    pub link: bool,
}

#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Span {
    pub text: String,
    pub style: TextStyle,
}

#[derive(Clone, Debug, PartialEq, Eq)]
pub enum Block {
    /// Consecutive ordinary lines form one paragraph. Each line remains
    /// separate so the renderer can preserve explicit line breaks.
    Paragraph(Vec<Vec<Span>>),
    Heading {
        level: u8,
        spans: Vec<Span>,
    },
    ListItem(Vec<Span>),
    /// The contents of a fenced code block, without the fence markers.
    Code(String),
}

/// Return timeline indexes whose message text contains the query, ignoring case.
/// The caller bounds the source window; this helper does one pass and preserves
/// event order so the result can be navigated predictably.
#[cfg(test)]
pub fn search_matches<'a>(texts: impl IntoIterator<Item = &'a str>, query: &str) -> Vec<usize> {
    let query = query.trim();
    if query.is_empty() {
        return Vec::new();
    }
    let query = query.to_lowercase();
    texts
        .into_iter()
        .enumerate()
        .filter_map(|(index, text)| text.to_lowercase().contains(&query).then_some(index))
        .collect()
}

/// Parse the supported display subset: ATX headings, unordered list items,
/// fenced code blocks, and inline emphasis/code/links. Unsupported syntax and
/// raw HTML remain ordinary text. An unmatched marker is also kept verbatim.
pub fn parse_markdown(text: &str) -> Vec<Block> {
    let mut blocks = Vec::new();
    let mut paragraph = Vec::new();
    let mut code_lines = None::<(String, Vec<String>)>;

    for line in text.split('\n') {
        let line = line.strip_suffix('\r').unwrap_or(line);
        if let Some((_, lines)) = code_lines.as_mut() {
            if line.trim_start().starts_with("```") {
                let contents = std::mem::take(lines).join("\n");
                code_lines = None;
                blocks.push(Block::Code(contents));
            } else {
                lines.push(line.to_owned());
            }
            continue;
        }

        if line.trim().is_empty() {
            flush_paragraph(&mut blocks, &mut paragraph);
            continue;
        }

        let trimmed = line.trim_start();
        if trimmed.starts_with("```") {
            flush_paragraph(&mut blocks, &mut paragraph);
            code_lines = Some((line.to_owned(), Vec::new()));
            continue;
        }

        if let Some((level, content)) = heading(line) {
            flush_paragraph(&mut blocks, &mut paragraph);
            blocks.push(Block::Heading {
                level,
                spans: parse_inline(content),
            });
        } else if let Some(content) = list_item(line) {
            flush_paragraph(&mut blocks, &mut paragraph);
            blocks.push(Block::ListItem(parse_inline(content)));
        } else {
            paragraph.push(parse_inline(line));
        }
    }

    if let Some((opening, lines)) = code_lines {
        // An unterminated fence is ambiguous. Preserve its opening marker and
        // all following characters as plain text instead of silently hiding it.
        let mut literal = opening;
        if !lines.is_empty() {
            literal.push('\n');
            literal.push_str(&lines.join("\n"));
        }
        paragraph.push(vec![plain_span(literal)]);
    }
    flush_paragraph(&mut blocks, &mut paragraph);
    blocks
}

fn heading(line: &str) -> Option<(u8, &str)> {
    let trimmed = line.trim_start();
    let level = trimmed.bytes().take_while(|byte| *byte == b'#').count();
    if !(1..=6).contains(&level)
        || !trimmed
            .as_bytes()
            .get(level)
            .is_some_and(u8::is_ascii_whitespace)
    {
        return None;
    }
    let content = trimmed[level..].trim_start();
    Some((level as u8, content))
}

fn list_item(line: &str) -> Option<&str> {
    let trimmed = line.trim_start();
    let bytes = trimmed.as_bytes();
    if bytes.len() >= 2 && matches!(bytes[0], b'-' | b'+' | b'*') && bytes[1].is_ascii_whitespace()
    {
        Some(trimmed[2..].trim_start())
    } else if bytes.len() >= 3
        && bytes[0].is_ascii_digit()
        && bytes[1] == b'.'
        && bytes[2].is_ascii_whitespace()
    {
        Some(trimmed[3..].trim_start())
    } else {
        None
    }
}

fn flush_paragraph(blocks: &mut Vec<Block>, paragraph: &mut Vec<Vec<Span>>) {
    if !paragraph.is_empty() {
        blocks.push(Block::Paragraph(std::mem::take(paragraph)));
    }
}

fn parse_inline(text: &str) -> Vec<Span> {
    let mut spans = Vec::new();
    let mut plain = String::new();
    let mut rest = text;

    while !rest.is_empty() {
        if let Some((consumed, content, style)) = inline_token(rest) {
            flush_plain(&mut spans, &mut plain);
            spans.push(Span {
                text: content.to_owned(),
                style,
            });
            rest = &rest[consumed..];
        } else {
            if rest.starts_with("**") && !rest[2..].contains("**") {
                plain.push_str(rest);
                break;
            }
            if (rest.starts_with('*') && !rest.starts_with("**") && !rest[1..].contains('*'))
                || (rest.starts_with('_') && !rest[1..].contains('_'))
                || (rest.starts_with('`') && !rest[1..].contains('`'))
                || (rest.starts_with('[') && !rest.contains("]("))
            {
                plain.push_str(rest);
                break;
            }
            let ch = rest.chars().next().expect("rest is non-empty");
            plain.push(ch);
            rest = &rest[ch.len_utf8()..];
        }
    }
    flush_plain(&mut spans, &mut plain);
    spans
}

fn inline_token(rest: &str) -> Option<(usize, &str, TextStyle)> {
    if let Some(after_open) = rest.strip_prefix("**") {
        if let Some(end) = after_open.find("**") {
            let content = &after_open[..end];
            if !content.is_empty() {
                return Some((
                    2 + end + 2,
                    content,
                    TextStyle {
                        bold: true,
                        ..Default::default()
                    },
                ));
            }
        }
    }

    for (marker, style) in [
        (
            "*",
            TextStyle {
                italic: true,
                ..Default::default()
            },
        ),
        (
            "_",
            TextStyle {
                italic: true,
                ..Default::default()
            },
        ),
        (
            "`",
            TextStyle {
                code: true,
                ..Default::default()
            },
        ),
    ] {
        if let Some(after_open) = rest.strip_prefix(marker) {
            if let Some(end) = after_open.find(marker) {
                let content = &after_open[..end];
                if !content.is_empty() {
                    return Some((marker.len() + end + marker.len(), content, style));
                }
            }
        }
    }

    if let Some(after_open) = rest.strip_prefix('[') {
        if let Some(label_end) = after_open.find("](") {
            if let Some(url_end) = after_open[label_end + 2..].find(')') {
                let label = &after_open[..label_end];
                if !label.is_empty() {
                    let consumed = 1 + label_end + 2 + url_end + 1;
                    return Some((
                        consumed,
                        label,
                        TextStyle {
                            link: true,
                            ..Default::default()
                        },
                    ));
                }
            }
        }
    }
    None
}

fn flush_plain(spans: &mut Vec<Span>, plain: &mut String) {
    if !plain.is_empty() {
        spans.push(plain_span(std::mem::take(plain)));
    }
}

fn plain_span(text: String) -> Span {
    Span {
        text,
        style: TextStyle::default(),
    }
}

#[cfg(test)]
mod tests {
    use super::{parse_markdown, search_matches, Block, TextStyle};

    fn displayed(blocks: &[Block]) -> String {
        let mut output = String::new();
        for (index, block) in blocks.iter().enumerate() {
            if index > 0 {
                output.push('\n');
            }
            match block {
                Block::Paragraph(lines) => {
                    for (line_index, line) in lines.iter().enumerate() {
                        if line_index > 0 {
                            output.push('\n');
                        }
                        output.push_str(
                            &line
                                .iter()
                                .map(|span| span.text.as_str())
                                .collect::<String>(),
                        );
                    }
                }
                Block::Heading { spans, .. } | Block::ListItem(spans) => {
                    output.push_str(
                        &spans
                            .iter()
                            .map(|span| span.text.as_str())
                            .collect::<String>(),
                    );
                }
                Block::Code(code) => output.push_str(code),
            }
        }
        output
    }

    #[test]
    fn parses_supported_blocks_and_inline_styles() {
        let blocks = parse_markdown("# 标题\n\n- **粗体** and *斜体* with `code`\n1. [链接](https://example.test)\n```\nlet x = 1;\n```");
        assert!(matches!(&blocks[0], Block::Heading { level: 1, .. }));
        assert!(matches!(&blocks[1], Block::ListItem(_)));
        assert!(matches!(&blocks[2], Block::ListItem(_)));
        assert!(matches!(&blocks[3], Block::Code(code) if code == "let x = 1;"));
        let spans = match &blocks[1] {
            Block::ListItem(spans) => spans,
            _ => unreachable!(),
        };
        assert!(spans
            .iter()
            .any(|span| span.text == "粗体" && span.style.bold));
        assert!(spans
            .iter()
            .any(|span| span.text == "斜体" && span.style.italic));
        assert!(spans
            .iter()
            .any(|span| span.text == "code" && span.style.code));
        let link = match &blocks[2] {
            Block::ListItem(spans) => spans.iter().find(|span| span.style.link).unwrap(),
            _ => unreachable!(),
        };
        assert_eq!(link.text, "链接");
    }

    #[test]
    fn keeps_html_unknown_syntax_and_unclosed_markers_as_plain_text() {
        let source = "<b>HTML</b> ~~strike~~ **unclosed *also open `x";
        let blocks = parse_markdown(source);
        assert_eq!(displayed(&blocks), source);
        assert!(blocks.iter().all(|block| match block {
            Block::Paragraph(lines) => lines
                .iter()
                .flatten()
                .all(|span| span.style == TextStyle::default()),
            _ => false,
        }));
    }

    #[test]
    fn unterminated_code_fence_is_preserved_verbatim() {
        let source = "before\n```rust\nlet 界 = `x`;";
        assert_eq!(displayed(&parse_markdown(source)), source);
    }

    #[test]
    fn preserves_unicode_and_explicit_line_breaks() {
        let source = "one 界🙂\ntwo\n\nthree";
        assert_eq!(displayed(&parse_markdown(source)), "one 界🙂\ntwo\nthree");
    }

    #[test]
    fn search_is_case_insensitive_ordered_and_handles_unicode() {
        let events = ["first", "HELLO world", "界🙂", "hello again"];
        assert_eq!(search_matches(events.iter().copied(), "hello"), vec![1, 3]);
        assert_eq!(search_matches(events.iter().copied(), "界🙂"), vec![2]);
        assert!(search_matches(events.iter().copied(), "  ").is_empty());
    }

    #[test]
    fn search_scans_a_bounded_timeline_without_reordering_matches() {
        let events: Vec<_> = (0..500)
            .map(|index| if index % 97 == 0 { "needle" } else { "other" })
            .collect();
        assert_eq!(
            search_matches(events.iter().copied(), "needle"),
            vec![0, 97, 194, 291, 388, 485]
        );
    }
}
