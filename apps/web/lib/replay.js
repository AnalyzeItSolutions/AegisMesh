// Time travel: the dispatch view (incidents, units, hazards, blocked roads) as it was after any event of the log. The rules are the same as the
// server's projection (services/dispatch-go/projection.go): events apply strictly in sequence, a repeat is ignored, a gap or an unknown event is an error.

const KNOWN = new Set(['IncidentReported', 'UnitDispatched', 'RoadBlocked', 'HazardPolygonAdded']);

export const emptyState = () => ({ lastSeq: 0, incidents: {}, units: {}, hazards: {}, blockedEdges: {} });

/** Exact-digit parsing of an array of events: h3_cell in a payload is a 64-bit integer that JSON.parse would round. */
export function parseEvents(text) {
  const events = JSON.parse(text.replace(/"h3_cell":\s*(\d+)/g, '"h3_cell":"$1"'));
  if (!Array.isArray(events)) throw new TypeError('events must be a JSON array');
  return events;
}

const hex = (v) => (v === undefined || v === null || v === '' ? null : BigInt(v).toString(16));

function check(cond, message) {
  if (!cond) throw new Error(message);
}

/**
 * Applies one event to `state` in place. Returns state. A malformed event, an unknown type or a sequence gap throws BEFORE anything changes.
 * An event at or below the last applied sequence is a replay and changes nothing.
 */
export function applyEvent(state, ev) {
  check(ev && Number.isInteger(ev.sequence_id) && ev.sequence_id >= 1, 'event needs an integer sequence_id');
  if (ev.sequence_id <= state.lastSeq) return state;
  check(ev.sequence_id === state.lastSeq + 1, `sequence gap: have ${state.lastSeq}, got ${ev.sequence_id}`);
  check(KNOWN.has(ev.event_type), `unknown event type ${ev.event_type}`);
  const p = ev.payload;
  check(p && typeof p === 'object', 'event needs a payload');
  switch (ev.event_type) {
    case 'IncidentReported':
      check(typeof p.id === 'string' && p.id, 'IncidentReported needs an id');
      state.incidents[p.id] = { id: p.id, kind: p.kind ?? '', severity: p.severity ?? '', lat: p.lat, lon: p.lon, h3: hex(p.h3_cell), reported_at: p.reported_at, status: 'open', unit_ids: [] };
      break;
    case 'UnitDispatched': {
      check(p.incident_id && p.unit_id, 'UnitDispatched needs incident_id and unit_id');
      state.units[p.unit_id] = { id: p.unit_id, status: 'dispatched', incident_id: p.incident_id };
      const inc = state.incidents[p.incident_id];
      if (inc) {
        inc.status = 'dispatched';
        inc.unit_ids.push(p.unit_id);
      }
      break;
    }
    case 'RoadBlocked':
      check(typeof p.edge_id === 'string' && p.edge_id, 'RoadBlocked needs an edge_id');
      state.blockedEdges[p.edge_id] = true;
      break;
    case 'HazardPolygonAdded':
      check(typeof p.id === 'string' && p.id && Array.isArray(p.polygon) && p.polygon.length >= 3, 'HazardPolygonAdded needs an id and a polygon of at least 3 points');
      state.hazards[p.id] = { id: p.id, kind: p.kind ?? '', polygon: p.polygon, active: true };
      break;
    default:
  }
  state.lastSeq = ev.sequence_id;
  return state;
}

/** The state after the event with sequence_id `seq` (0 = before anything). */
export function replayTo(events, seq) {
  const state = emptyState();
  for (const ev of events) {
    if (ev.sequence_id > seq) break;
    applyEvent(state, ev);
  }
  return state;
}

/** A log loaded once and scrubbed back and forth cheaply: a snapshot every `every` events, so a seek replays at most `every` events. */
export class Timeline {
  constructor(events, { every = 500 } = {}) {
    check(Number.isInteger(every) && every >= 1, 'every must be a positive integer');
    check(events.every((e, i) => e.sequence_id === i + 1), 'events must be numbered 1, 2, 3 ... without gaps or repeats');
    this.events = events;
    this.every = every;
    this.snapshots = [emptyState()]; // snapshots[i] = the state after event i * every
    const s = emptyState();
    for (const ev of events) {
      applyEvent(s, ev);
      if (s.lastSeq % every === 0) this.snapshots[s.lastSeq / every] = structuredClone(s);
    }
    this.lastSeq = s.lastSeq;
  }

  /** The state after event `seq`, as a fresh object the caller may keep or change. */
  seek(seq) {
    check(Number.isInteger(seq) && seq >= 0 && seq <= this.lastSeq, `seq must be 0..${this.lastSeq}`);
    const base = Math.floor(seq / this.every);
    const state = structuredClone(this.snapshots[base]);
    for (let i = base * this.every; i < seq; i++) applyEvent(state, this.events[i]);
    return state;
  }
}
