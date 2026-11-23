// The unit markers on the map, kept as the latest position of every unit. Built for tens of thousands of units: an update is one Map write,
// and the array a map layer draws from is rebuilt at most once per change and handed out unchanged until the next one.

const isNum = (v) => typeof v === 'number' && Number.isFinite(v);

/** Exact-digit parsing of server messages: h3_cell is a 64-bit integer (about 6e17) that JSON.parse would round. It is kept as an h3-js hex string. */
export function parseServerMessage(text) {
  const doc = JSON.parse(text.replace(/"h3_cell":\s*(\d+)/g, '"h3_cell":"$1"'));
  if (doc === null || typeof doc !== 'object' || typeof doc.type !== 'string') throw new TypeError('not a server message');
  const norm = (f) => ({ ...f, h3: BigInt(f.h3_cell).toString(16) });
  if (doc.type === 'frame') return { type: 'frame', frame: norm(doc.frame) };
  if (doc.type === 'snapshot') return { type: 'snapshot', frames: (doc.frames ?? []).map(norm) };
  if (doc.type === 'error') return { type: 'error', message: String(doc.message ?? '') };
  throw new TypeError(`unknown message type ${doc.type}`);
}

export class MarkerStore {
  #units = new Map();
  #array = null;

  /** Applies one frame ({unit_id, lat, lon, h3, heading, ts}). A frame older than the one held is stale and ignored; an equal timestamp replaces. */
  upsert(frame) {
    if (!frame || typeof frame.unit_id !== 'string' || !frame.unit_id || !isNum(frame.lat) || !isNum(frame.lon) || !isNum(frame.ts)) return false;
    if (frame.lat < -90 || frame.lat > 90 || frame.lon < -180 || frame.lon > 180) return false;
    const cur = this.#units.get(frame.unit_id);
    if (cur && frame.ts < cur.ts) return false;
    this.#units.set(frame.unit_id, { unit_id: frame.unit_id, lat: frame.lat, lon: frame.lon, h3: frame.h3 ?? null, heading: isNum(frame.heading) ? frame.heading : 0, ts: frame.ts });
    this.#array = null;
    return true;
  }

  /** Applies many frames; returns how many were applied. */
  applyAll(frames) {
    let n = 0;
    for (const f of frames) if (this.upsert(f)) n++;
    return n;
  }

  remove(unitId) {
    const had = this.#units.delete(unitId);
    if (had) this.#array = null;
    return had;
  }

  clear() {
    this.#units.clear();
    this.#array = null;
  }

  get(unitId) {
    return this.#units.get(unitId);
  }

  get size() {
    return this.#units.size;
  }

  /** The units as an array for a map layer: the same array object until something changes (layers compare data by identity). */
  toArray() {
    if (!this.#array) this.#array = [...this.#units.values()];
    return this.#array;
  }

  /** Units inside a {north, south, east, west} rectangle (west > east crosses the antimeridian). */
  within({ north, south, east, west }) {
    const inLon = west <= east ? (x) => x >= west && x <= east : (x) => x >= west || x <= east;
    return this.toArray().filter((u) => u.lat >= south && u.lat <= north && inLon(u.lon));
  }

  /** Drops units not heard from since `now - maxAgeMs`; returns how many went. */
  prune(now, maxAgeMs) {
    let n = 0;
    for (const [id, u] of this.#units) {
      if (now - u.ts > maxAgeMs) {
        this.#units.delete(id);
        n++;
      }
    }
    if (n) this.#array = null;
    return n;
  }
}
