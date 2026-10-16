# Duo's GPUI frontend

`xduo` is the graphical frontend: one GPU-rendered window over a Duo session,
built with [GPUI](https://gpui.rs), the Rust UI framework Zed uses.

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
macOS keeps its native titlebar, close, minimize and zoom controls. Release
archives include an `xduo.app` with the matching Duo binary and agent helpers.
For a source build, pass same-build binaries to
`scripts/package-xduo-macos.sh <output.app> <xduo> <duo> <helper-directory>`;
run `duo gui` with `DUO_GPUI_BIN` pointing to the app's
`Contents/MacOS/xduo-renderer` (the Finder launcher sets this automatically).
On Linux, xduo requests client-side decorations and draws its titlebar controls
and resize borders when GPUI reports client decorations active. If the compositor
negotiates server-side decorations, xduo leaves its titlebar to the compositor.
`app_id` is `xduo`; install the matching icon and launcher with
`scripts/install-xduo-desktop.sh` (the Duo CLI must be on PATH when installing).
The desktop launcher uses `DUO_REPO` or the current Git repository when available;
otherwise it prefers `zenity`, `kdialog`, or `yad` to choose a repository. With no
GUI chooser, it asks for the repository path in `x-terminal-emulator` or another
common terminal. The current Linux release `xduo` binary requires glibc 2.34 or
newer and a working Vulkan driver.

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
| `Ctrl+O` | Show or hide session status, Plan, and agent worktree paths, branches, and HEADs. |
| `Ctrl+P` | Show or hide both agents' structured work previews. |
| `Ctrl+M` / `Ctrl+Shift+M` | Open Austin's / Tony's model picker. Type to filter, use Up/Down to choose, Enter to apply, Tab to switch agents, and Esc to close. |
| `Ctrl+Alt+T` / `Ctrl+Shift+T` | Cycle Austin's / Tony's thinking level. Each agent card also has model and thinking controls. |
| `Ctrl+T` | Show or hide timeline timestamps. |
| `Ctrl+X` | Copy Austin's latest message to the human to the clipboard. |
| `F1` / `Ctrl+H` | Show or hide keyboard help. |
| `Ctrl+Q` / window close | Quit; an unfinished session asks for a second confirmation within 3 seconds. |

## Current boundaries

No agent terminal stream or shared-plan editor. The model picker uses each
agent's catalog from the session host and reports loading, driver errors, empty
catalogs, and unmatched filters separately. The work preview shows the structured
tool trail, recent durations, errors, context use, and streamed output provided
by the host. The task composer uses GPUI's native text input bridge for IME
composition and pasted text, with keyboard cursor movement, deletion, mouse
positioning, and selection.

## Tests

```sh
cd gpui && cargo test
```

The tests cover protocol parsing and serialization, quit confirmation, message
selection, composer editing, and client-decoration resize-edge selection. CI
runs both the Rust tests and release build.
