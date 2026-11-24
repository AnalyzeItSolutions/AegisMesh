import assert from 'node:assert/strict';
import { readFileSync, readdirSync, statSync } from 'node:fs';
import { dirname, join, resolve } from 'node:path';
import { test } from 'node:test';
import { fileURLToPath } from 'node:url';

import { LiveConnection } from '../lib/connection.js';
import { fetchTimeline } from '../lib/events.js';
import { Feed, describeEvent, describeFrame } from '../lib/feed.js';
import { KINDS, SEVERITIES, buildIncident, listIncidents, newId, parseIncident, syncStatus, validateReport } from '../lib/field.js';
import { LWWMap } from '../lib/crdt.js';
import { apiBase, authHeaders, DEFAULT_GATEWAY, gatewayUrl, readToken, TOKEN_KEY, wsUrl } from '../lib/gateway.js';
import { fromMapBounds, viewportCells } from '../lib/viewport.js';

const ROOT = resolve(dirname(fileURLToPath(import.meta.url)), '..');
const GOOD = { kind: 'flood', severity: 'red', lat: '12.97', lon: '77.59', note: ' water rising ' };

// ---------------------------------------------------------------------------------------------------------------------------- field form
test('a complete report is valid; each bad field is named', () => {
  assert.deepEqual(validateReport(GOOD), { ok: true, errors: {} });
  assert.deepEqual(Object.keys(validateReport({ ...GOOD, kind: 'x' }).errors), ['kind']);
  assert.deepEqual(Object.keys(validateReport({ ...GOOD, severity: '' }).errors), ['severity']);
  for (const lat of ['', 'abc', '91', '-91', NaN, null]) assert.ok(validateReport({ ...GOOD, lat }).errors.lat, `lat ${lat}`);
  for (const lon of ['', '181', '-181']) assert.ok(validateReport({ ...GOOD, lon }).errors.lon, `lon ${lon}`);
  assert.ok(validateReport({ ...GOOD, note: 'x'.repeat(501) }).errors.note);
  assert.equal(validateReport({ ...GOOD, lat: 0, lon: 0 }).ok, true, 'the equator and the prime meridian are real places');
  assert.ok(KINDS.includes('flood') && SEVERITIES.includes('red'));
});

test('a report becomes a stored record with a stable key and numeric coordinates', () => {
  const r = buildIncident(GOOD, { id: 'abc', now: 1_700_000_000_000 });
  assert.equal(r.key, 'incident:abc');
  assert.deepEqual(JSON.parse(r.value), { id: 'abc', kind: 'flood', severity: 'red', lat: 12.97, lon: 77.59, note: 'water rising', reported_at: 1_700_000_000_000 });
  assert.throws(() => buildIncident({ ...GOOD, lat: '200' }), /invalid report/);
  assert.match(newId(), /^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/);
  assert.match(newId({}), /^[0-9a-f]{8}-/, 'a browser without crypto.randomUUID still gets an id');
  assert.notEqual(newId(), newId());
});

test('reports on this device are listed newest first and junk records are skipped', () => {
  const m = new LWWMap();
  const stamp = (n) => ({ wall: n, counter: 0, node: 'a' });
  for (const [id, at] of [['a', 100], ['b', 300], ['c', 200]]) m.set(`incident:${id}`, buildIncident(GOOD, { id, now: at }).value, stamp(at));
  m.set('incident:junk', 'not json', stamp(1));
  m.set('other:key', buildIncident(GOOD, { id: 'z', now: 999 }).value, stamp(2));
  m.delete('incident:c', stamp(500));
  assert.deepEqual(listIncidents(m).map((i) => i.id), ['b', 'a']);
  assert.equal(parseIncident('{"id":5}'), null);
  assert.equal(parseIncident('nope'), null);
});

test('the banner says what is true: offline first, then sending, retrying, or all sent', () => {
  assert.equal(syncStatus({ online: false, pending: 0 }).level, 'offline');
  assert.match(syncStatus({ online: false, pending: 2 }).text, /2 changes saved on this device/);
  assert.match(syncStatus({ online: false, pending: 1 }).text, /1 change saved/);
  assert.equal(syncStatus({ online: true, pending: 3 }).level, 'pending');
  assert.equal(syncStatus({ online: true, pending: 3, error: new Error('x') }).level, 'error');
  assert.match(syncStatus({ online: true, pending: 3, error: new Error('x') }).text, /safe on this device/);
  assert.deepEqual(syncStatus({ online: true, pending: 0, error: new Error('x') }), { level: 'synced', text: 'All changes sent.' }, 'an old error with nothing left to send is not shown');
});

// ---------------------------------------------------------------------------------------------------------------------------- feed
test('the feed keeps the newest entries first and drops the oldest', () => {
  const f = new Feed(3);
  for (const t of ['a', 'b', 'c', 'd']) f.push({ text: t });
  assert.deepEqual(f.entries.map((e) => e.text), ['d', 'c', 'b']);
  f.clear();
  assert.deepEqual(f.entries, []);
  assert.throws(() => new Feed(0), RangeError);
  assert.equal(describeFrame({ unit_id: 'u1', lat: 12.97, lon: 77.59 }), 'u1 at 12.9700, 77.5900');
  assert.match(describeEvent({ event_type: 'IncidentReported', payload: { kind: 'flood', severity: 'red' } }), /flood.*red/);
  assert.match(describeEvent({ event_type: 'UnitDispatched', payload: { unit_id: 'u', incident_id: 'i' } }), /Unit u dispatched to incident i/);
  assert.match(describeEvent({ event_type: 'RoadBlocked', payload: { edge_id: 'e1' } }), /e1/);
  assert.match(describeEvent({ event_type: 'HazardPolygonAdded', payload: {} }), /Hazard added/);
  assert.match(describeEvent({ event_type: 'Strange' }), /Strange/);
});

// ---------------------------------------------------------------------------------------------------------------------------- connection
class FakeSocket {
  static all = [];

  constructor(url) { this.url = url; this.sent = []; this.closed = false; FakeSocket.all.push(this); }

  send(m) { this.sent.push(m); }

  close() { this.closed = true; this.onclose?.(); }
}

function rig(extra = {}) {
  FakeSocket.all = [];
  const states = [];
  const messages = [];
  const timers = [];
  const c = new LiveConnection({
    url: 'ws://x/ws', WebSocketImpl: FakeSocket, onMessage: (t) => messages.push(t), onState: (s) => states.push(s),
    timers: { set: (fn, ms) => { timers.push({ fn, ms }); return timers.length; }, clear: (h) => { timers[h - 1].cleared = true; } }, ...extra,
  });
  return { c, states, messages, timers };
}

test('the connection opens, delivers messages, and sends the current viewport once it is open', () => {
  const { c, states, messages } = rig();
  c.start();
  const ws = FakeSocket.all[0];
  c.send('{"type":"viewport","cells":[1]}'); // before the socket opened: remembered, not lost
  assert.deepEqual(ws.sent, []);
  ws.onopen();
  assert.deepEqual(ws.sent, ['{"type":"viewport","cells":[1]}']);
  c.send('{"type":"viewport","cells":[2]}');
  assert.equal(ws.sent.length, 2);
  ws.onmessage({ data: 'hello' });
  assert.deepEqual(messages, ['hello']);
  assert.deepEqual(states, ['connecting', 'open']);
});

test('after a drop it reconnects with growing pauses and re-sends the LATEST viewport, not the first', () => {
  const { c, timers } = rig({ delays: [10, 20, 40] });
  c.start();
  FakeSocket.all[0].onopen();
  c.send('v1');
  c.send('v2');
  FakeSocket.all[0].onclose();
  assert.equal(timers[0].ms, 10);
  timers[0].fn();
  FakeSocket.all[1].onclose(); // fails before opening
  assert.equal(timers[1].ms, 20);
  timers[1].fn();
  FakeSocket.all[2].onclose();
  assert.equal(timers[2].ms, 40);
  timers[2].fn();
  FakeSocket.all[3].onclose();
  assert.equal(timers[3].ms, 40, 'the last pause repeats');
  timers[3].fn();
  FakeSocket.all[4].onopen();
  assert.deepEqual(FakeSocket.all[4].sent, ['v2']);
  FakeSocket.all[4].onclose();
  assert.equal(timers[4].ms, 10, 'a successful connection resets the pauses');
});

test('stopping closes the socket, cancels the retry and ignores late events from the old socket', () => {
  const { c, states, messages, timers } = rig();
  c.start();
  const ws = FakeSocket.all[0];
  ws.onopen();
  ws.onclose();
  assert.equal(timers.length, 1);
  c.stop();
  assert.equal(timers[0].cleared, true);
  assert.equal(states.at(-1), 'closed');
  ws.onmessage({ data: 'late' });
  assert.deepEqual(messages, []);
  const again = rig();
  again.c.start();
  const s = FakeSocket.all[0];
  again.c.stop();
  assert.equal(s.closed, true);
  assert.equal(again.timers.length, 0, 'a deliberate stop must not schedule a reconnect');
});

// ---------------------------------------------------------------------------------------------------------------------------- events for time travel
const EVT = JSON.stringify([
  { sequence_id: 1, event_type: 'IncidentReported', payload: { id: 'i1', kind: 'flood', severity: 'red', lat: 1, lon: 2, h3_cell: 618683397703204863, reported_at: 5 } },
  { sequence_id: 2, event_type: 'RoadBlocked', payload: { edge_id: 'e' } },
]).replace('"h3_cell":"', '"h3_cell":');

test('the event log loads into a timeline, and a missing or broken endpoint is reported, never thrown', async () => {
  const ok = await fetchTimeline('/api', { fetchImpl: async (url, init) => { assert.equal(url, '/api/events?after=0&limit=5000'); assert.deepEqual(init.headers, { authorization: 'Bearer t' }); return { ok: true, text: async () => EVT }; }, headers: { authorization: 'Bearer t' } });
  assert.equal(ok.status, 'ready');
  assert.equal(ok.timeline.lastSeq, 2);
  assert.equal(ok.timeline.seek(1).incidents.i1.status, 'open');
  const missing = await fetchTimeline('/api', { fetchImpl: async () => ({ ok: false, status: 404 }) });
  assert.deepEqual(missing, { status: 'unavailable', reason: 'the events endpoint answered 404' });
  const down = await fetchTimeline('/api', { fetchImpl: async () => { throw new Error('network down'); } });
  assert.equal(down.status, 'unavailable');
  assert.match(down.reason, /network down/);
  const broken = await fetchTimeline('/api', { fetchImpl: async () => ({ ok: true, text: async () => '{"not":"an array"}' }) });
  assert.equal(broken.status, 'unavailable');
  const gap = await fetchTimeline('/api', { fetchImpl: async () => ({ ok: true, text: async () => JSON.stringify([{ sequence_id: 2, event_type: 'RoadBlocked', payload: { edge_id: 'e' } }]) }) });
  assert.match(gap.reason, /without gaps/);
});

// ---------------------------------------------------------------------------------------------------------------------------- map bounds
test('map bounds are wrapped and clamped into a rectangle the server accepts', () => {
  const b = (n, s, e, w) => ({ getNorth: () => n, getSouth: () => s, getEast: () => e, getWest: () => w });
  assert.deepEqual(fromMapBounds(b(13, 12, 78, 77)), { north: 13, south: 12, east: 78, west: 77 });
  assert.deepEqual(fromMapBounds(b(89, -89, 78, 77)), { north: 85, south: -85, east: 78, west: 77 });
  assert.deepEqual(fromMapBounds(b(1, -1, 190, 170)), { north: 1, south: -1, east: -170, west: 170 }, 'panned past the antimeridian: wrapped');
  assert.deepEqual(fromMapBounds(b(1, -1, 400, -20)), { north: 1, south: -1, east: 180, west: -180 }, 'zoomed out to the whole world');
  assert.ok(viewportCells(fromMapBounds(b(1, -1, 190, 170)), { res: 2 }).length > 0);
});

// ---------------------------------------------------------------------------------------------------------------------------- the pages are wired to real code
function sources(dir) {
  return readdirSync(dir).flatMap((n) => {
    const p = join(dir, n);
    return statSync(p).isDirectory() ? sources(p) : /\.js$/.test(n) ? [p] : [];
  });
}

test('every relative import in the app resolves to a file, and every named import exists in that module', async () => {
  const files = [...sources(join(ROOT, 'app'))];
  assert.ok(files.length >= 6);
  for (const file of files) {
    const text = readFileSync(file, 'utf8');
    for (const m of text.matchAll(/import\s+(?:([\w{}\s,*]+?)\s+from\s+)?'(\.[^']+)'/g)) {
      const target = resolve(dirname(file), m[2]);
      if (!target.endsWith('.js')) continue;
      if (!target.includes('/lib/')) { assert.ok(statSync(target).isFile(), `${file} imports ${m[2]}, which does not exist`); continue; } // pages hold JSX: `next build` compiles them
      const mod = await import(target).catch((e) => assert.fail(`${file} imports ${m[2]}: ${e.message}`));
      for (const name of (m[1]?.match(/\{([^}]*)\}/)?.[1] ?? '').split(',').map((s) => s.trim().split(/\s+as\s+/)[0]).filter(Boolean)) {
        assert.ok(name in mod, `${file.replace(ROOT, '')} imports { ${name} } from ${m[2]}, which does not export it`);
      }
    }
  }
});

test('the service worker caches the pages the app really has, and the manifest points at files that exist', () => {
  const sw = readFileSync(join(ROOT, 'public/sw.js'), 'utf8');
  for (const route of [...sw.matchAll(/'(\/[\w/.-]*)'/g)].map((m) => m[1]).filter((r) => !r.includes('.') && r !== '/api/' && r !== '/sync/' && r !== '/ws')) {
    const page = route === '/' ? join(ROOT, 'app/page.js') : join(ROOT, 'app', route, 'page.js');
    assert.ok(statSync(page).isFile(), `${route} is cached by the service worker but has no page`);
  }
  const manifest = JSON.parse(readFileSync(join(ROOT, 'public/manifest.webmanifest'), 'utf8'));
  assert.ok(statSync(join(ROOT, 'public', manifest.icons[0].src)).isFile());
  assert.ok(statSync(join(ROOT, 'app', manifest.start_url, 'page.js')).isFile());
  const pkg = JSON.parse(readFileSync(join(ROOT, 'package.json'), 'utf8'));
  for (const dep of ['next', 'react', 'react-dom', 'maplibre-gl', '@deck.gl/core', '@deck.gl/layers', '@deck.gl/mapbox']) assert.ok(pkg.dependencies[dep], `${dep} must be a dependency`);
});


// ---------------------------------------------------------------------------------------------------------------------------- the gateway address and the token
test('the browser talks to one gateway address, and a bad one is refused', () => {
  assert.equal(gatewayUrl(undefined), DEFAULT_GATEWAY);
  assert.equal(gatewayUrl('https://gw.example.test///'), 'https://gw.example.test');
  assert.equal(apiBase('http://localhost:8080'), 'http://localhost:8080/api');
  for (const bad of ['localhost:8080', 'ftp://x', 'http://', '/api']) assert.throws(() => gatewayUrl(bad), /must start with http/, bad);
});

test('the WebSocket address swaps the scheme and carries the token in the query, URL-encoded, because a browser cannot set headers there', () => {
  assert.equal(wsUrl('http://localhost:8080'), 'ws://localhost:8080/ws');
  assert.equal(wsUrl('https://gw.example.test', 'a.b-c'), 'wss://gw.example.test/ws?access_token=a.b-c');
  assert.equal(wsUrl('http://localhost:8080', 'x y&z=1'), 'ws://localhost:8080/ws?access_token=x%20y%26z%3D1');
});

test('the token is read from storage when there is one, and a missing or blocked storage is just "no token"', () => {
  assert.equal(readToken({ getItem: (k) => (k === TOKEN_KEY ? 'tok' : null) }), 'tok');
  assert.equal(readToken({ getItem: () => null }), null);
  assert.equal(readToken({ getItem: () => '' }), null);
  assert.equal(readToken({ getItem: () => { throw new Error('blocked'); } }), null);
  assert.equal(readToken(undefined), null, 'no sessionStorage in this environment');
  assert.deepEqual(authHeaders('tok'), { authorization: 'Bearer tok' });
  assert.deepEqual(authHeaders(null), {});
});

// ---------------------------------------------------------------------------------------------------------------------------- loading a long log page by page
function logServer(total) {
  const events = Array.from({ length: total }, (_, i) => ({ sequence_id: i + 1, event_type: 'RoadBlocked', payload: { edge_id: `e${i + 1}` }, aggregate_id: 'a', tenant_id: 't' }));
  const asked = [];
  const fetchImpl = async (url) => {
    const q = new URL(url, 'http://x').searchParams;
    const after = Number(q.get('after'));
    const limit = Number(q.get('limit'));
    asked.push([after, limit]);
    return { ok: true, text: async () => JSON.stringify(events.filter((e) => e.sequence_id > after).slice(0, limit)) };
  };
  return { fetchImpl, asked };
}

test('a long event log is loaded page by page, with no gaps or repeats, and then scrubbed', async () => {
  const { fetchImpl, asked } = logServer(11);
  const r = await fetchTimeline('/api', { fetchImpl, page: 4 });
  assert.equal(r.status, 'ready');
  assert.equal(r.timeline.lastSeq, 11);
  assert.deepEqual(asked, [[0, 4], [4, 4], [8, 4]], 'each request continues where the last page ended');
  assert.deepEqual(Object.keys(r.timeline.seek(6).blockedEdges), ['e1', 'e2', 'e3', 'e4', 'e5', 'e6']);
});

test('a log that is an exact multiple of the page size needs one more (empty) request, and an empty log is fine', async () => {
  const exact = logServer(8);
  assert.equal((await fetchTimeline('/api', { fetchImpl: exact.fetchImpl, page: 4 })).timeline.lastSeq, 8);
  assert.deepEqual(exact.asked, [[0, 4], [4, 4], [8, 4]]);
  const empty = logServer(0);
  const r = await fetchTimeline('/api', { fetchImpl: empty.fetchImpl, page: 4 });
  assert.equal(r.status, 'ready');
  assert.equal(r.timeline.lastSeq, 0);
});

test('a failure on a later page is reported, and a log that is too large is not loaded whole', async () => {
  const big = logServer(50);
  assert.deepEqual(await fetchTimeline('/api', { fetchImpl: big.fetchImpl, page: 5, maxEvents: 12 }), { status: 'unavailable', reason: 'the log has more than 12 events: too many to load whole' });
  assert.ok(big.asked.length <= 4, 'it stops asking once the limit is passed');
  let n = 0;
  const flaky = async () => (++n === 1 ? { ok: true, text: async () => JSON.stringify([{ sequence_id: 1, event_type: 'RoadBlocked', payload: { edge_id: 'e' } }]) } : { ok: false, status: 503 });
  assert.deepEqual(await fetchTimeline('/api', { fetchImpl: flaky, page: 1 }), { status: 'unavailable', reason: 'the events endpoint answered 503' });
});
