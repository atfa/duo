# xduo desktop window smoke check

`scripts/xduo-window-smoke.sh` launches the built renderer with one synthetic
view. It never starts a Duo session or agent. Run it from the checkout on a
logged-in desktop session after `make gpui`, or pass a renderer path:

```sh
scripts/xduo-window-smoke.sh
scripts/xduo-window-smoke.sh --interactive /path/to/xduo
```

An interactive terminal automatically enables the checklist pause. In CI,
X11 can run the state checks without a pause when `wmctrl` and `xprop` are
installed. The script reports exit status 77 when a display or the required
window-control interface is unavailable; that is a skip, not a pass. The
existing `scripts/gui-smoke.sh` remains the deterministic, headless protocol
check and is separate from this native-window check.

## Platform coverage

- **macOS:** starts the renderer and asks System Events for its `xduo` window.
  This needs a logged-in GUI session. macOS may require granting Accessibility
  access to the terminal running the script. Minimize, zoom, close behavior,
  and appearance still need a human to inspect; the script does not claim those
  actions passed merely because the process is alive.
- **Linux X11:** with `wmctrl` and `xprop`, checks the window title/class and
  asks the window manager to maximize, restore, minimize, restore, and close it.
  EWMH maximize state and process exit after close are asserted. A window
  manager that does not publish the minimized state produces an explicit skip
  for that one assertion. Decorations are rendered into pixels and are not
  inferred from X11 window properties.
- **Linux Wayland:** the script supports an interactive visual checklist and
  verifies that the renderer exits after the operator closes the window.
  Headless automation is reported as skipped. Wayland intentionally has no
  compositor-independent API for enumerating and controlling another
  application's toplevels; process liveness is not treated as proof that a
  visible window opened. A compositor-specific harness can add stronger checks
  in its own CI environment.

## Visual review checklist

Use the synthetic view while checking each platform and desktop environment:

1. Confirm the `xduo` window is visible, resizable, and titled `xduo`.
2. Inspect the titlebar, resize borders, and minimize/maximize/restore/close
   controls. Linux may use xduo client decorations or compositor decorations.
3. Check the actual Dock/taskbar icon. For macOS inspect the packaged `.app`
   opened through Finder; for Linux inspect the installed desktop launcher. A
   source icon file existing on disk does not prove the shell displays it.
4. Change the system appearance from light to dark and back, and confirm the
   xduo content updates both times.
5. Close the window and confirm the renderer exits. The synthetic view is not
   an unfinished live session, so a quit-confirmation prompt is not expected.

Do not run this as a visual test inside a GPU-less/headless container. The
process may fail to create a GPU window there; use `scripts/gui-smoke.sh` for
headless protocol coverage and run this check on a real desktop or a CI runner
that provides a logged-in display server.
