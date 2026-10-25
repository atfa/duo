// The mobile console. It renders the host's state, sends the three commands a
// phone is allowed to send, and keeps working across reconnects: the sequence
// epoch, the pending command and the replay the relay offers are all handled
// here, so a phone that drops its Wi-Fi catches up on its own.

import { LinkError, decryptEnvelope, deriveKeys, parsePairingKey, parseRoom } from "./crypto.js";
import { agentLabel, agentMeta, applyEvent, buildCommand, dotClass, driverChangeAllowed, filterModels, isCurrentModel, mergeCatalog, modeKey, needsModeConfirm, nextSeq, pendingTimeout, resendText, settlePending, viewModel } from "./console.js";

const RECONNECT_MIN_MS = 500;
const RECONNECT_MAX_MS = 15000;
const PING_MS = 25000;

const els = {
  title: document.getElementById("title"),
  status: document.getElementById("status"),
  banner: document.getElementById("banner"),
  room: document.getElementById("room"),
  mode: document.getElementById("mode"),
  modes: document.getElementById("modes"),
  phase: document.getElementById("phase"),
  step: document.getElementById("step"),
  stepTitle: document.getElementById("step-title"),
  progress: document.getElementById("progress"),
  expiry: document.getElementById("expiry"),
  plan: document.getElementById("plan"),
  agents: document.getElementById("agents"),
  drivers: { austin: document.getElementById("driver-austin"), tony: document.getElementById("driver-tony") },
  driverNote: document.getElementById("driver-note"),
  modelAgent: document.getElementById("model-agent"),
  modelLoad: document.getElementById("model-load"),
  modelStatus: document.getElementById("model-status"),
  modelFilter: document.getElementById("model-filter"),
  modelList: document.getElementById("model-list"),
  activity: document.getElementById("activity"),
  observer: document.getElementById("observer"),
  refresh: document.getElementById("refresh"),
  pausectl: document.getElementById("pausectl"),
  composer: document.getElementById("composer"),
  task: document.getElementById("task"),
  send: document.getElementById("send"),
  newsess: document.getElementById("newsess"),
  sent: document.getElementById("sent"),
};

let room = null;
let keys = null;
let model = null;
// catalog is the model list the host published for the agent the page is looking
// at. It is page state rather than a view of one state, because a state may omit
// it and the picker must not empty out when it does.
let catalog = null;
let socket = null;
let lastSeq = 0;
let expiresAt = 0;
let attempts = 0;
let hostPresent = false;
let fatal = false;
let retryTimer = 0;
let pingTimer = 0;
let expiryTimer = 0;
// One command in flight. Its id is reused on a retry, so a command that was
// already forwarded is dropped by the relay and by the host rather than run
// twice. The timer is what keeps a lost answer from disabling the page for good.
let pending = null;
let pendingTimer = 0;

// resolvePending ends the wait for the command in flight when it is over: the
// host answered it, the session it was addressed to is gone, or the host stayed
// silent past the bounded wait. The same rule runs on every state and from the
// timer, so the two triggers cannot disagree.
function resolvePending() {
  const before = pending;
  const result = settlePending(pending, model, Date.now());
  pending = result.pending;
  if (!pending && pendingTimer) {
    clearTimeout(pendingTimer);
    pendingTimer = 0;
  }
  if (result.note !== null) setSent(result.note);
  // A command that did not run keeps its text: the composer was cleared when it
  // was sent, and telling the human to send it again without it is how a prompt
  // gets lost. A draft they have started is left alone.
  if (result.resend) {
    const draft = resendText(before, els.task.value);
    if (draft) els.task.value = draft;
  }
  return result;
}

function setStatus(text, kind) {
  els.status.textContent = text;
  els.status.className = `status status-${kind}`;
}

function setBanner(text) {
  els.banner.textContent = text ?? "";
  els.banner.hidden = !text;
}

function setSent(text) {
  els.sent.textContent = text ?? "";
}

function render() {
  els.title.textContent = model ? model.project : "Duo";
  els.mode.textContent = model?.mode || "—";
  els.phase.textContent = model?.phase || "—";
  // The select shows the host's mode, except while a switch is in flight: the
  // choice the human just made must not snap back before the host answers.
  if (!pending) els.modes.value = modeKey(model?.mode);
  // The vitals stay on one line, so the header carries only the counter and the
  // long pair (policy and title) lives in the session card, where there is room.
  const step = model?.step;
  els.step.textContent = step ? `${step.index}/${step.total}` : "—";
  els.stepTitle.textContent = step ? [step.policy, step.title].filter(Boolean).join(" · ") : "No step yet.";
  els.progress.hidden = !step;
  if (step) {
    els.progress.max = step.total || 1;
    els.progress.value = step.index || 0;
  }
  els.plan.textContent = model?.plan || "";

  const agents = model?.agents.length
    ? model.agents
    : [{ id: "—", connected: false, running: false, model: "", thinking: "", activity: "no agent state" }];
  els.agents.replaceChildren(
    ...agents.map((agent) => {
      const li = document.createElement("li");
      const dot = document.createElement("span");
      dot.className = `dot ${dotClass(agent)}`;
      const body = document.createElement("div");
      const name = document.createElement("div");
      name.textContent = agentLabel(agent.id);
      const meta = document.createElement("div");
      meta.className = "agent-meta";
      meta.textContent = agentMeta(agent);
      const activity = document.createElement("div");
      activity.className = "agent-activity";
      activity.textContent = agent.activity || (agent.running ? "working" : "");
      body.append(name, meta, activity);
      li.append(dot, body);
      return li;
    }),
  );

  // The host appends oldest first. The feed reads newest first so the entry that
  // just happened is at the top of the tab instead of below the fold.
  const activity = model?.activity.length
    ? model.activity.slice().reverse()
    : [{ at: "", agent: "", text: "No activity yet.", muted: true }];
  els.activity.replaceChildren(
    ...activity.map((entry) => {
      const li = document.createElement("li");
      const when = document.createElement("span");
      when.className = "when";
      when.textContent = entry.at;
      const who = document.createElement("span");
      who.className = "who";
      who.textContent = agentLabel(entry.agent);
      const text = document.createElement("span");
      text.className = entry.muted ? "text muted" : "text";
      text.textContent = entry.text;
      li.append(when, who, text);
      return li;
    }),
  );

  els.observer.textContent = model?.observer.analyzing
    ? "Analyzing…"
    : model?.observer.report || "No report yet.";
  els.observer.className = model?.observer.report || model?.observer.analyzing ? "observer" : "observer muted";

  const ready = Boolean(model) && hostPresent && !fatal;
  els.send.disabled = !ready || Boolean(pending);
  els.newsess.disabled = !ready || Boolean(pending);
  els.refresh.disabled = !ready || Boolean(pending);
  els.modes.disabled = !ready || Boolean(pending);
  renderPause(ready);
  renderDrivers();
  renderModels();
}

// One button covers the whole pause lifecycle: the label comes from the host's
// pause state, so a phone never offers an action the session cannot take. While
// "pausing" the label offers cancel instead of waiting, because the request may
// sit until the agents reach an idle boundary.
const PAUSE_ACTIONS = { running: ["pause", "Pause work"], pausing: ["unpause", "Cancel pause"], paused: ["continue", "Resume work"] };

function renderPause(ready) {
  // viewModel flattens the host's pause projection to its state string, so the
  // table is keyed by that value and a pause the page cannot read keeps the
  // button dark rather than offering an action it does not mean.
  const [kind, label] = PAUSE_ACTIONS[model?.pause] ?? [null, "—"];
  els.pausectl.textContent = label;
  els.pausectl.disabled = !ready || Boolean(pending);
  els.pausectl.dataset.kind = kind ?? "";
}

// The driver selects are rebuilt only when the host's list changes: a render
// happens on every state, and replacing the options under an open select would
// close it on the human's finger.
let driversKey = "";

function renderDrivers() {
  const list = model?.drivers ?? [];
  const key = list.join("\u0000");
  if (key !== driversKey) {
    for (const select of Object.values(els.drivers)) {
      select.replaceChildren(
        ...list.map((name) => {
          const option = document.createElement("option");
          option.value = name;
          option.textContent = name;
          return option;
        }),
      );
    }
    driversKey = key;
  }
  const allowed = driverChangeAllowed(model);
  const ready = Boolean(model) && hostPresent && !fatal;
  for (const agent of model?.agents ?? []) {
    const select = els.drivers[agent.id];
    if (!select) continue;
    // The host's driver stays selected while a change is in flight, so a refusal
    // snaps the control back to what is really running.
    if (!pending) select.value = list.includes(agent.driver) ? agent.driver : "";
    select.disabled = !ready || Boolean(pending) || !allowed;
  }
  els.driverNote.hidden = allowed || !model;
  els.driverNote.textContent = allowed ? "" : "A driver can only change before the first task, while paused, or after DONE.";
}

// The model picker shows one agent at a time, because the host publishes one list
// at a time. Its rows are rebuilt only when what they would show changes: a render
// runs on every state, and rebuilding a list of buttons under a thumb loses taps.
let modelsKey = "";

function renderModels() {
  const ready = Boolean(model) && hostPresent && !fatal;
  const selected = els.modelAgent.value || "austin";
  els.modelAgent.disabled = !ready || Boolean(pending);
  els.modelLoad.disabled = !ready || Boolean(pending);
  els.modelFilter.disabled = !ready;

  // A list that belongs to the other agent is not this agent's list.
  const shown = catalog && catalog.agent === selected ? catalog : null;
  const filtered = filterModels(shown?.models ?? [], els.modelFilter.value);
  const current = model?.agents.find((agent) => agent.id === selected)?.model ?? "";
  const key = [selected, current, Boolean(pending), ready, shown?.loading, shown?.error, filtered.join("\u0000")].join("|");
  if (key !== modelsKey) {
    modelsKey = key;
    els.modelList.replaceChildren(
      ...filtered.map((reference) => {
        const li = document.createElement("li");
        const button = document.createElement("button");
        button.type = "button";
        button.className = isCurrentModel(reference, current) ? "model current" : "model";
        button.dataset.model = reference;
        button.textContent = reference;
        button.disabled = !ready || Boolean(pending);
        li.append(button);
        return li;
      }),
    );
  }

  els.modelStatus.textContent = modelStatusText(shown, filtered);
}

function modelStatusText(shown, filtered) {
  if (!shown) return "Load the model list to see what this agent can run.";
  if (shown.loading) return "Reading models from the driver…";
  if (shown.error) return shown.error;
  if (shown.models.length === 0) return "This driver reported no models.";
  const cut = shown.total > shown.models.length ? ` · the list is cut to ${shown.models.length} of ${shown.total}` : "";
  if (filtered.length === shown.models.length) return `${shown.models.length} models${cut}`;
  return `${filtered.length} of ${shown.models.length} match${cut}`;
}

function send(frame) {
  if (!socket || socket.readyState !== WebSocket.OPEN) return false;
  socket.send(JSON.stringify(frame));
  return true;
}

function stopPing() {
  if (pingTimer) clearInterval(pingTimer);
  pingTimer = 0;
}

function startPing() {
  stopPing();
  pingTimer = setInterval(() => send({ t: "ping" }), PING_MS);
}

function startExpiry() {
  if (expiryTimer) clearInterval(expiryTimer);
  const tick = () => {
    if (!expiresAt) return;
    const left = expiresAt * 1000 - Date.now();
    if (left <= 0) {
      els.expiry.textContent = "expired";
      fail("expired", "This pairing expired. Run /qrcode in Duo to create a new one.");
      return;
    }
    const minutes = Math.floor(left / 60000);
    els.expiry.textContent = minutes >= 60 ? `in ${Math.floor(minutes / 60)}h${String(minutes % 60).padStart(2, "0")}m` : `in ${minutes}m`;
  };
  tick();
  expiryTimer = setInterval(tick, 30000);
}

function fail(status, message) {
  fatal = true;
  setStatus(status, "bad");
  setBanner(message);
  stopPing();
  try {
    if (socket) socket.close(1000, status);
  } catch {
    // already closed
  }
  socket = null;
  render();
}

function handleError(code) {
  switch (code) {
    case "auth":
      // A scan can land in the moment before the computer registers the room.
      // Retry briefly; a rotated key still ends as fatal.
      if (authRetries < 5) {
        authRetries += 1;
        setBanner("Waiting for the computer to register this pairing…");
        scheduleReconnect();
        break;
      }
      fail("rejected", "The relay rejected this key. It may have been rotated or revoked.");
      break;
    case "expired":
      fail("expired", "This pairing expired. Run /qrcode in Duo to create a new one.");
      break;
    case "room_full":
      setBanner("The relay is at its connection limit for this room. Retrying shortly.");
      break;
    case "offline":
      setBanner("Duo is not connected right now. The command will be sent again when it returns.");
      break;
    case "rate_limited":
      setSent("Too many commands in a minute. The relay is protecting itself; your computer is unaffected.");
      break;
    case "bad_frame":
      setSent("The relay rejected a frame.");
      break;
    default:
      setBanner(`Relay error: ${code}`);
  }
}

function handleBye(code) {
  switch (code) {
    case "revoked":
      fail("revoked", "Remote access was revoked on the computer. Scan a new QR code to pair again.");
      break;
    case "expired":
      fail("expired", "This pairing expired.");
      break;
    case "over_budget":
      fail("paused", "The free relay budget for this room is spent. Duo itself keeps running on the computer.");
      break;
    case "replaced":
      setBanner("Another device replaced this session. Reconnecting…");
      break;
    default:
      setBanner(`Relay closed the connection: ${code}`);
  }
}

async function handleEnvelope(frame) {
  if (!Number.isSafeInteger(frame.seq) || !frame.kind || typeof frame.iv !== "string" || typeof frame.ct !== "string") return;
  // The newest sequence number wins; one that does not move forward means the
  // room was recreated and the epoch restarts there.
  lastSeq = nextSeq(lastSeq, frame.seq);
  try {
    const plain = await decryptEnvelope(keys.encKey, room, frame);
    if (frame.kind === "state") {
      model = viewModel(plain);
      catalog = mergeCatalog(catalog, plain.catalog);
      // The host answers the command it received, by id; a state from a new
      // session, or no state at all, ends the wait the same way.
      resolvePending();
    } else if (frame.kind === "event") {
      model = applyEvent(model, plain);
    }
    send({ t: "ack", seq: frame.seq });
  } catch (error) {
    const reason = error instanceof LinkError ? error.message : "unreadable frame";
    setSent(`Could not verify a frame from the computer (${reason}). Ignored.`);
  }
  render();
}

function handleFrame(text) {
  let frame;
  try {
    frame = JSON.parse(text);
  } catch {
    return;
  }
  if (!frame || typeof frame !== "object") return;
  switch (frame.t) {
    case "ready":
      authRetries = 0;
      expiresAt = Number(frame.expiresAt) || 0;
      lastSeq = nextSeq(lastSeq, Number(frame.seq));
      startExpiry();
      setStatus("paired", "ok");
      break;
    case "msg":
      void handleEnvelope(frame);
      break;
    case "presence":
      hostPresent = Boolean(frame.host);
      setBanner(hostPresent ? "" : "Duo is offline on the computer. Reopen Duo to continue.");
      if (hostPresent) setStatus("paired", "ok");
      render();
      break;
    case "error":
      handleError(String(frame.code ?? ""));
      break;
    case "bye":
      handleBye(String(frame.code ?? ""));
      break;
    case "pong":
      break;
    default:
      break;
  }
}

function scheduleReconnect() {
  if (fatal) return;
  const backoff = Math.min(RECONNECT_MAX_MS, RECONNECT_MIN_MS * 2 ** attempts) + Math.floor(Math.random() * 250);
  attempts += 1;
  setStatus("reconnecting", "wait");
  clearTimeout(retryTimer);
  retryTimer = setTimeout(connect, backoff);
}

function connect() {
  if (fatal) return;
  clearTimeout(retryTimer);
  setStatus(attempts > 0 ? "reconnecting" : "connecting", "wait");
  const scheme = location.protocol === "https:" ? "wss" : "ws";
  let next;
  try {
    next = new WebSocket(`${scheme}://${location.host}/ws?room=${encodeURIComponent(room)}`, ["duo.v1"]);
  } catch {
    scheduleReconnect();
    return;
  }
  socket = next;
  next.onopen = () => {
    attempts = 0;
    send({ t: "hello", v: 1, role: "guest", room, proof: keys.proof, since: lastSeq });
    startPing();
    // A command that never reached the host is retried with the same id, so a
    // duplicate is dropped rather than run twice.
    if (pending) send(pending.frame);
  };
  next.onmessage = (event) => handleFrame(event.data);
  next.onclose = () => {
    stopPing();
    hostPresent = false;
    if (fatal) return;
    if (socket === next) socket = null;
    render();
    scheduleReconnect();
  };
  next.onerror = () => {
    // The close handler owns reconnection; browsers report failures there too.
  };
}

async function command(kind, text) {
  if (!model || !hostPresent) {
    setSent("Duo is not connected right now.");
    return;
  }
  if (kind === "task" && !String(text).trim()) {
    setSent("Type a task first.");
    return;
  }
  const id = crypto.randomUUID();
  try {
    const frame = await buildCommand({
      room,
      encKey: keys.encKey,
      macKey: keys.macKey,
      kind,
      text: String(text).trim(),
      nonce: model.nonce,
      id,
    });
    // The command is built, so the text is now the socket's: leaving it in the
    // textarea invites a second send of the same prompt once the pending command
    // is acknowledged, and the host would run it twice. A failed build keeps the
    // text so it can be retried.
    if (kind === "task") els.task.value = "";
    pending = { id, frame, session: model.session, at: Date.now(), text: kind === "task" ? String(text).trim() : "", timeout: pendingTimeout(kind) };
    if (pendingTimer) clearTimeout(pendingTimer);
    pendingTimer = setTimeout(() => {
      resolvePending();
      render();
    }, pendingTimeout(kind));
    setSent(kind === "task" ? "Sending…" : "Sending command…");
    if (!send(frame)) {
      setSent("Queued: Duo will receive it on reconnect.");
      scheduleReconnect();
    }
  } catch (error) {
    pending = null;
    setSent(`Could not send the command: ${error.message}`);
  }
  render();
}

// The page is three tabs over one dumb document: every panel is always in the
// DOM and keeps being rendered while it is hidden, so switching tabs costs no
// round trip and a command sent from Settings is answered on every tab.
const TABS = ["session", "activity", "settings"];

function selectTab(name, focus) {
  if (!TABS.includes(name)) return;
  for (const tab of TABS) {
    const button = document.getElementById(`tab-${tab}`);
    const panel = document.getElementById(`panel-${tab}`);
    if (!button || !panel) continue;
    const on = tab === name;
    button.setAttribute("aria-selected", on ? "true" : "false");
    button.tabIndex = on ? 0 : -1;
    panel.hidden = !on;
    if (on && focus) button.focus();
  }
  // The panels differ in height, so a tab picked while the page is scrolled down
  // would otherwise show its middle. The dock and the header stay put.
  window.scrollTo(0, 0);
}

function wireTabs() {
  const buttons = TABS.map((tab) => document.getElementById(`tab-${tab}`)).filter(Boolean);
  for (const button of buttons) {
    button.addEventListener("click", () => selectTab(button.id.slice(4)));
    button.addEventListener("keydown", (event) => {
      const at = buttons.indexOf(button);
      let target = -1;
      if (event.key === "ArrowRight") target = (at + 1) % buttons.length;
      else if (event.key === "ArrowLeft") target = (at - 1 + buttons.length) % buttons.length;
      else if (event.key === "Home") target = 0;
      else if (event.key === "End") target = buttons.length - 1;
      if (target < 0) return;
      event.preventDefault();
      selectTab(buttons[target].id.slice(4), true);
    });
  }
}

let authRetries = 0;

async function boot() {
  room = parseRoom(location.pathname);
  els.room.textContent = room ?? "—";
  if (!room) {
    fail("invalid link", "This link is not a Duo pairing link. Run /qrcode in Duo and scan again.");
    return;
  }
  let pairingKey;
  try {
    pairingKey = parsePairingKey(location.hash);
  } catch {
    fail("invalid key", "The key in this link is malformed. Run /qrcode in Duo and scan again.");
    return;
  }
  if (!pairingKey) {
    setStatus("no key", "wait");
    setBanner("Open this page from the QR code Duo shows: the key lives in the link fragment and never reaches the relay.");
    return;
  }
  try {
    keys = await deriveKeys(pairingKey);
  } catch {
    fail("invalid key", "Could not derive the pairing keys on this device.");
    return;
  }
  render();
  connect();
}

wireTabs();

els.composer.addEventListener("submit", (event) => {
  event.preventDefault();
  void command("task", els.task.value);
});
els.task.addEventListener("keydown", (event) => {
  if (event.key === "Enter" && (event.metaKey || event.ctrlKey)) {
    event.preventDefault();
    els.composer.requestSubmit();
  }
});
els.newsess.addEventListener("click", () => void command("new_session", ""));
els.refresh.addEventListener("click", () => void command("observer_report", ""));
els.pausectl.addEventListener("click", () => {
  const kind = els.pausectl.dataset.kind;
  if (kind) void command(kind, "");
});
els.modes.addEventListener("change", () => {
  const target = els.modes.value;
  if (!target) return;
  // A switch discards the round in progress, so a started session that is not
  // finished asks first. Before the first task and after DONE it costs nothing.
  if (needsModeConfirm(model) && !window.confirm(`Switch to ${target.toUpperCase()} mode? The round in progress is discarded.`)) {
    els.modes.value = modeKey(model?.mode);
    return;
  }
  void command("switch_mode", target);
});
for (const [id, select] of Object.entries(els.drivers)) {
  select.addEventListener("change", () => {
    if (!select.value) return;
    void command("set_driver", `${id} ${select.value}`);
  });
}
els.modelAgent.addEventListener("change", () => {
  if (els.modelAgent.value) void command("catalog", els.modelAgent.value);
});
els.modelLoad.addEventListener("click", () => void command("catalog", els.modelAgent.value || "austin"));
els.modelFilter.addEventListener("input", () => renderModels());
els.modelList.addEventListener("click", (event) => {
  const button = event.target.closest("button[data-model]");
  if (!button) return;
  const reference = button.dataset.model;
  if (!reference) return;
  void command("set_model", `${els.modelAgent.value || "austin"} ${reference}`);
});

window.addEventListener("online", () => {
  if (!fatal && (!socket || socket.readyState === WebSocket.CLOSED)) connect();
});
document.addEventListener("visibilitychange", () => {
  if (document.visibilityState === "visible" && !fatal && (!socket || socket.readyState === WebSocket.CLOSED)) connect();
});

void boot();
