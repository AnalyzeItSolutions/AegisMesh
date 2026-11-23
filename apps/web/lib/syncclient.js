// The field client's side of sync: every change is applied locally at once (the app works offline), recorded in an outbox, and sent when
// there is a connection. Sending is at-least-once and every merge is idempotent, so a lost reply or a repeated push does no harm.
import { HLC, LWWMap, ORSet, canonicalState, hlcToStamp } from './crdt.js';
import { decodeBatch, encodeBatch, fromBatch, toBatch } from './wire.js';

const STATE_KEY = 'state';

/** A transport that POSTs the batch text and returns the reply text: {push(text), pull(text)}. The gateway turns it into the gRPC call. */
export function fetchTransport(baseUrl, headers = {}, fetchImpl = globalThis.fetch) {
  const post = async (path, body) => {
    const res = await fetchImpl(`${baseUrl}${path}`, { method: 'POST', headers: { 'content-type': 'application/json', ...headers }, body });
    if (!res.ok) throw new Error(`sync ${path} failed: ${res.status}`);
    return res.text();
  };
  return { push: (text) => post('/sync/push', text), pull: (text) => post('/sync/pull', text) };
}

function emptyDelta() {
  return { lww: [], adds: [], removes: [] };
}

/** Folds delta b into a: the greater stamp wins per key, set adds and removes are unioned. */
function coalesce(a, b) {
  const byKey = new Map(a.lww.map((e) => [e.key, e]));
  for (const e of b.lww) {
    const cur = byKey.get(e.key);
    const s = e.reg.stamp;
    if (!cur || cur.reg.stamp.wall < s.wall || (cur.reg.stamp.wall === s.wall && (cur.reg.stamp.counter < s.counter || (cur.reg.stamp.counter === s.counter && cur.reg.stamp.node < s.node)))) byKey.set(e.key, e);
  }
  return { lww: [...byKey.values()], adds: [...a.adds, ...b.adds], removes: [...a.removes, ...b.removes] };
}

export class SyncClient {
  /**
   * @param store      a MemoryStore or IdbStore
   * @param transport  {push(text), pull(text)} returning promises of reply text
   * @param replicaId  this device's unique id
   * @param online     () => boolean: whether a connection is believed to exist
   * @param clock      () => milliseconds, injectable for tests
   */
  constructor({ store, transport, replicaId, online = () => true, clock = () => Date.now() }) {
    this.store = store;
    this.transport = transport;
    this.replicaId = replicaId;
    this.online = online;
    this.hlc = new HLC(replicaId, clock);
    this.lww = new LWWMap();
    this.sets = new Map(); // name -> ORSet
    this.since = 0; // the server cursor of the last pull (arrival order on the server, not a clock)
  }

  /** Loads what was saved (state, clock, pending changes stay in the outbox). Call once before use. */
  async init() {
    const saved = await this.store.get(STATE_KEY);
    if (saved) {
      this.hlc.restore(saved.hlc);
      this.lww.merge(saved.lww);
      for (const [name, s] of Object.entries(saved.sets)) {
        const o = new ORSet(this.replicaId, saved.counters[name] ?? 0);
        o.merge(s);
        this.sets.set(name, o);
      }
      this.since = Number(saved.since ?? 0);
    }
    return this;
  }

  async #save() {
    const sets = {};
    const counters = {};
    for (const [name, o] of this.sets) {
      sets[name] = o.state();
      counters[name] = o.n;
    }
    await this.store.put(STATE_KEY, { hlc: this.hlc.snapshot(), lww: [...this.lww.regs].map(([key, reg]) => ({ key, reg })), sets, counters, since: this.since });
  }

  #set(name) {
    if (!this.sets.has(name)) this.sets.set(name, new ORSet(this.replicaId));
    return this.sets.get(name);
  }

  async #record(delta) {
    await this.store.outboxAdd({ delta });
    await this.#save();
  }

  // -- local changes: applied immediately, queued for sending --
  async set(key, value) {
    if (!key) throw new TypeError('key is required');
    await this.#record({ ...emptyDelta(), lww: this.lww.set(key, String(value), this.hlc.now()) });
  }

  async del(key) {
    await this.#record({ ...emptyDelta(), lww: this.lww.delete(key, this.hlc.now()) });
  }

  async addToSet(name, element) {
    if (!name || !element) throw new TypeError('set name and element are required');
    const d = this.#set(name).add(element);
    await this.#record({ ...emptyDelta(), adds: Object.entries(d.adds).flatMap(([el, tags]) => tags.map((tag) => ({ set: name, element: el, tag }))) });
  }

  async removeFromSet(name, element) {
    const d = this.#set(name).remove(element);
    await this.#record({ ...emptyDelta(), removes: d.removed.map((tag) => ({ set: name, tag })) });
  }

  get(key) {
    return this.lww.get(key);
  }

  has(name, element) {
    return this.sets.get(name)?.contains(element) ?? false;
  }

  elements(name) {
    return this.sets.get(name)?.elements() ?? [];
  }

  async pending() {
    return (await this.store.outboxAll()).length;
  }

  /** One string for the whole local state, for comparing replicas. */
  canonical() {
    return canonicalState(this.lww, this.sets);
  }

  // -- talking to the server --
  /** Sends every queued change as one batch. Nothing is removed from the outbox unless the server accepted the batch. */
  async flush() {
    if (!this.online()) return { sent: 0, offline: true };
    const entries = await this.store.outboxAll();
    if (!entries.length) return { sent: 0 };
    const delta = entries.reduce((acc, e) => coalesce(acc, e.delta), emptyDelta());
    try {
      await this.transport.push(encodeBatch(toBatch(this.replicaId, delta, 0n)));
    } catch (error) {
      return { sent: 0, error };
    }
    await this.store.outboxRemove(entries.map((e) => e.id));
    return { sent: entries.length };
  }

  /** Fetches what the server has newer than the last pull and merges it. */
  async pull() {
    if (!this.online()) return { merged: 0, offline: true };
    let batch;
    try {
      batch = decodeBatch(await this.transport.pull(JSON.stringify({ since_seq: this.since })));
    } catch (error) {
      return { merged: 0, error };
    }
    const delta = fromBatch(batch);
    this.lww.merge(delta.lww);
    for (const e of delta.lww) this.hlc.update(e.reg.stamp); // later local writes are ordered after everything seen
    for (const a of delta.adds) this.#set(a.set).merge({ adds: { [a.element]: [a.tag] }, removed: [] });
    for (const r of delta.removes) this.#set(r.set).merge({ adds: {}, removed: [r.tag] });
    if (batch.cursor > this.since) this.since = batch.cursor;
    await this.#save();
    return { merged: delta.lww.length + delta.adds.length + delta.removes.length };
  }

  /** Send, then fetch. */
  async sync() {
    const pushed = await this.flush();
    const pulled = await this.pull();
    return { pushed, pulled };
  }

  /** In a browser: sync whenever the connection returns. Returns a function that stops it. */
  autoSync(target = globalThis) {
    const handler = () => { this.sync().catch(() => {}); };
    target.addEventListener('online', handler);
    return () => target.removeEventListener('online', handler);
  }
}

export { hlcToStamp };
