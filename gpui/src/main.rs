//! Duo's GPUI frontend (MVP).
//!
//! It is a renderer, not a session. It reads NDJSON frames from stdin, draws one
//! window, and writes the commands a human pressed to stdout. Everything about the
//! workflow lives in the Go host that started it; the contract between the two is
//! `docs/gui-protocol.md`.

mod app;
mod protocol;

use std::io::BufRead;

use gpui::{
    px, size, App, AppContext, Application, Bounds, Focusable, TitlebarOptions, WindowBounds,
    WindowDecorations, WindowOptions,
};
use smol::channel;

use app::DuoApp;
use protocol::ServerMessage;

fn main() {
    let (tx, rx) = channel::unbounded::<ServerMessage>();
    read_stdin(tx);

    Application::new().run(move |cx: &mut App| {
        let bounds = Bounds::centered(None, size(px(1180.), px(780.)), cx);
        let options = WindowOptions {
            window_bounds: Some(WindowBounds::Windowed(bounds)),
            titlebar: Some(TitlebarOptions {
                title: Some("xduo".into()),
                ..Default::default()
            }),
            // Keep system-drawn controls and resize borders on macOS and ask the
            // Linux compositor/window manager for server-side decorations. This
            // lets GNOME, KDE, Xfce and other desktops use their own titlebar
            // layout and button style instead of relying on client-side chrome.
            window_decorations: Some(WindowDecorations::Server),
            is_resizable: true,
            is_minimizable: true,
            app_id: Some("xduo".into()),
            ..Default::default()
        };

        cx.open_window(options, move |window, cx| {
            let app = cx.new(|cx| {
                cx.observe_window_appearance(window, |_, window, _| window.refresh())
                    .detach();
                DuoApp::new(rx, cx)
            });
            let focus = app.read(cx).focus_handle(cx);
            window.focus(&focus);
            app
        })
        .expect("open the Duo window");
    });
}

/// Reads the host's frames on a thread of its own: the window loop must never block
/// on stdin. An unparsable line is reported on stderr and skipped — stderr is not
/// part of the protocol, and this is the only direction where a bad frame can be
/// neither shown to the human nor answered.
fn read_stdin(tx: channel::Sender<ServerMessage>) {
    std::thread::spawn(move || {
        let stdin = std::io::stdin();
        for line in stdin.lock().lines() {
            let Ok(line) = line else { break };
            if line.trim().is_empty() {
                continue;
            }
            match serde_json::from_str::<ServerMessage>(&line) {
                Ok(message) => {
                    if tx.send_blocking(message).is_err() {
                        break;
                    }
                }
                Err(err) => eprintln!("xduo: ignoring an unparsable frame: {err}"),
            }
        }
        let _ = tx.send_blocking(ServerMessage::Closed {
            text: "Duo host disconnected.".to_owned(),
        });
    });
}
