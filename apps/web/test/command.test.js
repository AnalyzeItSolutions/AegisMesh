import assert from 'node:assert/strict';
import { test } from 'node:test';

import { latLngToCell } from 'h3-js';

import { buildLayers, hazardLayer, incidentLayer, SEVERITY_COLOR, STALE_AFTER_MS, STALE_UNIT_COLOR, UNIT_COLOR, unitLayer } from '../lib/layers.js';
import { MarkerStore, parseServerMessage } from '../lib/markers.js';
import { applyEvent, emptyState, parseEvents, replayTo, Timeline } from '../lib/replay.js';
import { cellToWire, chooseResolution, MAX_CELLS, viewportCells, viewportMessage, ViewportSubscription, wireToCell } from '../lib/viewport.js';

// Real output of the Go services: the telemetry WebSocket's messages and the dispatch service's events (h3_cell near 6.2e17: beyond a JS number).
const GOLD_FRAME = '{"type":"frame","frame":{"unit_id":"u1","lat":12.97,"lon":77.59,"h3_cell":618683397703204863,"heading":90.5,"ts":1700000000000}}';
const GOLD_SNAPSHOT = '{"type":"snapshot","frames":[{"unit_id":"u1","lat":12.97,"lon":77.59,"h3_cell":618683397703204863,"heading":90.5,"ts":1700000000000}]}';
const GOLD_EVENTS = '[{"tenant_id":"11111111-1111-4111-8111-111111111111","sequence_id":1,"aggregate_id":"aaaaaaaa-0000-4000-8000-000000000001","event_type":"IncidentReported","payload":{"h3_cell":618683397703204863,"id":"aaaaaaaa-0000-4000-8000-000000000001","kind":"flood","lat":12.97,"lon":77.59,"reported_at":1700000000000,"severity":"red"},"actor_id":"dispatcher-1","prev_hash":"0000000000000000000000000000000000000000000000000000000000000000","entry_hash":"71bc45a349bcb179a20f9aa4ac5a4b29c34e81750e166a436ae3aab83f74d965"},{"tenant_id":"11111111-1111-4111-8111-111111111111","sequence_id":2,"aggregate_id":"aaaaaaaa-0000-4000-8000-000000000001","event_type":"UnitDispatched","payload":{"incident_id":"aaaaaaaa-0000-4000-8000-000000000001","unit_id":"bbbbbbbb-0000-4000-8000-000000000001"},"actor_id":"dispatcher-1","prev_hash":"71bc45a349bcb179a20f9aa4ac5a4b29c34e81750e166a436ae3aab83f74d965","entry_hash":"f2d128d58f185e4099090122453cf547a3a853105b4311f41beec91a23784b8b"},{"tenant_id":"11111111-1111-4111-8111-111111111111","sequence_id":3,"aggregate_id":"cccccccc-0000-4000-8000-000000000001","event_type":"HazardPolygonAdded","payload":{"id":"cccccccc-0000-4000-8000-000000000001","kind":"flood","polygon":[[77.5,12.9],[77.6,12.9],[77.6,13]]},"actor_id":"dispatcher-1","prev_hash":"f2d128d58f185e4099090122453cf547a3a853105b4311f41beec91a23784b8b","entry_hash":"1877c33b9c049356fbab1ac657d23e8c3063fc9d0c380e7ae9ec290a3aa90f6c"},{"tenant_id":"11111111-1111-4111-8111-111111111111","sequence_id":4,"aggregate_id":"aaaaaaaa-0000-4000-8000-000000000001","event_type":"RoadBlocked","payload":{"edge_id":"edge-42"},"actor_id":"dispatcher-1","prev_hash":"1877c33b9c049356fbab1ac657d23e8c3063fc9d0c380e7ae9ec290a3aa90f6c","entry_hash":"d885ffa656e9392edb99f6069ffbf2fa1e6d845a6f4647ea345763d855e5911d"}]';
const GO_CELL = '618683397703204863'; // the cell the Go telemetry service computes for 12.97, 77.59 at resolution 9

const CITY = { north: 12.99, south: 12.95, east: 77.62, west: 77.57 };

// ---------------------------------------------------------------------------------------------------------------------------- viewport
test('a smaller viewport gets a finer resolution, never finer than 9, and always within the cell limit', () => {
  const sizes = [0.02, 0.1, 0.5, 2, 8, 30].map((d) => ({ north: 13 + d, south: 13, east: 77.5 + d, west: 77.5 }));
  const res = sizes.map((b) => chooseResolution(b));
  assert.deepEqual([...res].sort((a, b) => b - a), res, 'resolution must not get finer as the viewport grows');
  assert.ok(res[0] <= 9 && res[res.length - 1] < res[0]);
  for (const b of sizes) assert.ok(viewportCells(b).length <= MAX_CELLS);
});

test('every point inside the viewport falls in one of its cells, including points hugging the edge', () => {
  for (const b of [CITY, { north: 13.4, south: 12.6, east: 78.0, west: 77.2 }]) {
    const res = chooseResolution(b);
    const cells = new Set(viewportCells(b));
    let seed = 7;
    const rnd = () => { seed = (seed * 1103515245 + 12345) % 2147483648; return seed / 2147483648; };
    for (let i = 0; i < 400; i++) {
      const edge = i % 4;
      const lat = edge === 0 ? b.south : edge === 1 ? b.north : b.south + rnd() * (b.north - b.south);
      const lon = edge === 2 ? b.west : edge === 3 ? b.east : b.west + rnd() * (b.east - b.west);
      assert.ok(cells.has(latLngToCell(lat, lon, res)), `${lat},${lon} is not covered`);
    }
  }
});

test('asking for more cells than the limit is refused instead of sent', () => {
  assert.throws(() => viewportCells({ north: 20, south: 10, east: 80, west: 70 }, { res: 9 }), RangeError);
  assert.throws(() => viewportCells(CITY, { res: 12 }), RangeError);
  assert.throws(() => viewportCells(CITY, { maxCells: 3, res: 9 }), RangeError);
});

test('a viewport across the antimeridian covers both sides', () => {
  const cells = viewportCells({ north: 1, south: -1, east: -179.5, west: 179.5 }, { res: 6 });
  const lons = cells.map((c) => c).length;
  assert.ok(lons > 0);
  assert.ok(cells.includes(latLngToCell(0, 179.8, 6)) && cells.includes(latLngToCell(0, -179.8, 6)));
  assert.ok(!cells.includes(latLngToCell(0, 0, 6)), 'the long way round must not be included');
});

test('bad bounds are refused', () => {
  for (const bad of [null, {}, { north: 1, south: 2, east: 1, west: 0 }, { north: 91, south: 0, east: 1, west: 0 }, { north: 2, south: 1, east: 181, west: 0 }, { north: 'a', south: 1, east: 1, west: 0 }]) {
    assert.throws(() => viewportCells(bad), undefined, JSON.stringify(bad));
  }
});

test('cell ids match what the Go service computes, and the subscription message carries exact digits', () => {
  const hex = latLngToCell(12.97, 77.59, 9);
  assert.equal(cellToWire(hex), GO_CELL);
  assert.equal(wireToCell(GO_CELL), hex);
  const msg = viewportMessage([hex, latLngToCell(12.971, 77.591, 9)]);
  assert.ok(msg.includes(GO_CELL), 'the exact 18-digit id must appear in the message');
  const back = JSON.parse(msg.replace(/(\d{16,})/g, '"$1"')).cells.map(wireToCell);
  assert.deepEqual(new Set(back), new Set([hex, latLngToCell(12.971, 77.591, 9)]));
  assert.notEqual(String(JSON.parse(msg).cells[0]), GO_CELL, 'plain JSON.parse would have rounded it');
});

test('the subscription is sent when the viewport changes, not when it merely repeats, and again after a reconnect', () => {
  const sent = [];
  const sub = new ViewportSubscription({ send: (m) => sent.push(m) });
  assert.equal(sub.update(CITY).sent, true);
  assert.equal(sub.update({ ...CITY }).sent, false);
  assert.equal(sent.length, 1);
  const panned = { ...CITY, east: CITY.east + 0.05, west: CITY.west + 0.05 };
  assert.equal(sub.update(panned).sent, true);
  assert.equal(sent.length, 2);
  assert.notEqual(sent[0], sent[1]);
  sub.reset();
  assert.equal(sub.update(panned).sent, true, 'the server forgot the viewport on reconnect');
  assert.throws(() => sub.update({ north: 1, south: 2, east: 1, west: 0 }));
  assert.equal(sent.length, 3, 'a bad viewport sends nothing');
  assert.ok(sent.every((m) => JSON.parse(m.replace(/(\d{16,})/g, '"$1"')).type === 'viewport'));
});

// ---------------------------------------------------------------------------------------------------------------------------- markers
const frame = (id, ts, lat = 12.97, lon = 77.59) => ({ unit_id: id, lat, lon, heading: 0, ts, h3: null });

test('server messages keep the exact h3 cell', () => {
  const f = parseServerMessage(GOLD_FRAME);
  assert.equal(f.type, 'frame');
  assert.equal(f.frame.unit_id, 'u1');
  assert.equal(f.frame.h3, latLngToCell(12.97, 77.59, 9));
  assert.equal(cellToWire(f.frame.h3), GO_CELL);
  const s = parseServerMessage(GOLD_SNAPSHOT);
  assert.equal(s.type, 'snapshot');
  assert.equal(s.frames[0].h3, f.frame.h3);
  assert.deepEqual(parseServerMessage('{"type":"error","message":"bad cell"}'), { type: 'error', message: 'bad cell' });
  for (const bad of ['{"type":"nope"}', '[]', 'x', '{"type":"frame"}']) assert.throws(() => parseServerMessage(bad), undefined, bad);
});

test('the store keeps the newest position of each unit and ignores stale and invalid frames', () => {
  const m = new MarkerStore();
  assert.equal(m.upsert(frame('u1', 100, 1, 1)), true);
  assert.equal(m.upsert(frame('u1', 90, 9, 9)), false, 'older frame');
  assert.deepEqual([m.get('u1').lat, m.get('u1').lon], [1, 1]);
  assert.equal(m.upsert(frame('u1', 100, 2, 2)), true, 'an equal timestamp replaces, like the server');
  assert.equal(m.get('u1').lat, 2);
  for (const bad of [frame('', 1), frame('u', 1, 91, 0), frame('u', 1, 0, 181), { unit_id: 'u', lat: NaN, lon: 0, ts: 1 }, { unit_id: 'u', lat: 0, lon: 0 }, null]) {
    assert.equal(m.upsert(bad), false);
  }
  assert.equal(m.size, 1);
  assert.equal(m.applyAll([frame('a', 1), frame('b', 1), frame('a', 0)]), 2);
  assert.equal(m.remove('a'), true);
  assert.equal(m.remove('a'), false);
  assert.equal(m.size, 2);
});

test('within() and prune() select by position and by age, including across the antimeridian', () => {
  const m = new MarkerStore();
  m.applyAll([frame('in', 10, 12.97, 77.59), frame('out', 10, 40, 10), frame('east', 10, 0, 179.8), frame('west', 10, 0, -179.8)]);
  assert.deepEqual(m.within(CITY).map((u) => u.unit_id), ['in']);
  assert.deepEqual(m.within({ north: 1, south: -1, east: -179, west: 179 }).map((u) => u.unit_id).sort(), ['east', 'west']);
  m.upsert(frame('fresh', 100_000));
  assert.equal(m.prune(100_000, 50_000), 4);
  assert.deepEqual(m.toArray().map((u) => u.unit_id), ['fresh']);
});

test('25,000 markers: loading, moving them all, and drawing from the cached array stay fast and correct', () => {
  const m = new MarkerStore();
  const N = 25_000;
  const t0 = performance.now();
  for (let i = 0; i < N; i++) m.upsert(frame(`unit-${i}`, 1000, 12 + (i % 500) * 0.001, 77 + Math.floor(i / 500) * 0.001));
  assert.equal(m.size, N);
  const first = m.toArray();
  assert.equal(first.length, N);
  assert.equal(m.toArray(), first, 'an unchanged store hands out the same array');
  for (let i = 0; i < N; i++) m.upsert(frame(`unit-${i}`, 2000, 13, 78));
  assert.equal(m.size, N);
  assert.notEqual(m.toArray(), first, 'a change must produce a new array');
  assert.ok(m.toArray().every((u) => u.ts === 2000 && u.lat === 13));
  const layer = unitLayer(m.toArray(), { now: 2500 });
  assert.equal(layer.props.data.length, N);
  assert.ok(performance.now() - t0 < 5000, 'loading, moving and drawing 25,000 markers took too long');
});

// ---------------------------------------------------------------------------------------------------------------------------- layers
test('the layers are drawn hazards, then incidents, then units, with the right positions and colours', () => {
  const units = [{ unit_id: 'u1', lat: 12.97, lon: 77.59, ts: 1_000_000 }, { unit_id: 'u2', lat: 1, lon: 2, ts: 1_000_000 - STALE_AFTER_MS - 1 }];
  const incidents = [{ id: 'i1', lat: 10, lon: 20, severity: 'red', status: 'open' }, { id: 'i2', lat: 11, lon: 21, severity: 'black', status: 'dispatched' }, { id: 'i3', lat: 0, lon: 0, severity: '??', status: 'open' }];
  const hazards = [{ id: 'h1', polygon: [[77.5, 12.9], [77.6, 12.9], [77.6, 13]], active: true }, { id: 'h2', polygon: [[0, 0], [1, 0], [1, 1]], active: false }];
  const layers = buildLayers({ units, incidents, hazards, now: 1_000_000, selectedId: 'u2' });
  assert.deepEqual(layers.map((l) => l.props.id), ['hazards', 'incidents', 'units']);
  assert.deepEqual(layers.map((l) => l.type), ['PolygonLayer', 'ScatterplotLayer', 'ScatterplotLayer']);
  const [h, i, u] = layers.map((l) => l.props);
  assert.deepEqual(u.getPosition(units[0]), [77.59, 12.97], 'positions are [lon, lat]');
  assert.deepEqual(u.getFillColor(units[0]), UNIT_COLOR);
  assert.deepEqual(u.getFillColor(units[1]), STALE_UNIT_COLOR, 'a unit not heard from for a minute is grey');
  assert.deepEqual([u.getRadius(units[0]), u.getRadius(units[1])], [5, 9], 'the selected unit is drawn larger');
  assert.deepEqual(i.getFillColor(incidents[0]), SEVERITY_COLOR.red);
  assert.deepEqual(i.getFillColor(incidents[1]), SEVERITY_COLOR.black);
  assert.equal(i.getFillColor(incidents[2])[3], 255, 'an unknown severity still gets a colour');
  assert.ok(i.getRadius(incidents[0]) > i.getRadius(incidents[1]), 'an open incident is drawn larger than a dispatched one');
  assert.deepEqual(h.getPolygon(hazards[0]), hazards[0].polygon);
  assert.notDeepEqual(h.getFillColor(hazards[0]), h.getFillColor(hazards[1]));
  assert.deepEqual(h.getLineColor(hazards[0]).slice(0, 3), [220, 38, 38]);
});

test('layers pass the store array through untouched and refresh colours only when they could have changed', () => {
  const m = new MarkerStore();
  m.upsert(frame('u1', 5));
  const data = m.toArray();
  const a = unitLayer(data, { now: 1000, selectedId: null }).props;
  assert.equal(a.data, data, 'deck.gl compares data by identity: it must not be copied');
  const sameBucket = unitLayer(data, { now: 1000 + 5000, selectedId: null }).props;
  const nextBucket = unitLayer(data, { now: 1000 + 10_000, selectedId: null }).props;
  const selected = unitLayer(data, { now: 1000, selectedId: 'u1' }).props;
  assert.deepEqual(sameBucket.updateTriggers.getFillColor, a.updateTriggers.getFillColor);
  assert.notDeepEqual(nextBucket.updateTriggers.getFillColor, a.updateTriggers.getFillColor);
  assert.notDeepEqual(selected.updateTriggers.getRadius, a.updateTriggers.getRadius);
  assert.equal(buildLayers().length, 3, 'with nothing to show the three layers still exist, so ids stay stable');
  assert.equal(incidentLayer([]).props.data.length + hazardLayer([]).props.data.length, 0);
});

// ---------------------------------------------------------------------------------------------------------------------------- replay
const EVENTS = parseEvents(GOLD_EVENTS);

test('the real events from the dispatch service parse with their exact h3 cell', () => {
  assert.equal(EVENTS.length, 4);
  assert.deepEqual(EVENTS.map((e) => e.event_type), ['IncidentReported', 'UnitDispatched', 'HazardPolygonAdded', 'RoadBlocked']);
  assert.equal(cellToWire(replayTo(EVENTS, 1).incidents[EVENTS[0].payload.id].h3), GO_CELL);
});

test('replaying to a past sequence id shows the situation as it was then', () => {
  const inc = EVENTS[0].payload.id;
  const at = (n) => replayTo(EVENTS, n);
  assert.deepEqual(at(0), emptyState());
  assert.equal(at(1).incidents[inc].status, 'open');
  assert.deepEqual(Object.keys(at(1).units), []);
  assert.equal(at(2).incidents[inc].status, 'dispatched');
  assert.deepEqual(at(2).incidents[inc].unit_ids, [EVENTS[1].payload.unit_id]);
  assert.equal(at(2).units[EVENTS[1].payload.unit_id].status, 'dispatched');
  assert.deepEqual(Object.keys(at(2).hazards), [], 'the hazard did not exist yet at sequence 2');
  assert.equal(Object.keys(at(3).hazards).length, 1);
  assert.equal(at(3).hazards[EVENTS[2].payload.id].active, true);
  assert.deepEqual(at(3).blockedEdges, {});
  assert.deepEqual(at(4).blockedEdges, { 'edge-42': true });
  assert.equal(at(4).lastSeq, 4);
  assert.deepEqual(at(4), replayTo(EVENTS, 99), 'beyond the end is the final state');
});

test('events apply strictly in order: replays are ignored, gaps and unknown events are refused without changing anything', () => {
  const s = emptyState();
  applyEvent(s, EVENTS[0]);
  const before = structuredClone(s);
  applyEvent(s, EVENTS[0]);
  assert.deepEqual(s, before, 'a repeated event changes nothing');
  assert.throws(() => applyEvent(s, EVENTS[2]), /gap/);
  assert.throws(() => applyEvent(s, { ...EVENTS[1], event_type: 'Nope' }), /unknown event type/);
  assert.throws(() => applyEvent(s, { ...EVENTS[1], payload: { incident_id: '' } }), /UnitDispatched needs/);
  assert.throws(() => applyEvent(s, { ...EVENTS[1], sequence_id: 'x' }), /sequence_id/);
  assert.throws(() => applyEvent(s, { sequence_id: 2, event_type: 'HazardPolygonAdded', payload: { id: 'h', polygon: [[0, 0]] } }), /polygon/);
  assert.deepEqual(s, before, 'every refusal left the state exactly as it was');
});

function manyEvents(n) {
  const out = [];
  for (let i = 1; i <= n; i++) {
    const kind = i % 4;
    const base = { sequence_id: i, aggregate_id: `agg-${i}`, tenant_id: 't' };
    if (kind === 1) out.push({ ...base, event_type: 'IncidentReported', payload: { id: `inc-${i}`, kind: 'flood', severity: 'red', lat: 12 + i * 1e-4, lon: 77, h3_cell: 618683397703204863, reported_at: i } });
    else if (kind === 2) out.push({ ...base, event_type: 'UnitDispatched', payload: { incident_id: `inc-${i - 1}`, unit_id: `unit-${i}` } });
    else if (kind === 3) out.push({ ...base, event_type: 'RoadBlocked', payload: { edge_id: `edge-${i}` } });
    else out.push({ ...base, event_type: 'HazardPolygonAdded', payload: { id: `h-${i}`, kind: 'flood', polygon: [[0, 0], [1, 0], [1, 1]] } });
  }
  return out;
}

test('scrubbing a long log back and forth equals replaying from the start, and snapshots are never changed by a seek', () => {
  const events = manyEvents(3000);
  const tl = new Timeline(events, { every: 100 });
  assert.equal(tl.lastSeq, 3000);
  const probes = [0, 1, 99, 100, 101, 1234, 2999, 3000, 500, 17, 2500, 100, 3000, 0];
  for (const seq of probes) assert.deepEqual(tl.seek(seq), replayTo(events, seq), `seek(${seq})`);
  const s = tl.seek(1234);
  s.incidents['inc-1'].status = 'tampered';
  s.blockedEdges.junk = true;
  assert.deepEqual(tl.seek(1234), replayTo(events, 1234), 'changing a returned state must not leak into the timeline');
  assert.throws(() => tl.seek(3001), /seq must be/);
  assert.throws(() => tl.seek(-1), /seq must be/);
  assert.throws(() => tl.seek(1.5), /seq must be/);
});

test('a timeline refuses a log with gaps or repeats', () => {
  const e = manyEvents(10);
  assert.throws(() => new Timeline([e[0], e[2]]), /without gaps/);
  assert.throws(() => new Timeline([e[0], e[0]]), /without gaps/);
  assert.throws(() => new Timeline(e, { every: 0 }), /every/);
  assert.equal(new Timeline([]).lastSeq, 0);
});
