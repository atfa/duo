//! Duo's GPUI frontend. The process owns one app entity and can recreate its
//! window after macOS closes it while the Dock process remains alive.
mod app;
mod commands;
mod protocol;
mod timeline;

use app::DuoApp;
use gpui::{
    actions, px, size, AnyWindowHandle, App, AppContext, Application, Bounds, Entity, Focusable,
    TitlebarOptions, WindowBounds, WindowDecorations, WindowOptions,
};
#[cfg(target_os = "macos")]
use gpui::{KeyBinding, Menu, MenuItem, SystemMenuType};
use protocol::ServerMessage;
use smol::channel;
use std::{cell::RefCell, io::BufRead, rc::Rc};

actions!(xduo, [Quit]);

fn main() {
    let (tx, rx) = channel::unbounded::<ServerMessage>();
    read_stdin(tx);
    let app_entity: Rc<RefCell<Option<Entity<DuoApp>>>> = Rc::new(RefCell::new(None));
    let window_handle: Rc<RefCell<Option<AnyWindowHandle>>> = Rc::new(RefCell::new(None));
    let application = Application::new();
    #[cfg(target_os = "macos")]
    {
        let app_entity = app_entity.clone();
        let window_handle = window_handle.clone();
        application.on_reopen(move |cx| {
            if let Some(handle) = *window_handle.borrow() {
                if handle
                    .update(cx, |_, window, _| window.activate_window())
                    .is_ok()
                {
                    return;
                }
            }
            let Some(entity) = app_entity.borrow().clone() else {
                return;
            };
            *window_handle.borrow_mut() =
                Some(open_window(cx, Some(entity), None, app_entity.clone()));
        });
    }
    application.run({
        let app_entity = app_entity.clone();
        let window_handle = window_handle.clone();
        move |cx: &mut App| {
            cx.activate(true);
            #[cfg(target_os = "macos")]
            {
                let quit_entity = app_entity.clone();
                let quit_window = window_handle.clone();
                cx.bind_keys([KeyBinding::new("cmd-q", Quit, None)]);
                cx.on_action(move |_: &Quit, cx| {
                    let entity = quit_entity.borrow().clone();
                    if let Some(entity) = entity {
                        if entity.update(cx, |app, cx| app.confirm_application_quit(cx)) {
                            cx.quit();
                        } else if quit_window.borrow().is_some_and(|handle| {
                            handle
                                .update(cx, |_, window, _| window.activate_window())
                                .is_err()
                        }) {
                            *quit_window.borrow_mut() =
                                Some(open_window(cx, Some(entity), None, quit_entity.clone()));
                        }
                    }
                });
                cx.set_menus(vec![Menu {
                    name: "xduo".into(),
                    items: vec![
                        MenuItem::os_submenu("Services", SystemMenuType::Services),
                        MenuItem::separator(),
                        MenuItem::action("Quit xduo", Quit),
                    ],
                }]);
            }
            *window_handle.borrow_mut() = Some(open_window(cx, None, Some(rx), app_entity.clone()));
        }
    });
}

fn open_window(
    cx: &mut App,
    entity: Option<Entity<DuoApp>>,
    receiver: Option<channel::Receiver<ServerMessage>>,
    app_entity: Rc<RefCell<Option<Entity<DuoApp>>>>,
) -> AnyWindowHandle {
    let handle = cx
        .open_window(window_options(cx), move |window, cx| {
            let entity = entity.unwrap_or_else(|| {
                cx.new(|cx| DuoApp::new(receiver.expect("initial window receiver"), cx))
            });
            *app_entity.borrow_mut() = Some(entity.clone());
            #[cfg(not(target_os = "macos"))]
            let close_entity = entity.clone();
            window.on_window_should_close(cx, move |_, cx| {
                #[cfg(target_os = "macos")]
                {
                    let _ = cx;
                    true
                }
                #[cfg(not(target_os = "macos"))]
                {
                    close_entity.update(cx, |app, cx| app.on_window_close(cx))
                }
            });
            entity.update(cx, |_, cx| {
                cx.observe_window_appearance(window, |_, window, _| window.refresh())
                    .detach()
            });
            let focus = entity.read(cx).focus_handle(cx);
            window.focus(&focus);
            entity
        })
        .expect("open the Duo window");
    handle.into()
}

fn window_options(cx: &App) -> WindowOptions {
    let bounds = Bounds::centered(None, size(px(1180.), px(780.)), cx);
    WindowOptions {
        window_bounds: Some(WindowBounds::Windowed(bounds)),
        titlebar: Some(TitlebarOptions {
            title: Some("xduo".into()),
            ..Default::default()
        }),
        window_decorations: Some(if cfg!(target_os = "linux") {
            WindowDecorations::Client
        } else {
            WindowDecorations::Server
        }),
        is_resizable: true,
        is_minimizable: true,
        app_id: Some("xduo".into()),
        ..Default::default()
    }
}

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
