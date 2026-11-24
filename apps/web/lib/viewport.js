// The map's visible rectangle -> the H3 cells to subscribe to, and the subscription message for the telemetry WebSocket.
// Cell ids travel as exact 64-bit integers (about 6e17, beyond a JS number's 2^53): h3-js uses hex strings, the wire uses decimal digits.
import { getHexagonAreaAvg, getHexagonEdgeLengthAvg, polygonToCells } from 'h3-js';

export const MAX_CELLS = 5000; // the server's limit for one subscription
export const MAX_RES = 9; // the server refuses cells finer than a frame's own cell
const KM_PER_DEG = 111.32;
const CHUNK_DEG = 60; // h3 polygons are filled in strips of at most this many degrees of longitude

const isNum = (v) => typeof v === 'number' && Number.isFinite(v);

/** Checks a {north, south, east, west} rectangle (degrees). west > east means it crosses the antimeridian. */
export function checkBounds(b) {
  if (!b || ![b.north, b.south, b.east, b.west].every(isNum)) throw new TypeError('bounds need numeric north, south, east and west');
  if (b.south >= b.north || b.north > 90 || b.south < -90) throw new RangeError('bounds: south must be below north, both within -90..90');
  if (b.east < -180 || b.east > 180 || b.west < -180 || b.west > 180) throw new RangeError('bounds: longitudes must be within -180..180');
  return b;
}

const lonSpan = (b) => (b.east >= b.west ? b.east - b.west : b.east + 360 - b.west);

export function boundsAreaKm2(b) {
  const midLat = ((b.north + b.south) / 2) * (Math.PI / 180);
  return (b.north - b.south) * KM_PER_DEG * lonSpan(b) * KM_PER_DEG * Math.max(0.05, Math.cos(midLat));
}

/** The finest resolution (at most MAX_RES) whose cells for this rectangle are expected to stay within maxCells. */
export function chooseResolution(bounds, maxCells = MAX_CELLS) {
  const area = boundsAreaKm2(checkBounds(bounds));
  for (let res = MAX_RES; res > 0; res--) {
    if ((area / getHexagonAreaAvg(res, 'km2')) * 1.4 <= maxCells) return res; // 1.4: cells along the edge, which an area estimate leaves out
  }
  return 0;
}

const wrap = (lon) => ((((lon + 180) % 360) + 360) % 360) - 180;

/** A map library's bounds object (getNorth/getSouth/getEast/getWest, longitudes possibly beyond +-180 after panning round the world) -> a checked rectangle. */
export function fromMapBounds(b) {
  const north = Math.min(85, b.getNorth());
  const south = Math.max(-85, b.getSouth());
  const west = b.getWest();
  const east = b.getEast();
  if (east - west >= 360) return { north, south, west: -180, east: 180 };
  return { north, south, west: wrap(west), east: wrap(east) };
}

/** The rectangle widened by one cell edge, so a unit just outside the visible edge is already subscribed when the map pans. */
function pad(b, res) {
  const km = getHexagonEdgeLengthAvg(res, 'km');
  const dLat = km / KM_PER_DEG;
  const north = Math.min(90, b.north + dLat);
  const south = Math.max(-90, b.south - dLat);
  const dLon = dLat / Math.max(0.05, Math.cos(((north + south) / 2) * (Math.PI / 180)));
  if (lonSpan(b) + 2 * dLon >= 360) return { north, south, west: -180, east: 180 };
  return { north, south, west: wrap(b.west - dLon), east: wrap(b.east + dLon) };
}

/** The H3 cells (hex strings) covering the rectangle at a resolution, chosen from its size unless given. Throws if more than maxCells are needed. */
export function viewportCells(bounds, { res, maxCells = MAX_CELLS } = {}) {
  checkBounds(bounds);
  const r = res ?? chooseResolution(bounds, maxCells);
  if (!Number.isInteger(r) || r < 0 || r > MAX_RES) throw new RangeError(`resolution must be 0..${MAX_RES}`);
  const p = pad(bounds, r);
  // refuse a hopeless request from its size alone: enumerating millions of cells only to throw would take tens of seconds and gigabytes
  if (boundsAreaKm2(p) / getHexagonAreaAvg(r, 'km2') > maxCells * 2) throw new RangeError(`the viewport needs more than ${maxCells} cells at resolution ${r}`);
  const strips = p.west <= p.east ? [[p.west, p.east]] : [[p.west, 180], [-180, p.east]];
  const cells = new Set();
  for (const [w, e] of strips) {
    for (let lo = w; lo < e; lo += CHUNK_DEG) {
      const hi = Math.min(e, lo + CHUNK_DEG);
      for (const c of polygonToCells([[p.south, lo], [p.south, hi], [p.north, hi], [p.north, lo]], r)) cells.add(c);
      if (cells.size > maxCells) throw new RangeError(`the viewport needs more than ${maxCells} cells at resolution ${r}`);
    }
  }
  return [...cells];
}

/** Hex cell id (h3-js) -> decimal digits (the wire form). */
export const cellToWire = (hex) => BigInt(`0x${hex}`).toString();

/** Decimal digits -> hex cell id. */
export const wireToCell = (digits) => BigInt(digits).toString(16);

const byValue = (a, b) => (a < b ? -1 : a > b ? 1 : 0);

/** The WebSocket subscription message with every cell as an exact integer literal. */
export function viewportMessage(cells) {
  const digits = cells.map((c) => BigInt(`0x${c}`)).sort(byValue).map(String);
  return `{"type":"viewport","cells":[${digits.join(',')}]}`;
}

/** Keeps the server's idea of the viewport in step with the map: sends a message only when the set of cells actually changed. */
export class ViewportSubscription {
  constructor({ send, maxCells = MAX_CELLS }) {
    this.send = send;
    this.maxCells = maxCells;
    this.key = null;
    this.cells = [];
    this.res = null;
  }

  /** Call whenever the map moves or zooms. Returns {sent, cells, res}. Bad bounds throw and send nothing. */
  update(bounds) {
    checkBounds(bounds);
    const res = chooseResolution(bounds, this.maxCells);
    const cells = viewportCells(bounds, { res, maxCells: this.maxCells });
    const message = viewportMessage(cells);
    if (message === this.key) return { sent: false, cells: cells.length, res };
    this.send(message);
    this.key = message;
    this.cells = cells;
    this.res = res;
    return { sent: true, cells: cells.length, res };
  }

  /** After a reconnect the server has forgotten the viewport: the next update sends it again. */
  reset() {
    this.key = null;
  }
}
