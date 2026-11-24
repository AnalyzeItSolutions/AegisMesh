import { test } from 'node:test';
import assert from 'node:assert/strict';
import { webcrypto } from 'node:crypto';
import { authConfig, challengeFor, safeReturn, loginUrl, startLogin, finishLogin, getValidToken, saveTokens, logout, decodeClaims, AUTH_KEY, PKCE_KEY } from '../lib/auth.js';
import { TOKEN_KEY } from '../lib/gateway.js';

const memory = () => {
  const m = new Map();
  return { getItem: (k) => (m.has(k) ? m.get(k) : null), setItem: (k, v) => m.set(k, String(v)), removeItem: (k) => m.delete(k), m };
};
const cfg = authConfig({ origin: 'http://localhost:3000' });
const reply = (status, body) => async () => ({ ok: status >= 200 && status < 300, status, json: async () => body });

test('the PKCE challenge matches the RFC 7636 test vector', async () => {
  assert.equal(await challengeFor('dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk', webcrypto.subtle), 'E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM');
});

test('only same-site paths are valid return targets', () => {
  assert.equal(safeReturn('/command'), '/command');
  for (const bad of ['//evil.com', 'https://evil.com', '/\\evil.com', 'javascript:x', undefined, '/a\nb']) assert.equal(safeReturn(bad), '/field');
});

test('login url carries S256 challenge, state and redirect', () => {
  const u = new URL(loginUrl(cfg, { challenge: 'c', state: 's' }));
  assert.equal(u.pathname, '/realms/app/protocol/openid-connect/auth');
  assert.equal(u.searchParams.get('code_challenge_method'), 'S256');
  assert.equal(u.searchParams.get('redirect_uri'), 'http://localhost:3000/auth/callback');
  assert.equal(u.searchParams.get('state'), 's');
});

test('login round trip stores tokens; a replay or a wrong state is refused', async () => {
  const storage = memory();
  let went;
  await startLogin(cfg, { storage, assign: (u) => (went = u), returnTo: '/command' });
  const state = new URL(went).searchParams.get('state');
  let sent;
  const fetchImpl = async (url, init) => ((sent = new URLSearchParams(init.body)), { ok: true, status: 200, json: async () => ({ access_token: 'A', refresh_token: 'R', expires_in: 300 }) });
  const back = await finishLogin(cfg, { search: `?code=abc&state=${state}`, storage, fetchImpl, now: () => 1000 });
  assert.equal(back, '/command');
  assert.equal(sent.get('code_verifier').length > 40, true);
  assert.equal(storage.getItem(TOKEN_KEY), 'A');
  assert.equal(JSON.parse(storage.getItem(AUTH_KEY)).expires_at, 301000);
  await assert.rejects(finishLogin(cfg, { search: `?code=abc&state=${state}`, storage, fetchImpl }), /no sign-in in progress/);
  await startLogin(cfg, { storage, assign: () => {} });
  await assert.rejects(finishLogin(cfg, { search: '?code=abc&state=other', storage, fetchImpl }), /state mismatch/);
  assert.equal(storage.getItem(PKCE_KEY), null);
});

test('a server refusal and an error reply are readable errors', async () => {
  const storage = memory();
  await startLogin(cfg, { storage, assign: () => {} });
  const { state } = JSON.parse(storage.getItem(PKCE_KEY));
  await assert.rejects(finishLogin(cfg, { search: `?code=x&state=${state}`, storage, fetchImpl: reply(400, { error_description: 'Code not valid' }) }), /Code not valid/);
  await assert.rejects(finishLogin(cfg, { search: '?error=access_denied', storage }), /access_denied/);
});

test('token is renewed shortly before expiry, once for concurrent callers', async () => {
  const storage = memory();
  saveTokens(storage, { access_token: 'old', refresh_token: 'R1', expires_in: 300 }, 0);
  let calls = 0;
  const fetchImpl = async () => (calls++, { ok: true, status: 200, json: async () => ({ access_token: 'new', refresh_token: 'R2', expires_in: 300 }) });
  assert.equal(await getValidToken(cfg, { storage, fetchImpl, now: () => 100_000 }), 'old');
  assert.equal(calls, 0);
  const [a, b] = await Promise.all([getValidToken(cfg, { storage, fetchImpl, now: () => 280_000 }), getValidToken(cfg, { storage, fetchImpl, now: () => 280_000 })]);
  assert.deepEqual([a, b, calls], ['new', 'new', 1]);
  assert.equal(JSON.parse(storage.getItem(AUTH_KEY)).refresh_token, 'R2');
});

test('rejected refresh signs out; offline keeps the session; no session is null', async () => {
  const dead = memory();
  saveTokens(dead, { access_token: 'old', refresh_token: 'R', expires_in: 10 }, 0);
  assert.equal(await getValidToken(cfg, { storage: dead, fetchImpl: reply(400, { error: 'invalid_grant' }), now: () => 50_000 }), null);
  assert.equal(dead.getItem(TOKEN_KEY), null);
  const off = memory();
  saveTokens(off, { access_token: 'old', refresh_token: 'R', expires_in: 10 }, 0);
  assert.equal(await getValidToken(cfg, { storage: off, fetchImpl: async () => { throw new TypeError('offline'); }, now: () => 50_000 }), 'old');
  assert.equal(await getValidToken(cfg, { storage: memory() }), null);
});

test('logout clears tokens and goes to the end-session url with the id token hint', () => {
  const storage = memory();
  saveTokens(storage, { access_token: 'A', refresh_token: 'R', id_token: 'I', expires_in: 300 }, 0);
  let went;
  logout(cfg, { storage, assign: (u) => (went = u) });
  assert.equal(storage.getItem(TOKEN_KEY), null);
  assert.equal(new URL(went).searchParams.get('id_token_hint'), 'I');
});

test('claims decode for display, garbage gives null', () => {
  const p = Buffer.from(JSON.stringify({ preferred_username: 'alice' })).toString('base64url');
  assert.equal(decodeClaims(`h.${p}.s`).preferred_username, 'alice');
  assert.equal(decodeClaims('nope'), null);
});

import { LiveConnection } from '../lib/connection.js';
import { fetchTransport } from '../lib/syncclient.js';

test('a header function is asked for every request, a url function for every connect', async () => {
  let n = 0;
  const seen = [];
  const t = fetchTransport('/api', async () => ({ authorization: `Bearer t${++n}` }), async (u, init) => (seen.push(init.headers.authorization), { ok: true, text: async () => '{}' }));
  await t.push('{}');
  await t.pull('{}');
  assert.deepEqual(seen, ['Bearer t1', 'Bearer t2']);

  const urls = [];
  class WS { constructor(u) { urls.push(u); } send() {} close() {} }
  let k = 0;
  const c = new LiveConnection({ url: async () => `ws://x/ws?access_token=t${++k}`, WebSocketImpl: WS, onMessage() {}, timers: { set: (f) => f, clear() {} } });
  c.start();
  await new Promise((r) => setTimeout(r, 5));
  c.socket.onclose();
  await c.timer();
  await new Promise((r) => setTimeout(r, 5));
  c.stop();
  assert.deepEqual(urls, ['ws://x/ws?access_token=t1', 'ws://x/ws?access_token=t2']);
});
