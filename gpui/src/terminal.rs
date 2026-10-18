//! Terminal screen state for xduo's embedded PTY view.
//!
//! This is a screen parser, not a process or transport: the session host owns
//! the PTY and supplies its byte stream. Keeping byte processing here makes
//! parser behavior independently testable before it is attached to GPUI.

/// Default terminal dimensions used before the embedded view is measured.
pub const DEFAULT_TERMINAL_ROWS: u16 = 24;
pub const DEFAULT_TERMINAL_COLS: u16 = 80;

/// Bounds terminal screen allocation when a frontend resize is received.
const MAX_TERMINAL_DIMENSION: u16 = 512;
const SCROLLBACK_LINES: usize = 2_000;

/// VT100/xterm screen state fed by bytes read from an agent's PTY.
pub struct TerminalScreen {
    parser: vt100::Parser,
    rows: u16,
    cols: u16,
}

impl Default for TerminalScreen {
    fn default() -> Self {
        Self::new(DEFAULT_TERMINAL_ROWS, DEFAULT_TERMINAL_COLS)
    }
}

impl TerminalScreen {
    /// Creates an empty screen with the requested dimensions.
    pub fn new(rows: u16, cols: u16) -> Self {
        let (rows, cols) = bounded_size(rows, cols);
        Self {
            parser: vt100::Parser::new(rows, cols, SCROLLBACK_LINES),
            rows,
            cols,
        }
    }

    /// Applies a PTY output chunk. Parser state is retained across calls, so
    /// control sequences and multibyte text may span frame boundaries.
    pub fn process(&mut self, bytes: &[u8]) {
        self.parser.process(bytes);
    }

    /// Resizes the emulated screen, bounding dimensions before allocation.
    pub fn resize(&mut self, rows: u16, cols: u16) {
        let (rows, cols) = bounded_size(rows, cols);
        self.parser.screen_mut().set_size(rows, cols);
        self.rows = rows;
        self.cols = cols;
    }

    /// Current dimensions as (rows, columns).
    pub fn size(&self) -> (u16, u16) {
        (self.rows, self.cols)
    }

    /// Read-only access to parsed terminal state for rendering and cursor/mode
    /// inspection by the frontend.
    pub fn screen(&self) -> &vt100::Screen {
        self.parser.screen()
    }
}

fn bounded_size(rows: u16, cols: u16) -> (u16, u16) {
    (
        rows.clamp(1, MAX_TERMINAL_DIMENSION),
        cols.clamp(1, MAX_TERMINAL_DIMENSION),
    )
}

#[cfg(test)]
mod tests {
    use super::TerminalScreen;

    #[test]
    fn parses_cursor_movement_and_line_erasure() {
        let mut terminal = TerminalScreen::new(2, 12);
        terminal.process(b"hello\x1b[1;1H\x1b[2Kready");

        assert!(terminal
            .screen()
            .rows(0, 12)
            .next()
            .unwrap()
            .starts_with("ready"));
        assert_eq!(terminal.screen().cursor_position(), (0, 5));
    }

    #[test]
    fn preserves_parser_state_across_output_chunks() {
        let mut terminal = TerminalScreen::new(2, 12);
        terminal.process(b"\x1b[38;5;");
        terminal.process(b"196mR");

        assert_eq!(
            terminal.screen().cell(0, 0).unwrap().fgcolor(),
            vt100::Color::Idx(196)
        );

        terminal.process(&[0xe4]);
        terminal.process(&[0xbd, 0xa0]);
        assert_eq!(terminal.screen().cell(0, 1).unwrap().contents(), "你");
    }

    #[test]
    fn retains_scrollback_and_resizes_screen() {
        let mut terminal = TerminalScreen::new(2, 8);
        terminal.process(b"one\r\ntwo\r\nthree");
        assert!(terminal.screen().scrollback_len() > 0);

        terminal.resize(3, 10);
        assert_eq!(terminal.size(), (3, 10));
        assert_eq!(terminal.screen().size(), (3, 10));
    }

    #[test]
    fn parses_alternate_screen_modes() {
        let mut terminal = TerminalScreen::new(2, 8);
        terminal.process(b"normal\x1b[?1049halt");
        assert!(terminal.screen().alternate_screen());
        assert!(terminal
            .screen()
            .rows(0, 8)
            .next()
            .unwrap()
            .starts_with("alt"));

        terminal.process(b"\x1b[?1049l");
        assert!(!terminal.screen().alternate_screen());
        assert!(terminal
            .screen()
            .rows(0, 8)
            .next()
            .unwrap()
            .starts_with("normal"));
    }

    #[test]
    fn clamps_dimensions_before_allocating() {
        let mut terminal = TerminalScreen::new(0, u16::MAX);
        assert_eq!(terminal.size(), (1, 512));
        terminal.resize(u16::MAX, 0);
        assert_eq!(terminal.size(), (512, 1));
    }
}
