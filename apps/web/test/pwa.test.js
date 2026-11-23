import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { test } from 'node:test';
import vm from 'node:vm';

import { HLC, LWWMap, ORSet, canonicalState, compareStamps, hlcToStamp, stampToHlc } from '../lib/crdt.js';
import { IdbStore, MemoryStore, openStore } from '../lib/store.js';
import { SyncClient, fetchTransport } from '../lib/syncclient.js';
import { decodeBatch, encodeBatch, fromBatch, toBatch } from '../lib/wire.js';

// What the real Go sync service returns for PullDelta (taken from its own code): clock values near 1.1e17, beyond a JS number's exact range.
const GOLDEN = '{"replica_id":"hub","clock":111411200008388609,"cursor":2,"lww":[{"key":"incident:1","value":"flooded","hlc":111411200008060935,"replica_id":"r1"},{"key":"incident:2","value":"","hlc":111411200008388609,"replica_id":"r2","deleted":true}],"orset_adds":[{"set":"units","element":"u1","tag":"r1:1"},{"set":"units","element":"u2","tag":"r2:1"}],"orset_removes":[{"set":"units","tag":"r2:1"}]}';

// ---------------------------------------------------------------------------------------------------------------------------- CRDT rules
test('the clock never goes backwards and orders local events after what it has seen', () => {
  let t = 1000;
  const h = new HLC('a', () => t);
  const s1 = h.now();
  const s2 = h.now(); // same millisecond: the counter breaks the tie
  assert.ok(compareStamps(s1, s2) < 0 && s2.wall === 1000 && s2.counter === 1);
  t = 500; // the wall clock jumps back
  assert.ok(compareStamps(s2, h.now()) < 0);
  const after = h.update({ wall: 5000, counter: 9, node: 'b' });
  assert.deepEqual([after.wall, after.counter], [5000, 10]);
  assert.ok(compareStamps({ wall: 5000, counter: 9, node: 'b' }, h.now()) < 0);
});

test('stamps order by wall, counter, then node, like the server', () => {
  assert.ok(compareStamps({ wall: 1, counter: 9, node: 'z' }, { wall: 2, counter: 0, node: 'a' }) < 0);
  assert.ok(compareStamps({ wall: 1, counter: 1, node: 'z' }, { wall: 1, counter: 2, node: 'a' }) < 0);
  assert.ok(compareStamps({ wall: 1, counter: 1, node: 'a' }, { wall: 1, counter: 1, node: 'b' }) < 0);
  assert.equal(compareStamps({ wall: 1, counter: 1, node: 'a' }, { wall: 1, counter: 1, node: 'a' }), 0);
});

test('the wire clock keeps every digit, and refuses a counter that does not fit 16 bits', () => {
  const s = { wall: 1_700_000_000_123, counter: 7, node: 'r1' };
  assert.equal(stampToHlc(s), 111411200008060935n);
  assert.deepEqual(hlcToStamp(111411200008060935n, 'r1'), s);
  assert.throws(() => stampToHlc({ wall: 1, counter: 70000, node: 'x' }), RangeError);
});

test('last-writer-wins gives every replica the same winner in any order, and a delete is a write', () => {
  assert.equal(new LWWMap().get('never-written'), undefined);
  const writes = [['k', 'v1', { wall: 5, counter: 0, node: 'a' }], ['k', 'v2', { wall: 5, counter: 0, node: 'b' }], ['k', 'v0', { wall: 4, counter: 9, node: 'z' }]];
  const results = [writes, [...writes].reverse(), [writes[1], writes[2], writes[0]]].map((order) => {
    const m = new LWWMap();
    for (const [k, v, s] of order) m.set(k, v, s);
    return m.get('k');
  });
  assert.deepEqual(results, ['v2', 'v2', 'v2']);
  const m = new LWWMap();
  m.set('k', 'v', { wall: 1, counter: 0, node: 'a' });
  m.delete('k', { wall: 2, counter: 0, node: 'a' });
  m.set('k', 'old', { wall: 1, counter: 5, node: 'a' }); // an older write after the delete does not bring it back
  assert.equal(m.get('k'), undefined);
  assert.deepEqual(m.keys(), []);
});

test('a concurrent add wins over a remove, and merging twice changes nothing', () => {
  const a = new ORSet('a');
  const b = new ORSet('b');
  const first = a.add('u1');
  b.merge(first);
  const removal = b.remove('u1'); // b removes what it has seen
  const concurrent = a.add('u1'); // a adds again at the same time
  a.merge(removal);
  b.merge(concurrent);
  b.merge(concurrent);
  assert.ok(a.contains('u1') && b.contains('u1'));
  assert.deepEqual(a.state(), b.state());
});

// ---------------------------------------------------------------------------------------------------------------------------- wire format
test('a batch from the real Go server decodes with every digit of every clock value', () => {
  const b = decodeBatch(GOLDEN);
  assert.equal(b.clock, 111411200008388609n);
  assert.equal(b.cursor, 2);
  assert.deepEqual(b.lww.map((e) => [e.key, e.hlc, e.deleted]), [['incident:1', 111411200008060935n, false], ['incident:2', 111411200008388609n, true]]);
  const d = fromBatch(b);
  assert.deepEqual(d.lww[0].reg.stamp, { wall: 1_700_000_000_123, counter: 7, node: 'r1' });
  assert.notEqual(String(JSON.parse(GOLDEN).lww[0].hlc), '111411200008060935', 'plain JSON.parse would have rounded it');
});

test('encoding writes exact integer literals the server can parse, and round-trips', () => {
  const b = decodeBatch(GOLDEN);
  const text = encodeBatch(b);
  assert.match(text, /"hlc":111411200008060935[,}]/);
  assert.match(text, /"clock":111411200008388609[,}]/);
  assert.equal(JSON.stringify(decodeBatch(text), (_, v) => (typeof v === 'bigint' ? String(v) : v)), JSON.stringify(b, (_, v) => (typeof v === 'bigint' ? String(v) : v)));
});

test('a value that looks like a clock field cannot corrupt a batch', () => {
  const nasty = '{"hlc":123}"clock":9';
  const text = encodeBatch(toBatch('r', { lww: [{ key: 'k', reg: { value: nasty, stamp: { wall: 3, counter: 1, node: 'r' }, deleted: false } }], adds: [], removes: [] }));
  assert.equal(decodeBatch(text).lww[0].value, nasty);
});

test('malformed batches are refused', () => {
  for (const bad of ['not json', '[]', '{"lww":[{"key":"","value":"v","hlc":1,"replica_id":"r"}]}', '{"lww":[{"key":"k","value":"v","hlc":1}]}', '{"orset_adds":[{"set":"s"}]}', '{"orset_removes":[{"tag":"t"}]}']) {
    assert.throws(() => decodeBatch(bad), undefined, bad);
  }
});

// ---------------------------------------------------------------------------------------------------------------------------- storage
class FakeIDB {
  constructor() { this.dbs = new Map(); }

  open(name) {
    const req = {};
    setTimeout(() => {
      let rec = this.dbs.get(name);
      const fresh = !rec;
      if (fresh) { rec = { stores: new Map() }; this.dbs.set(name, rec); }
      req.result = {
        createObjectStore: (n, o = {}) => rec.stores.set(n, { rows: new Map(), auto: o.autoIncrement, key: o.keyPath, next: 1 }),
        transaction: (n) => {
          const tx = { pending: 0, done: false };
          const finish = () => setTimeout(() => { if (!tx.pending && !tx.done) { tx.done = true; tx.oncomplete?.(); } }, 0);
          const wrap = (store, fn) => {
            const r = {};
            tx.pending++;
            setTimeout(() => { r.result = fn(store); tx.pending--; r.onsuccess?.(); finish(); }, 0);
            return r;
          };
          const copy = (v) => (v === undefined ? undefined : JSON.parse(JSON.stringify(v)));
          tx.objectStore = (sn) => {
            const st = rec.stores.get(sn);
            return {
              get: (k) => wrap(st, (s) => copy(s.rows.get(k))),
              put: (v, k) => wrap(st, (s) => { s.rows.set(k, copy(v)); return k; }),
              add: (v) => wrap(st, (s) => { const id = s.next++; const row = { ...copy(v), [s.key]: id }; s.rows.set(id, row); return id; }),
              delete: (k) => wrap(st, (s) => { s.rows.delete(k); }),
              getAll: () => wrap(st, (s) => [...s.rows.values()].map(copy)),
              getAllKeys: () => wrap(st, (s) => [...s.rows.keys()]),
            };
          };
          finish();
          return tx;
        },
      };
      if (fresh) req.onupgradeneeded?.();
      req.onsuccess?.();
    }, 0);
    return req;
  }
}

for (const [label, make] of [['memory store', async () => new MemoryStore()], ['IndexedDB store', async () => IdbStore.open('t', new FakeIDB())]]) {
  test(`${label}: records, keys and a strictly ordered outbox`, async () => {
    const s = await make();
    await s.put('a', { n: 1 });
    await s.put('b', 'text');
    assert.deepEqual(await s.get('a'), { n: 1 });
    assert.equal(await s.get('missing'), undefined);
    assert.deepEqual(await s.keys(), ['a', 'b']);
    await s.delete('a');
    assert.deepEqual(await s.keys(), ['b']);
    const ids = [await s.outboxAdd({ x: 1 }), await s.outboxAdd({ x: 2 }), await s.outboxAdd({ x: 3 })];
    assert.ok(ids[0] < ids[1] && ids[1] < ids[2]);
    assert.deepEqual((await s.outboxAll()).map((e) => e.x), [1, 2, 3]);
    await s.outboxRemove([ids[1]]);
    assert.deepEqual((await s.outboxAll()).map((e) => e.x), [1, 3]);
    await s.outboxRemove([]); // removing nothing is fine
    assert.equal((await s.outboxAll()).length, 2);
  });
}

test('IndexedDB data survives closing and reopening the database', async () => {
  const idb = new FakeIDB();
  const a = await IdbStore.open('app', idb);
  await a.put('k', 'v');
  await a.outboxAdd({ pending: true });
  const b = await IdbStore.open('app', idb);
  assert.equal(await b.get('k'), 'v');
  assert.equal((await b.outboxAll()).length, 1);
});

test('without IndexedDB, or when it cannot open, the app falls back to memory', async () => {
  assert.ok((await openStore({ indexedDB: undefined })) instanceof MemoryStore);
  const broken = { open: () => { const r = {}; setTimeout(() => r.onerror?.(), 0); return r; } };
  assert.ok((await openStore({ indexedDB: broken })) instanceof MemoryStore);
  assert.ok((await openStore({ indexedDB: new FakeIDB() })) instanceof IdbStore);
});

// ---------------------------------------------------------------------------------------------------------------------------- sync
/** A stand-in for the server that speaks the same wire format and applies the same merge rules. */
class Hub {
  constructor() { this.lww = new LWWMap(); this.sets = new Map(); this.pushes = 0; this.failNext = 0; this.lastSince = null; this.lossRate = 0; this.rand = Math.random; this.seq = 0; this.regSeq = new Map(); }

  #maybeLose() {
    if (this.failNext > 0) { this.failNext--; throw new Error('network down'); }
    if (this.lossRate && this.rand() < this.lossRate) throw new Error('packet lost');
  }

  async push(text) {
    this.#maybeLose();
    this.pushes++;
    const d = fromBatch(decodeBatch(text));
    for (const e of d.lww) {
      const before = this.lww.regs.get(e.key);
      this.lww.merge([e]);
      if (this.lww.regs.get(e.key) !== before) this.regSeq.set(e.key, ++this.seq);
    }
    for (const a of d.adds) this.#set(a.set).merge({ adds: { [a.element]: [a.tag] }, removed: [] });
    for (const r of d.removes) this.#set(r.set).merge({ adds: {}, removed: [r.tag] });
    return '{}';
  }

  async pull(text) {
    this.#maybeLose();
    const since = Number(/"since_seq":\s*(\d+)/.exec(text)[1]);
    this.lastSince = since;
    const b = { replica_id: 'hub', clock: 0n, cursor: this.seq, lww: [], orset_adds: [], orset_removes: [] };
    for (const [key, reg] of [...this.lww.regs].sort(([a], [b2]) => (a < b2 ? -1 : 1))) {
      const hlc = stampToHlc(reg.stamp);
      if (hlc > b.clock) b.clock = hlc;
      if (this.regSeq.get(key) > since) b.lww.push({ key, value: reg.value, hlc, replica_id: reg.stamp.node, deleted: reg.deleted });
    }
    for (const [name, o] of this.sets) {
      const st = o.state();
      for (const [element, tags] of Object.entries(st.adds)) for (const tag of tags) b.orset_adds.push({ set: name, element, tag });
      for (const tag of st.removed) b.orset_removes.push({ set: name, tag });
    }
    return JSON.stringify({ ...JSON.parse(encodeBatch(b).replace(/"(hlc|clock)":(\d+)/g, '"$1":"$2"')), cursor: this.seq }).replace(/"(hlc|clock)":"(\d+)"/g, '"$1":$2');
  }

  #set(name) { if (!this.sets.has(name)) this.sets.set(name, new ORSet('hub')); return this.sets.get(name); }

  canonical() { return canonicalState(this.lww, this.sets); }
}

const client = async (hub, id, opts = {}) => new SyncClient({ store: opts.store ?? new MemoryStore(), transport: hub, replicaId: id, online: opts.online ?? (() => true), clock: opts.clock ?? (() => 1_700_000_000_000) }).init();

test('offline writes work at once, queue up, and are sent in one batch when the connection returns', async () => {
  const hub = new Hub();
  let online = false;
  const c = await client(hub, 'r1', { online: () => online });
  await c.set('incident:1', 'flooded');
  await c.addToSet('units', 'u1');
  await c.set('incident:2', 'fire');
  assert.equal(c.get('incident:1'), 'flooded');
  assert.ok(c.has('units', 'u1'));
  assert.equal(await c.pending(), 3);
  assert.deepEqual(await c.flush(), { sent: 0, offline: true });
  assert.equal(hub.pushes, 0);
  online = true;
  const r = await c.sync();
  assert.equal(r.pushed.sent, 3);
  assert.equal(await c.pending(), 0);
  assert.equal(hub.pushes, 1);
  assert.equal(hub.lww.get('incident:2'), 'fire');
  assert.ok(hub.sets.get('units').contains('u1'));
});

test('a failed send loses nothing: the queue stays and the retry succeeds', async () => {
  const hub = new Hub();
  const c = await client(hub, 'r1');
  await c.set('k', 'v');
  hub.failNext = 1;
  const failed = await c.flush();
  assert.equal(failed.sent, 0);
  assert.ok(failed.error);
  assert.equal(await c.pending(), 1);
  assert.equal((await c.flush()).sent, 1);
  assert.equal(hub.lww.get('k'), 'v');
  assert.equal(await c.pending(), 0);
});

test('changes made offline survive a restart: state, queue and the set tag counter', async () => {
  const hub = new Hub();
  const store = new MemoryStore();
  const a = await client(hub, 'r1', { store, online: () => false });
  await a.set('k', 'v');
  await a.addToSet('units', 'u1');
  const b = await client(hub, 'r1', { store, online: () => false }); // the app was closed and reopened
  assert.equal(b.get('k'), 'v');
  assert.ok(b.has('units', 'u1'));
  assert.equal(await b.pending(), 2);
  await b.addToSet('units', 'u2');
  const tags = [...b.sets.get('units').adds.values()].flatMap((t) => [...t]).sort();
  assert.deepEqual(tags, ['r1:1', 'r1:2'], 'tags must stay unique after a restart');
});

test('a pull merges server changes, only fetches what is newer, and keeps later local writes ahead of them', async () => {
  const hub = new Hub();
  const remote = await client(hub, 'r2');
  await remote.set('k', 'from-server');
  await remote.flush();
  const behind = await client(hub, 'r1', { clock: () => 1000 }); // this device's clock is far behind
  assert.equal((await behind.pull()).merged, 1);
  assert.equal(behind.get('k'), 'from-server');
  const cursor = behind.since;
  assert.equal(cursor, 1);
  await behind.pull();
  assert.equal(hub.lastSince, cursor, 'the second pull asks only for what is newer than the first');
  await behind.set('k', 'local-later');
  await behind.flush();
  assert.equal(hub.lww.get('k'), 'local-later', 'a write made after seeing the remote value must win even on a slow clock');
});

test('two devices that edited the same things offline end with the same state as the server', async () => {
  const hub = new Hub();
  const a = await client(hub, 'a', { clock: () => 1_700_000_000_005 });
  const b = await client(hub, 'b', { clock: () => 1_700_000_000_005 });
  await a.set('incident:1', 'by-a');
  await b.set('incident:1', 'by-b');
  await a.addToSet('units', 'u1');
  await b.addToSet('units', 'u1');
  await b.removeFromSet('units', 'u1');
  for (let i = 0; i < 2; i++) { await a.sync(); await b.sync(); }
  assert.equal(a.canonical(), b.canonical());
  assert.equal(a.canonical(), hub.canonical());
  assert.equal(a.get('incident:1'), 'by-b'); // same wall time and counter: the higher node id wins, on every device
  assert.ok(a.has('units', 'u1'), "a's own add was concurrent with b's remove, so it survives");
});

test('many devices, many conflicting edits, 20% of calls lost: everyone converges', async () => {
  let seed = 12345;
  const rand = () => { seed = (seed * 1664525 + 1013904223) % 4294967296; return seed / 4294967296; };
  const hub = new Hub();
  hub.rand = rand;
  const devices = [];
  for (let i = 0; i < 12; i++) devices.push(await client(hub, `d${String(i).padStart(2, '0')}`, { clock: () => 1_700_000_000_000 + (i % 3) }));
  for (let e = 0; e < 300; e++) {
    const d = devices[Math.floor(rand() * devices.length)];
    const key = `incident:${Math.floor(rand() * 6)}`;
    const op = Math.floor(rand() * 10);
    if (op <= 5) await d.set(key, `${d.replicaId}-${e}`);
    else if (op === 6) await d.del(key);
    else if (op <= 8) await d.addToSet('units', `u${Math.floor(rand() * 5)}`);
    else await d.removeFromSet('units', `u${Math.floor(rand() * 5)}`);
  }
  assert.notEqual(new Set(devices.map((d) => d.canonical())).size, 1, 'devices must have diverged while offline');
  hub.lossRate = 0.2;
  let rounds = 0;
  for (; rounds < 100; rounds++) {
    for (const d of devices) await d.sync();
    if (devices.every((d) => d.canonical() === hub.canonical())) break;
  }
  assert.ok(rounds < 100, 'did not converge');
  assert.ok(devices.every((d) => d.canonical() === hub.canonical()));
});

test('the fetch transport posts to the sync endpoints and reports a bad status', async () => {
  const calls = [];
  const ok = (url, init) => { calls.push([url, init.method, init.body, init.headers['x-tenant-id']]); return Promise.resolve({ ok: true, text: () => Promise.resolve('reply') }); };
  const t = fetchTransport('https://api.test', { 'x-tenant-id': 'T' }, ok);
  assert.equal(await t.push('P'), 'reply');
  assert.equal(await t.pull('Q'), 'reply');
  assert.deepEqual(calls, [['https://api.test/sync/push', 'POST', 'P', 'T'], ['https://api.test/sync/pull', 'POST', 'Q', 'T']]);
  const bad = fetchTransport('https://api.test', {}, () => Promise.resolve({ ok: false, status: 503 }));
  await assert.rejects(() => bad.push('x'), /503/);
});

test('autoSync syncs when the browser comes back online, and can be stopped', async () => {
  const hub = new Hub();
  const c = await client(hub, 'r1');
  await c.set('k', 'v');
  const handlers = new Map();
  const target = { addEventListener: (t, f) => handlers.set(t, f), removeEventListener: (t) => handlers.delete(t) };
  const stop = c.autoSync(target);
  handlers.get('online')();
  await new Promise((r) => setTimeout(r, 20));
  assert.equal(hub.lww.get('k'), 'v');
  stop();
  assert.equal(handlers.size, 0);
});

// ---------------------------------------------------------------------------------------------------------------------------- service worker
const SW = readFileSync(new URL('../public/sw.js', import.meta.url), 'utf8');
const ORIGIN = 'https://app.test';

function loadSw({ network }) {
  const listeners = {};
  const store = new Map(); // cache name -> Map(url -> response)
  const key = (r) => (typeof r === 'string' ? new URL(r, ORIGIN).href : r.url);
  const caches = {
    open: async (name) => {
      if (!store.has(name)) store.set(name, new Map());
      const c = store.get(name);
      return { addAll: async (urls) => { for (const u of urls) c.set(key(u), { ok: true, url: u, cached: true }); }, put: async (req, res) => { c.set(key(req), res); } };
    },
    keys: async () => [...store.keys()],
    delete: async (n) => store.delete(n),
    match: async (req) => { for (const c of store.values()) if (c.has(key(req))) return c.get(key(req)); return undefined; },
  };
  const calls = [];
  const fetchFn = (req) => { calls.push(req.url); return network(req); };
  const self = { addEventListener: (t, f) => { listeners[t] = f; }, location: { origin: ORIGIN }, skipWaiting: async () => {}, clients: { claim: async () => {} } };
  vm.runInNewContext(SW, { self, caches, fetch: fetchFn, URL, Response: { error: () => ({ error: true }) }, Promise });
  const fire = async (type, init = {}) => {
    const e = { ...init, waitUntil(p) { this.p = p; }, respondWith(p) { this.responded = p; } };
    listeners[type](e);
    if (e.p) await e.p;
    return e;
  };
  return { fire, store, calls };
}

const res = (ok = true, tag = 'net') => ({ ok, tag, clone() { return this; } });
const req = (path, extra = {}) => ({ url: ORIGIN + path, method: 'GET', mode: 'cors', ...extra });

test('the service worker caches the app shell on install and drops only its own old caches on activate', async () => {
  const sw = loadSw({ network: async () => res() });
  sw.store.set('aegismesh-shell-v0', new Map());
  sw.store.set('someone-elses-cache', new Map());
  await sw.fire('install');
  assert.deepEqual([...sw.store.get('aegismesh-shell-v1').keys()].sort(), ['/', '/command', '/field', '/manifest.webmanifest'].map((p) => ORIGIN + p).sort());
  await sw.fire('activate');
  assert.deepEqual([...sw.store.keys()].sort(), ['aegismesh-shell-v1', 'someone-elses-cache']);
});

test('the service worker never touches writes, API and sync calls, or other origins', async () => {
  const sw = loadSw({ network: async () => res() });
  for (const r of [req('/field', { method: 'POST' }), req('/api/incidents'), req('/sync/push'), req('/ws'), { url: 'https://other.test/x.js', method: 'GET', mode: 'cors' }]) {
    assert.equal((await sw.fire('fetch', { request: r })).responded, undefined, r.url);
  }
});

test('pages are network-first and cached, and fall back to the cache with no connection', async () => {
  let up = true;
  const sw = loadSw({ network: async () => { if (!up) throw new TypeError('offline'); return res(true, 'fresh'); } });
  await sw.fire('install');
  const online = await (await sw.fire('fetch', { request: req('/field/report', { mode: 'navigate' }) })).responded;
  assert.equal(online.tag, 'fresh');
  assert.equal([...sw.store.get('aegismesh-shell-v1').keys()].includes(ORIGIN + '/field/report'), true);
  up = false;
  const cachedPage = await (await sw.fire('fetch', { request: req('/field/report', { mode: 'navigate' }) })).responded;
  assert.equal(cachedPage.tag, 'fresh');
  const unknown = await (await sw.fire('fetch', { request: req('/never-seen', { mode: 'navigate' }) })).responded;
  assert.equal(unknown.cached, true, 'an unknown page offline gets the cached field app shell');
});

test('static files are cache-first, and only good responses are cached', async () => {
  let status = true;
  const sw = loadSw({ network: async () => res(status, 'static') });
  const first = await (await sw.fire('fetch', { request: req('/_next/static/app.js') })).responded;
  assert.equal(first.tag, 'static');
  assert.deepEqual(sw.calls, [ORIGIN + '/_next/static/app.js']);
  await (await sw.fire('fetch', { request: req('/_next/static/app.js') })).responded;
  assert.equal(sw.calls.length, 1, 'the second request is answered from the cache');
  status = false;
  await (await sw.fire('fetch', { request: req('/_next/static/missing.js') })).responded;
  assert.equal([...sw.store.get('aegismesh-shell-v1').keys()].includes(ORIGIN + '/_next/static/missing.js'), false);
});
