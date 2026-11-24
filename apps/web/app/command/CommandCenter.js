'use client';

import 'maplibre-gl/dist/maplibre-gl.css';
import { useEffect, useMemo, useRef, useState } from 'react';
import { fetchTimeline } from '../../lib/events.js';
import { Feed, describeEvent, describeFrame } from '../../lib/feed.js';
import { buildLayers } from '../../lib/layers.js';
import { LiveConnection } from '../../lib/connection.js';
import { MarkerStore, parseServerMessage } from '../../lib/markers.js';
import { emptyState } from '../../lib/replay.js';
import { ViewportSubscription, fromMapBounds } from '../../lib/viewport.js';

const WS_URL = process.env.NEXT_PUBLIC_WS_URL ?? 'ws://localhost:8084/ws';
const API_URL = process.env.NEXT_PUBLIC_API_URL ?? '/api';
const MAP_STYLE = process.env.NEXT_PUBLIC_MAP_STYLE ?? 'https://demotiles.maplibre.org/style.json'; // a public demo style: set your own tiles for real use
const START_VIEW = { center: [77.59, 12.97], zoom: 11 };
const STALE_UNIT_MS = 5 * 60_000;

// The login flow (Keycloak) is not part of this page: it leaves the access token in sessionStorage.
function authHeaders() {
  try {
    const t = sessionStorage.getItem('aegismesh.token');
    return t ? { authorization: `Bearer ${t}` } : {};
  } catch {
    return {};
  }
}

export default function CommandCenter() {
  const mapEl = useRef(null);
  const store = useRef(new MarkerStore());
  const feed = useRef(new Feed(30));
  const drawRef = useRef(() => {});
  const [conn, setConn] = useState('connecting');
  const [units, setUnits] = useState(0);
  const [entries, setEntries] = useState([]);
  const [problem, setProblem] = useState(null);
  const [timeline, setTimeline] = useState({ status: 'loading' });
  const [seq, setSeq] = useState(null); // null = the live end of the log
  const [selectedId, setSelectedId] = useState(null);

  // what the map shows for incidents and hazards: the latest state, or the state at the chosen past sequence id
  const view = useMemo(() => {
    if (timeline.status !== 'ready') return emptyState();
    const t = timeline.timeline;
    return t.seek(seq === null ? t.lastSeq : Math.min(seq, t.lastSeq));
  }, [timeline, seq]);
  const viewRef = useRef(view);
  viewRef.current = view;
  const selectedRef = useRef(selectedId);
  selectedRef.current = selectedId;

  useEffect(() => {
    drawRef.current();
  }, [view, selectedId]);

  useEffect(() => {
    let cancelled = false;
    fetchTimeline(API_URL, { headers: authHeaders() }).then((t) => !cancelled && setTimeline(t));
    return () => { cancelled = true; };
  }, []);

  useEffect(() => {
    let disposed = false;
    const cleanups = [];
    (async () => {
      const [{ default: maplibregl }, { MapboxOverlay }, deckLayers] = await Promise.all([import('maplibre-gl'), import('@deck.gl/mapbox'), import('@deck.gl/layers')]);
      if (disposed) return;
      const map = new maplibregl.Map({ container: mapEl.current, style: MAP_STYLE, ...START_VIEW });
      const overlay = new MapboxOverlay({ layers: [] });
      map.addControl(new maplibregl.NavigationControl());
      map.addControl(overlay);
      cleanups.push(() => map.remove());

      let raf = null;
      const draw = () => {
        raf = null;
        const v = viewRef.current;
        const layers = buildLayers({ units: store.current.toArray(), incidents: Object.values(v.incidents), hazards: Object.values(v.hazards), now: Date.now(), selectedId: selectedRef.current });
        overlay.setProps({ layers: layers.map((l) => new deckLayers[l.type](l.props)), onClick: (info) => setSelectedId(info?.object?.unit_id ?? null) });
        setUnits(store.current.size);
        setEntries([...feed.current.entries]);
      };
      const schedule = () => { if (raf === null) raf = requestAnimationFrame(draw); };
      drawRef.current = schedule;
      cleanups.push(() => { if (raf !== null) cancelAnimationFrame(raf); drawRef.current = () => {}; });

      const connection = new LiveConnection({
        url: WS_URL,
        onState: setConn,
        onMessage: (text) => {
          try {
            const m = parseServerMessage(text);
            if (m.type === 'frame') {
              if (store.current.upsert(m.frame)) feed.current.push({ at: Date.now(), kind: 'unit', text: describeFrame(m.frame) });
            } else if (m.type === 'snapshot') {
              store.current.applyAll(m.frames);
            } else if (m.type === 'error') {
              setProblem(m.message);
            }
            schedule();
          } catch (error) {
            setProblem(`unreadable message from the server: ${error.message}`);
          }
        },
      });
      const subscription = new ViewportSubscription({ send: (message) => connection.send(message) });
      const follow = () => {
        try {
          subscription.update(fromMapBounds(map.getBounds()));
          setProblem(null);
        } catch (error) {
          setProblem(`cannot subscribe to this view: ${error.message}`);
        }
      };
      map.on('load', follow);
      map.on('moveend', follow);
      connection.start();
      cleanups.push(() => connection.stop());
      const prune = setInterval(() => { if (store.current.prune(Date.now(), STALE_UNIT_MS)) schedule(); }, 30_000);
      cleanups.push(() => clearInterval(prune));
      schedule();
    })().catch((error) => setProblem(`the map could not start: ${error.message}`));
    return () => {
      disposed = true;
      cleanups.reverse().forEach((c) => c());
    };
  }, []);

  const last = timeline.status === 'ready' ? timeline.timeline.lastSeq : 0;
  const shown = seq === null ? last : Math.min(seq, last);
  const events = timeline.status === 'ready' ? timeline.timeline.events.slice(Math.max(0, shown - 5), shown).reverse() : [];

  return (
    <div className="cc">
      <div className="map" ref={mapEl} aria-label="Map of units, incidents and hazards" />
      <aside>
        <h1>Command center</h1>
        <p className="muted">Live feed: {conn === 'open' ? 'connected' : conn === 'connecting' ? 'connecting…' : 'disconnected, retrying'} · {units} unit{units === 1 ? '' : 's'} in view</p>
        {problem && <div className="banner error" role="alert">{problem}</div>}
        {selectedId && <p>Selected: <strong>{selectedId}</strong> <button type="button" className="secondary" onClick={() => setSelectedId(null)}>Clear</button></p>}
        <h2>Latest</h2>
        {entries.length === 0 ? <p className="muted">Waiting for units to report…</p> : (
          <ul className="list">{entries.slice(0, 12).map((e, i) => <li key={`${e.at}-${i}`}>{e.text}<br /><span className="muted">{new Date(e.at).toLocaleTimeString()}</span></li>)}</ul>
        )}
        {events.length > 0 && (
          <>
            <h2>Events up to {shown}</h2>
            <ul className="list">{events.map((ev) => <li key={ev.sequence_id}>#{ev.sequence_id} {describeEvent(ev)}</li>)}</ul>
          </>
        )}
      </aside>
      <div className="timebar">
        {timeline.status === 'ready' && last > 0 ? (
          <>
            <label htmlFor="seq">Time travel: event {shown} of {last}{seq === null ? ' (live)' : ''}</label>
            <input id="seq" type="range" min={0} max={last} value={shown} onChange={(e) => setSeq(Number(e.target.value) >= last ? null : Number(e.target.value))} />
            <button type="button" className="secondary" onClick={() => setSeq(null)} disabled={seq === null}>Back to live</button>
          </>
        ) : (
          <p className="muted">{timeline.status === 'loading' ? 'Loading the event log…' : timeline.status === 'ready' ? 'No events yet.' : `Time travel is unavailable: ${timeline.reason}.`}</p>
        )}
      </div>
    </div>
  );
}
