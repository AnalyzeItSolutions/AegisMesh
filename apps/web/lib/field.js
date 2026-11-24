// The field app's rules, kept out of the page so they can be tested without a browser: report validation, the record that goes into the synced
// store, the offline banner's wording, and the list of reports held on this device.

export const KINDS = ['flood', 'fire', 'collapse', 'medical', 'road', 'other'];
export const SEVERITIES = ['red', 'yellow', 'green', 'black'];
export const NOTE_MAX = 500;

export function newId(crypto = globalThis.crypto) {
  if (crypto?.randomUUID) return crypto.randomUUID();
  const h = (n) => Array.from({ length: n }, () => Math.floor(Math.random() * 16).toString(16)).join('');
  return `${h(8)}-${h(4)}-4${h(3)}-${'89ab'[Math.floor(Math.random() * 4)]}${h(3)}-${h(12)}`;
}

const isNum = (v) => typeof v === 'number' && Number.isFinite(v);

/** {ok, errors: {field: message}} for a report as typed in the form (lat and lon may still be strings). */
export function validateReport(r) {
  const errors = {};
  const lat = typeof r.lat === 'string' && r.lat.trim() !== '' ? Number(r.lat) : r.lat;
  const lon = typeof r.lon === 'string' && r.lon.trim() !== '' ? Number(r.lon) : r.lon;
  if (!KINDS.includes(r.kind)) errors.kind = 'Choose what happened.';
  if (!SEVERITIES.includes(r.severity)) errors.severity = 'Choose how severe it is.';
  if (!isNum(lat) || lat < -90 || lat > 90) errors.lat = 'Latitude must be a number between -90 and 90.';
  if (!isNum(lon) || lon < -180 || lon > 180) errors.lon = 'Longitude must be a number between -180 and 180.';
  if ((r.note ?? '').length > NOTE_MAX) errors.note = `Keep the note under ${NOTE_MAX} characters.`;
  return { ok: Object.keys(errors).length === 0, errors };
}

/** The synced-store record for a report: {id, key, value} where value is the JSON text stored under `key`. Throws if the report is invalid. */
export function buildIncident(r, { id = newId(), now = Date.now() } = {}) {
  const { ok, errors } = validateReport(r);
  if (!ok) throw new Error(`invalid report: ${Object.values(errors).join(' ')}`);
  const record = { id, kind: r.kind, severity: r.severity, lat: Number(r.lat), lon: Number(r.lon), note: (r.note ?? '').trim(), reported_at: now };
  return { id, key: `incident:${id}`, value: JSON.stringify(record) };
}

export function parseIncident(value) {
  try {
    const o = JSON.parse(value);
    return o && typeof o === 'object' && typeof o.id === 'string' ? o : null;
  } catch {
    return null;
  }
}

/** The reports in a synced map of registers, newest first. Entries that are not valid reports are skipped. */
export function listIncidents(lww) {
  return lww
    .keys()
    .filter((k) => k.startsWith('incident:'))
    .map((k) => parseIncident(lww.get(k)))
    .filter(Boolean)
    .sort((a, b) => b.reported_at - a.reported_at || (a.id < b.id ? -1 : 1));
}

/** The banner: {level, text}. Offline is stated first and says nothing is lost. */
export function syncStatus({ online, pending, error = null }) {
  const n = `${pending} change${pending === 1 ? '' : 's'}`;
  if (!online) {
    return { level: 'offline', text: pending ? `You are offline. ${n} saved on this device and will be sent when the connection returns.` : 'You are offline. Anything you report is saved on this device and sent when the connection returns.' };
  }
  if (error && pending) return { level: 'error', text: `Could not send ${n} yet; they are safe on this device and will be retried.` };
  if (pending) return { level: 'pending', text: `Sending ${n}…` };
  return { level: 'synced', text: 'All changes sent.' };
}
