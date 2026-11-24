'use client';

import { useCallback, useEffect, useRef, useState } from 'react';
import { KINDS, SEVERITIES, buildIncident, listIncidents, newId, syncStatus, validateReport } from '../../lib/field.js';
import { openStore } from '../../lib/store.js';
import { SyncClient, fetchTransport } from '../../lib/syncclient.js';

const SYNC_URL = process.env.NEXT_PUBLIC_SYNC_URL ?? '/api';
const EMPTY = { kind: 'flood', severity: 'red', lat: '', lon: '', note: '' };

function replicaId() {
  try {
    let id = localStorage.getItem('aegismesh.replica');
    if (!id) {
      id = newId();
      localStorage.setItem('aegismesh.replica', id);
    }
    return id;
  } catch {
    return newId(); // private window: this session only
  }
}

// The login flow (Keycloak) is not part of this page: it is expected to leave the access token in sessionStorage.
function authHeaders() {
  try {
    const t = sessionStorage.getItem('aegismesh.token');
    return t ? { authorization: `Bearer ${t}` } : {};
  } catch {
    return {};
  }
}

export default function FieldPage() {
  const client = useRef(null);
  const [online, setOnline] = useState(true);
  const [pending, setPending] = useState(0);
  const [syncError, setSyncError] = useState(null);
  const [incidents, setIncidents] = useState([]);
  const [form, setForm] = useState(EMPTY);
  const [errors, setErrors] = useState({});
  const [note, setNote] = useState('');

  const refresh = useCallback(async () => {
    const c = client.current;
    if (!c) return;
    setIncidents(listIncidents(c.lww));
    setPending(await c.pending());
  }, []);

  const sync = useCallback(async () => {
    const c = client.current;
    if (!c || !navigator.onLine) return;
    const { pushed, pulled } = await c.sync();
    setSyncError(pushed.error || pulled.error || null);
    await refresh();
  }, [refresh]);

  useEffect(() => {
    let disposed = false;
    const goOnline = () => { setOnline(true); sync(); };
    const goOffline = () => setOnline(false);
    (async () => {
      const store = await openStore();
      const c = await new SyncClient({ store, transport: fetchTransport(SYNC_URL, authHeaders()), replicaId: replicaId(), online: () => navigator.onLine }).init();
      if (disposed) return;
      client.current = c;
      setOnline(navigator.onLine);
      await refresh();
      sync();
    })();
    window.addEventListener('online', goOnline);
    window.addEventListener('offline', goOffline);
    const timer = setInterval(sync, 15_000);
    return () => {
      disposed = true;
      window.removeEventListener('online', goOnline);
      window.removeEventListener('offline', goOffline);
      clearInterval(timer);
    };
  }, [refresh, sync]);

  const set = (field) => (e) => setForm((f) => ({ ...f, [field]: e.target.value }));

  function locateMe() {
    if (!navigator.geolocation) return setNote('This device cannot share its location: type the coordinates.');
    navigator.geolocation.getCurrentPosition(
      (p) => setForm((f) => ({ ...f, lat: p.coords.latitude.toFixed(6), lon: p.coords.longitude.toFixed(6) })),
      () => setNote('Location is not available: type the coordinates.'),
      { enableHighAccuracy: true, timeout: 10_000 },
    );
  }

  async function submit(e) {
    e.preventDefault();
    const { ok, errors: found } = validateReport(form);
    setErrors(found);
    if (!ok || !client.current) return;
    const rec = buildIncident(form);
    await client.current.set(rec.key, rec.value);
    await client.current.addToSet('incidents', rec.id);
    setForm(EMPTY);
    setNote('Saved on this device.');
    await refresh();
    sync();
  }

  const banner = syncStatus({ online, pending, error: syncError });

  return (
    <main>
      <h1>Report an incident</h1>
      <div className={`banner ${banner.level}`} role="status" aria-live="polite">{banner.text}</div>
      <form onSubmit={submit} noValidate>
        <label htmlFor="kind">What happened</label>
        <select id="kind" value={form.kind} onChange={set('kind')}>{KINDS.map((k) => <option key={k} value={k}>{k}</option>)}</select>
        {errors.kind && <p className="error-text">{errors.kind}</p>}
        <label htmlFor="severity">Severity</label>
        <select id="severity" value={form.severity} onChange={set('severity')}>{SEVERITIES.map((s) => <option key={s} value={s}>{s}</option>)}</select>
        {errors.severity && <p className="error-text">{errors.severity}</p>}
        <div className="row">
          <div>
            <label htmlFor="lat">Latitude</label>
            <input id="lat" inputMode="decimal" value={form.lat} onChange={set('lat')} />
            {errors.lat && <p className="error-text">{errors.lat}</p>}
          </div>
          <div>
            <label htmlFor="lon">Longitude</label>
            <input id="lon" inputMode="decimal" value={form.lon} onChange={set('lon')} />
            {errors.lon && <p className="error-text">{errors.lon}</p>}
          </div>
        </div>
        <button type="button" className="secondary" onClick={locateMe}>Use my location</button>
        <label htmlFor="note">Note (optional)</label>
        <textarea id="note" rows={3} value={form.note} onChange={set('note')} />
        {errors.note && <p className="error-text">{errors.note}</p>}
        <button type="submit">Report</button>
        {note && <p className="muted" role="status">{note}</p>}
      </form>
      <h2>Reported from this device</h2>
      {incidents.length === 0 ? <p className="muted">Nothing reported yet.</p> : (
        <ul className="list">
          {incidents.map((i) => (
            <li key={i.id}><strong>{i.kind}</strong> ({i.severity}) at {i.lat.toFixed(4)}, {i.lon.toFixed(4)}{i.note ? ` - ${i.note}` : ''}<br /><span className="muted">{new Date(i.reported_at).toLocaleString()}</span></li>
          ))}
        </ul>
      )}
    </main>
  );
}
