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

// viewModel turns one decrypted state payload into what the page draws.
export function viewModel(state) {
  const source = state && typeof state === "object" ? state : {};
  const agents = source.agents && typeof source.agents === "object" ? source.agents : {};
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
      const agent = agents[id] && typeof agents[id] === "object" ? agents[id] : {};
      return {
        id,
        connected: agent.connected === true,
        running: agent.running === true,
        model: clip(agent.model, 28),
        thinking: text(agent.thinking),
        activity: clip(agent.activity, 120),
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
      const id = agent === "austin" || agent === "tony" ? agent : "";
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
