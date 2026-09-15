// Pure helpers for the Wehikuł czasu window: no DOM, no IPC, no catalogue.
// Kept apart so the node tests exercise exactly what the window computes.

export const GRANULARITIES = Object.freeze([1, 2, 4, 6, 12, 24]);
const ACTIVE_STATES = new Set(["planning", "planned", "fetching", "finalizing"]);
const FINAL_STATES = new Set(["complete", "failed", "cancelled", "interrupted"]);
const SKIP_REASONS = new Set(["reserved_device", "reserved_rune", "control_rune", "trailing_dot_or_space", "working_copy_name", "special"]);
const ACTIONS = new Set(["A", "M", "D", "R"]);

// The daemon refuses a confirmation that would leave less than this free;
// the dialog mirrors it instead of offering a button that can only fail.
export const SPACE_MARGIN = 64 * 1024 * 1024;

export function formatBytes(value, locale) {
  let amount = Number(value);
  if (!Number.isFinite(amount) || amount < 0) amount = 0;
  const units = ["byte", "kilobyte", "megabyte", "gigabyte", "terabyte"];
  let unit = 0;
  while (amount >= 1024 && unit < units.length - 1) {
    amount /= 1024;
    unit++;
  }
  return new Intl.NumberFormat(locale, { style: "unit", unit: units[unit], unitDisplay: "short", maximumFractionDigits: unit === 0 ? 0 : 1 }).format(amount);
}

export function utcOffsetLabel(minutes) {
  const absolute = Math.abs(minutes);
  return `${minutes < 0 ? "−" : "+"}${String(Math.floor(absolute / 60)).padStart(2, "0")}:${String(absolute % 60).padStart(2, "0")}`;
}

// localInputValue renders a moment for <input type="datetime-local">, which
// reads and writes the viewer's own clock.
export function localInputValue(ms) {
  const date = new Date(ms);
  const pad = number => String(number).padStart(2, "0");
  return `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())}T${pad(date.getHours())}:${pad(date.getMinutes())}:${pad(date.getSeconds())}`;
}

export function parseBars(buckets) {
  return (buckets ?? [])
    .map(bucket => ({ ...bucket, startMs: Date.parse(bucket.start), endMs: Date.parse(bucket.end) }))
    .filter(bar => Number.isFinite(bar.startMs) && Number.isFinite(bar.endMs) && bar.endMs > bar.startMs)
    .sort((left, right) => left.startMs - right.startMs);
}

// chartGeometry places every bar on one time axis, from the first bar to the
// later of the last bar and the chosen moment. Nothing is sampled away: each
// bar keeps a minimum width and height so a single change stays clickable.
export function chartGeometry(bars, momentMs) {
  if (!bars?.length) return null;
  const start = bars[0].startMs;
  const end = Math.max(bars[bars.length - 1].endMs, Number.isFinite(momentMs) ? momentMs : 0, start + 1);
  const span = end - start;
  const peak = Math.max(1, ...bars.map(bar => Number(bar.changed_paths) || 0));
  return {
    start,
    end,
    bars: bars.map(bar => ({
      bar,
      left: (bar.startMs - start) / span * 100,
      width: Math.max(0.35, (bar.endMs - bar.startMs) / span * 100),
      height: Math.max(4, (Number(bar.changed_paths) || 0) / peak * 100),
    })),
    marker: Number.isFinite(momentMs) && momentMs >= start && momentMs <= end ? (momentMs - start) / span * 100 : null,
  };
}

// intervalBounds turns a bar [start, end) into the daemon's inclusive range:
// the last millisecond before the next bar still belongs to this one.
export function intervalBounds(bar) {
  return { from: new Date(bar.startMs).toISOString(), to: new Date(bar.endMs - 1).toISOString() };
}

export function skipReasonKey(reason) {
  return `timeMachine.reason.${SKIP_REASONS.has(reason) ? reason : "unknown"}`;
}

export function actionKey(action) {
  return `timeMachine.action.${ACTIONS.has(action) ? action : "M"}`;
}

export const isActive = state => ACTIVE_STATES.has(state);
export const isFinal = state => FINAL_STATES.has(state);

export function hasRoom(operation) {
  return Number(operation?.bytes_total ?? 0) + SPACE_MARGIN <= Number(operation?.space_available ?? 0);
}

export function breadcrumb(path) {
  const parts = path ? path.split("/") : [];
  return parts.map((name, index) => ({ name, path: parts.slice(0, index + 1).join("/") }));
}

export const joinPath = (folder, name) => (folder ? `${folder}/${name}` : name);

export function parentPath(path) {
  const index = path.lastIndexOf("/");
  return index < 0 ? "" : path.slice(0, index);
}
