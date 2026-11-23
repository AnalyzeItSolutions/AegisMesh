// JS port of the server's CRDT rules (services/sync-go/internal/crdt): hybrid logical clock, last-writer-wins registers, add-wins OR-set.
// A stamp is {wall, counter, node}: ordered by wall time, then counter, then node id, exactly like the Go side.

export function compareStamps(a, b) {
  if (a.wall !== b.wall) return a.wall < b.wall ? -1 : 1;
  if (a.counter !== b.counter) return a.counter < b.counter ? -1 : 1;
  if (a.node !== b.node) return a.node < b.node ? -1 : 1;
  return 0;
}

const SHIFT = 16n;
const COUNTER_MASK = 0xffffn;

/** The wire form of a stamp's time: wall << 16 | counter, as a BigInt (it does not fit a JS number). */
export function stampToHlc(s) {
  if (s.counter > 0xffff) throw new RangeError('counter does not fit the 16-bit wire field');
  return (BigInt(s.wall) << SHIFT) | BigInt(s.counter);
}

export function hlcToStamp(hlc, node) {
  const b = BigInt(hlc);
  return { wall: Number(b >> SHIFT), counter: Number(b & COUNTER_MASK), node };
}

/** Never goes backwards; orders local events after everything it has seen. */
export class HLC {
  constructor(node, clock = () => Date.now()) {
    this.node = node;
    this.clock = clock;
    this.wall = 0;
    this.counter = 0;
  }

  now() {
    const phys = this.clock();
    if (phys > this.wall) {
      this.wall = phys;
      this.counter = 0;
    } else {
      this.counter++;
    }
    return { wall: this.wall, counter: this.counter, node: this.node };
  }

  update(r) {
    const phys = this.clock();
    const wall = Math.max(phys, this.wall, r.wall);
    if (wall === this.wall && wall === r.wall) this.counter = Math.max(this.counter, r.counter) + 1;
    else if (wall === this.wall) this.counter++;
    else if (wall === r.wall) this.counter = r.counter + 1;
    else this.counter = 0;
    this.wall = wall;
    return { wall: this.wall, counter: this.counter, node: this.node };
  }

  snapshot() {
    return { wall: this.wall, counter: this.counter };
  }

  restore(s) {
    if (s) {
      this.wall = s.wall;
      this.counter = s.counter;
    }
  }
}

/** Keyed last-writer-wins registers; a delete is a tombstone that wins or loses by stamp like any write. */
export class LWWMap {
  constructor() {
    this.regs = new Map(); // key -> {value, stamp, deleted}
  }

  #put(key, reg) {
    const cur = this.regs.get(key);
    if (!cur || compareStamps(cur.stamp, reg.stamp) < 0) this.regs.set(key, reg);
    return [{ key, reg: this.regs.get(key) }]; // the delta to send
  }

  set(key, value, stamp) {
    return this.#put(key, { value, stamp, deleted: false });
  }

  delete(key, stamp) {
    return this.#put(key, { value: '', stamp, deleted: true });
  }

  /** Applies a delta or a full state, in any order, any number of times. */
  merge(entries) {
    for (const { key, reg } of entries) this.#put(key, { value: reg.value, stamp: { ...reg.stamp }, deleted: !!reg.deleted });
  }

  get(key) {
    const r = this.regs.get(key);
    return r && !r.deleted ? r.value : undefined;
  }

  keys() {
    return [...this.regs].filter(([, r]) => !r.deleted).map(([k]) => k).sort();
  }
}

/** Observed-remove set where a concurrent add wins over a remove: every add carries a unique tag; a remove drops only the tags it has seen. */
export class ORSet {
  constructor(node, n = 0) {
    this.node = node;
    this.n = n;
    this.adds = new Map(); // element -> Set(tags)
    this.removed = new Set(); // tags
  }

  add(element) {
    const tag = `${this.node}:${++this.n}`;
    if (!this.adds.has(element)) this.adds.set(element, new Set());
    this.adds.get(element).add(tag);
    return { adds: { [element]: [tag] }, removed: [] };
  }

  remove(element) {
    const tags = [...(this.adds.get(element) ?? [])].filter((t) => !this.removed.has(t)).sort();
    for (const t of tags) this.removed.add(t);
    return { adds: {}, removed: tags };
  }

  merge(delta) {
    for (const [el, tags] of Object.entries(delta.adds ?? {})) {
      if (!this.adds.has(el)) this.adds.set(el, new Set());
      for (const t of tags) this.adds.get(el).add(t);
    }
    for (const t of delta.removed ?? []) this.removed.add(t);
  }

  contains(element) {
    return [...(this.adds.get(element) ?? [])].some((t) => !this.removed.has(t));
  }

  elements() {
    return [...this.adds.keys()].filter((e) => this.contains(e)).sort();
  }

  /** The full state in canonical (sorted) form. */
  state() {
    const adds = {};
    for (const el of [...this.adds.keys()].sort()) adds[el] = [...this.adds.get(el)].sort();
    return { adds, removed: [...this.removed].sort() };
  }
}

/** One string for the whole state of a replica; equal states give identical strings, so replicas can be compared byte for byte. */
export function canonicalState(lww, sets) {
  const regs = {};
  for (const k of [...lww.regs.keys()].sort()) {
    const { value, stamp, deleted } = lww.regs.get(k);
    regs[k] = { value, stamp: { wall: stamp.wall, counter: stamp.counter, node: stamp.node }, deleted: !!deleted };
  }
  const out = {};
  for (const name of [...sets.keys()].sort()) {
    const s = sets.get(name).state();
    if (Object.keys(s.adds).length || s.removed.length) out[name] = s;
  }
  return JSON.stringify({ regs, sets: out });
}
