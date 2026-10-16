# Duo's GPUI frontend (MVP)

`xduo` is the graphical frontend: one GPU-rendered window over a Duo session,
built with [GPUI](https://gpui.rs), the Rust UI framework Zed uses. It is an MVP
on purpose — the smallest window that is genuinely usable rather than a copy of
the terminal interface.

It is a **renderer**, not a session. It reads NDJSON frames from stdin, draws
them, and writes the commands a human pressed to stdout. Everything about the
workflow lives in the Go host that starts it, and the contract between the two is
[docs/gui-protocol.md](../docs/gui-protocol.md).

## Build

From the repository root:

```sh
make gpui
```

or directly:

```sh
cd gpui && cargo build --release
```

`duo gui` resolves the renderer in one order: `--gui-binary <path>`, then
`DUO_GPUI_BIN`, then `xduo` on `PATH`, then an `xduo` next to the `duo`
binary itself.

The window follows the system light/dark appearance and updates when it changes.
It keeps the platform titlebar and native resize borders. macOS draws its
standard close, minimize and zoom controls. Build a macOS `.app` with
`scripts/package-xduo-macos.sh <output.app>` to install the xduo Dock icon; run
`duo gui` with `DUO_GPUI_BIN` pointing to the app's `Contents/MacOS/xduo`.
On Linux, xduo requests server-side decorations so the desktop's window manager
supplies its own buttons and borders. `app_id` is `xduo`; install the matching
icon and launcher with `scripts/install-xduo-desktop.sh` (the Duo CLI must be on
PATH when installing). The d² mark also appears in the window content.

The build tree is `gpui/.target` rather than cargo's default `target`, which
`gpui/.cargo/config.toml` sets. This repository's Go tooling walks `./...` from
the root, and a walk that touches a Cargo target directory while cargo is
rewriting it fails `go test ./...` with "no such file or directory". Go skips a
directory whose name begins with a dot, so the Rust tree lives in one.

### The pinned `libc`

`Cargo.toml` pins `libc = "=0.2.179"`, and the pin is not decoration. `gpui`'s
transitive `xattr 0.2.3` (through `zed-async-tar` and `gpui_http_client`) reads
`libc::ENOATTR`, a constant libc stopped defining in 0.2.180, so without the pin
the crate does not build at all on a current toolchain:

```text
error[E0425]: cannot find value `ENOATTR` in crate `::libc`
```

`Cargo.lock` is committed for the same reason: `gpui` is pre-1.0 and its
dependency tree moves, and a build that worked yesterday should still work
tomorrow.

## Run

```sh
duo gui                       # a new Fast session, in the window
duo gui --mode goal           # every other session flag works the same way
duo gui resume <session-id>   # open the window on the session already in flight
```

## Keys

| Key | Action |
| --- | --- |
| `Enter` | Send the typed task through Duo. |
| `Shift+Enter` | Insert a newline in the task. |
| `Esc` | Clear the input. |
| `Ctrl+G` | Ask for Goal mode. Fast sessions only; the host refuses it elsewhere. |
| `Ctrl+L` | Switch the language Duo writes in. |
| `Ctrl+R` | Restart Austin's process. |
| `Ctrl+Y` | Restart Tony's process. |
| `Ctrl+O` | Show or hide the session status and Plan. |
| `Ctrl+Q` | Quit the window. |

## What the MVP does not do

No agent terminal stream, no model or thinking picker, no shared-plan editor, or
mouse-based text selection. The task composer uses GPUI's native text input bridge
for IME composition and pasted text, with keyboard cursor movement and deletion.
The window shows the mode, the phase, both agents, the driver diagnostics, the
session status and Plan, and the timeline. A picker built from a model
catalogue this frontend never fetches would be a menu that silently does nothing,
which is worse than not having the menu.

## Tests

```sh
cd gpui && cargo test
```

The tests cover the protocol, which is the half that can be wrong silently: that
a full `View` frame parses, that unknown and missing fields are tolerated, that
agent identity is lower case on the wire, and that a command frame serializes
with only the fields it needs. Rust is not part of `make check`, because CI
carries no Rust toolchain.
