// The page's state model and command builder, kept free of the DOM so the tests
// can drive the exact code the phone runs.
//
// Shapes follow docs/remote-protocol.md. Nothing here trusts a field: a newer
// host may add them, an older one may omit them.

export const COMMAND_KINDS = new Set(["task", "new_session", "observer_report"]);
export const MAX_ACTIVITY = 12;

// nextSeq is the sequence rule: the newest sequence number wins, and one that
// does not move forward means the room was recreated, so the epoch restarts at
// that number. A frame with no usable seq leaves the watermark alone.
export function nextSeq(lastSeq, seq) {
  if (!Number.isSafeInteger(seq) || seq < 1) return lastSeq;
  return seq;
}

function text(value, fallback = "") {
  return typeof value === "string" ? value : fallback;
}

function clip(value, limit) {
  const value2 = text(value).trim();
  return value2.length > limit ? `${value2.slice(0, limit - 1)}…` : value2;
}

function time(value) {
  const ms = Number(value);
  if (!Number.isFinite(ms) || ms <= 0) return "";
  const date = new Date(ms);
  return `${String(date.getHours()).padStart(2, "0")}:${String(date.getMinutes()).padStart(2, "0")}`;
}

// agentId normalises the spelling of an agent. The host sends "austin" and
// "tony"; an older host sent "Austin" and "Tony", and a page that only matched
// one spelling rendered a red dot for an agent that was connected all along. So
// both the snapshot keys and an event's agent field fold through here.
export function agentId(value) {
  const id = text(value).trim().toLowerCase();
  return id === "austin" || id === "tony" ? id : "";
}

// agentLabel is the name a human reads, whatever spelling arrived.
export function agentLabel(value) {
  const id = agentId(value);
  if (id) return id.charAt(0).toUpperCase() + id.slice(1);
  return text(value);
}

// dotClass is the agent's indicator. Green means healthy and idle, blue means
// working, grey means "not attached yet", which is a normal moment during
// startup, and red is reserved for a process that actually failed. A page that
// painted "not started yet" red made two healthy agents look broken.
export function dotClass(agent) {
  if (!agent) return "dot-wait";
  if (agent.state === "failed") return "dot-bad";
  if (agent.running) return "dot-run";
  if (agent.connected) return "dot-on";
  return "dot-wait";
}

// agentMeta is the line under an agent's name: what runs it, with which model
// and thinking level. A host that reports no process state at all has no driver
// for this agent, and says so instead of leaving the phone to guess.
export function agentMeta(agent) {
  if (!agent) return "";
  if (agent.stateKnown && !agent.state) return "not configured";
  return [agent.driver, agent.model, agent.thinking].filter(Boolean).join(" · ");
}

// viewModel turns one decrypted state payload into what the page draws.
export function viewModel(state) {
  const source = state && typeof state === "object" ? state : {};
  const agents = source.agents && typeof source.agents === "object" ? source.agents : {};
  const known = {};
  for (const [key, value] of Object.entries(agents)) {
    const id = agentId(key);
    if (id && value && typeof value === "object") known[id] = value;
  }
  const step = source.step && typeof source.step === "object" ? source.step : null;
  const observer = source.observer && typeof source.observer === "object" ? source.observer : {};
  const reports = Array.isArray(observer.reports) ? observer.reports : [];
  const activity = Array.isArray(source.activity) ? source.activity : [];

  return {
    nonce: Number.isSafeInteger(source.n) ? source.n : 0,
    project: clip(source.project, 40) || "Duo",
    session: clip(source.session, 48),
    mode: text(source.mode).toUpperCase(),
    phase: text(source.phase),
    started: source.started === true,
    step: step
      ? {
          index: Number(step.index) || 0,
          total: Number(step.total) || 0,
          policy: text(step.policy).toUpperCase(),
          title: clip(step.title, 90),
        }
      : null,
    plan: clip(source.plan, 160),
    pause: text(source.pause?.state),
    agents: ["austin", "tony"].map((id) => {
      const agent = known[id] && typeof known[id] === "object" ? known[id] : {};
      return {
        id,
        connected: agent.connected === true,
        running: agent.running === true,
        driver: clip(agent.driver, 20),
        model: clip(agent.model, 28),
        thinking: text(agent.thinking),
        activity: clip(agent.activity, 120),
        // A host that predates the field says nothing about the process, so the
        // page stays quiet about it rather than claiming "not configured".
        state: text(agent.state),
        stateKnown: Object.hasOwn(agent, "state"),
      };
    }),
    activity: activity.slice(-MAX_ACTIVITY).map((entry) => ({
      at: time(entry?.at),
      kind: text(entry?.kind, "activity"),
      agent: text(entry?.agent),
      text: clip(entry?.text, 240),
    })),
    observer: {
      analyzing: observer.analyzing === true,
      report: reports.length > 0 ? clip(reports[reports.length - 1]?.text, 900) : "",
    },
    delivery:
      source.delivery && typeof source.delivery === "object"
        ? {
            applied: source.delivery.applied === true,
            head: clip(source.delivery.head, 16),
            note: clip(source.delivery.note, 160),
          }
        : null,
    // The host answers the last command it received, addressed by id, so a
    // refusal is visible on the phone instead of only in a local log.
    command:
      source.command && typeof source.command === "object"
        ? {
            id: text(source.command.id),
            kind: text(source.command.kind),
            accepted: source.command.accepted === true,
            note: clip(source.command.note, 200),
          }
        : null,
  };
}

// applyEvent folds one incremental event into the current model. A kind the
// page does not know is ignored: the host republishes a full state after a
// reconnect, so a dropped event can only ever cost a moment of detail.
export function applyEvent(model, event) {
  if (!model || !event || typeof event !== "object") return model;
  if (!["activity", "phase", "agent"].includes(event.kind)) return model;
  const next = { ...model, activity: model.activity.slice() };
  if (Number.isSafeInteger(event.n)) next.nonce = event.n;
  const at = time(event.at);
  const agent = text(event.agent);
  const body = clip(event.text, 240);
  switch (event.kind) {
    case "activity":
      next.activity.push({ at, kind: "activity", agent, text: body });
      if (next.activity.length > MAX_ACTIVITY) next.activity = next.activity.slice(-MAX_ACTIVITY);
      break;
    case "phase":
      next.phase = text(event.text, next.phase);
      break;
    case "agent": {
      const id = agentId(agent);
      if (id) {
        next.agents = next.agents.map((entry) =>
          entry.id === id ? { ...entry, activity: body, running: true } : entry,
        );
      }
      break;
    }
    default:
      // Observer and delivery results are richer than one line, so they arrive
      // with the next snapshot rather than being guessed from an event.
      break;
  }
  return next;
}

// A command stays in flight until the host answers it, and the page must
// recognise every way that wait ends or its buttons stay disabled forever: the
// answer itself, a state from a different session (the host restarted, so the
// answer will never come), and a bounded wait for a host that went silent.
// Nothing here resends: the relay drops a command id it already forwarded, so a
// silent host is never answered by retrying, and a resend could run a task twice.
export const PENDING_TIMEOUT_MS = 15000;

// settlePending resolves the one command in flight against a state the host
// published. A null note means "still waiting", so the caller says nothing new.
// resend says the command did not run, so its text has to go back to the human
// rather than being lost with the composer it was cleared from.
export function settlePending(pending, state, now) {
  if (!pending) return { pending: null, note: null, resend: false };
  const source = state && typeof state === "object" ? state : {};
  const answer = source.command && typeof source.command === "object" ? source.command : null;
  if (answer && answer.id === pending.id) {
    const accepted = answer.accepted === true;
    const note = accepted ? "Sent." : clip(answer.note, 200) || "The computer refused the command.";
    return { pending: null, note, resend: !accepted };
  }
  // A state from another session cannot answer this command: the host it was
  // addressed to is gone. A state from the same session that does not mention the
  // command is older news, and the answer may still arrive.
  if (pending.session && source.session && source.session !== pending.session) {
    return { pending: null, note: "Duo started a new session; the command was not confirmed.", resend: true };
  }
  const sent = Number(pending.at);
  if (Number.isFinite(now) && Number.isFinite(sent) && sent > 0 && now - sent >= PENDING_TIMEOUT_MS) {
    return { pending: null, note: "Duo did not answer; send it again.", resend: true };
  }
  return { pending, note: null, resend: false };
}

// resendText is the text a command that did not run gives back. The composer was
// cleared when the command was sent, so asking the human to send it again without
// the text is asking for a prompt they no longer have — which is exactly how a
// phone prompt was lost once already. A draft already started is never overwritten.
export function resendText(pending, draft) {
  if (!pending || typeof pending.text !== "string" || pending.text === "") return "";
  if (typeof draft === "string" && draft.trim() !== "") return "";
  return pending.text;
}

// buildCommand signs and encrypts one command. The MAC covers the fields the
// relay could change, and the id is repeated in the envelope's additional data,
// so a relay operator can neither rewrite nor duplicate a command.
export async function buildCommand({ room, encKey, macKey, kind, text: body, nonce, id, randomBytes }) {
  if (!COMMAND_KINDS.has(kind)) throw new Error(`unsupported command kind ${kind}`);
  const payload = { v: 1, id, kind, text: kind === "task" ? body : "", n: nonce };
  const { commandMac } = await import("./crypto.js");
  payload.mac = await commandMac(macKey, id, kind, nonce, payload.text);
  const { sealCommand } = await import("./crypto.js");
  const { iv, ct } = await sealCommand(encKey, room, id, JSON.stringify(payload), randomBytes);
  return { t: "cmd", id, iv, ct };
}
