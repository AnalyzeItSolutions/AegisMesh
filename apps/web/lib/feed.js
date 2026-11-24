// The command center's live feed: a short, newest-first list of what just happened.

const fmt = (n) => Number(n).toFixed(4);

export function describeFrame(f) {
  return `${f.unit_id} at ${fmt(f.lat)}, ${fmt(f.lon)}`;
}

/** One line for an event-store event. */
export function describeEvent(ev) {
  const p = ev.payload ?? {};
  switch (ev.event_type) {
    case 'IncidentReported':
      return `Incident reported: ${p.kind ?? 'unknown'} (${p.severity ?? 'no severity'})`;
    case 'UnitDispatched':
      return `Unit ${p.unit_id} dispatched to incident ${p.incident_id}`;
    case 'HazardPolygonAdded':
      return `Hazard added: ${p.kind || 'unnamed'}`;
    case 'RoadBlocked':
      return `Road blocked: ${p.edge_id}`;
    default:
      return `Event ${ev.event_type}`;
  }
}

export class Feed {
  constructor(limit = 30) {
    if (!Number.isInteger(limit) || limit < 1) throw new RangeError('limit must be a positive integer');
    this.limit = limit;
    this.entries = [];
  }

  /** Adds an entry {at, text, kind} at the top; the oldest fall off the end. */
  push(entry) {
    this.entries.unshift(entry);
    if (this.entries.length > this.limit) this.entries.length = this.limit;
    return this;
  }

  clear() {
    this.entries = [];
  }
}
