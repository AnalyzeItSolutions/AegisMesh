// Signing in: the OAuth2 authorization code flow with PKCE against Keycloak, from the browser, no client secret (the client is public).
//   startLogin  -> remembers a one-time secret and a state value, sends the user to Keycloak
//   finishLogin -> back on /auth/callback: checks the state, swaps the code for tokens using the secret, saves them
//   getValidToken -> the access token to put on a request; renews it shortly before it expires (access tokens last minutes)
// Tokens live in sessionStorage: they vanish when the tab closes, but script on the page can read them, so this app must never render untrusted HTML.
import { TOKEN_KEY } from './gateway.js';

export const AUTH_KEY = 'aegismesh.auth'; // {refresh_token, id_token, expires_at}
export const PKCE_KEY = 'aegismesh.pkce'; // {verifier, state, returnTo}: present only between startLogin and finishLogin
export const DEFAULT_ISSUER = 'http://localhost:8081/realms/app';
export const RENEW_BEFORE_MS = 30_000;

export class AuthError extends Error {}

export function authConfig({ issuer, clientId = 'web', origin }) {
  const iss = (issuer || DEFAULT_ISSUER).replace(/\/+$/, '');
  if (!/^https?:\/\/[^/\s]+/.test(iss)) throw new AuthError(`the sign-in address must start with http:// or https://, got "${iss}"`);
  if (!origin) throw new AuthError('the page origin is needed to build the sign-in redirect');
  return { issuer: iss, clientId, redirectUri: `${origin}/auth/callback`, origin };
}

const b64url = (bytes) => btoa(String.fromCharCode(...bytes)).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '');

export function randomString(bytes = 32, crypto = globalThis.crypto) {
  return b64url(crypto.getRandomValues(new Uint8Array(bytes)));
}

/** The PKCE challenge for a verifier: base64url(SHA-256(verifier)) (RFC 7636, method S256). */
export async function challengeFor(verifier, subtle = globalThis.crypto.subtle) {
  return b64url(new Uint8Array(await subtle.digest('SHA-256', new TextEncoder().encode(verifier))));
}

/** Only a path on THIS site is a valid place to return to: anything else (another site, a protocol-relative //host) would make the callback an open redirect. */
export function safeReturn(path, fallback = '/field') {
  return typeof path === 'string' && /^\/(?![/\\])/.test(path) && !/[\r\n]/.test(path) ? path : fallback;
}

export function loginUrl(cfg, { challenge, state }) {
  const q = new URLSearchParams({ client_id: cfg.clientId, redirect_uri: cfg.redirectUri, response_type: 'code', scope: 'openid', code_challenge: challenge, code_challenge_method: 'S256', state });
  return `${cfg.issuer}/protocol/openid-connect/auth?${q}`;
}

export async function startLogin(cfg, { storage, assign, returnTo }) {
  const verifier = randomString(48);
  const state = randomString(16);
  storage.setItem(PKCE_KEY, JSON.stringify({ verifier, state, returnTo: safeReturn(returnTo) }));
  assign(loginUrl(cfg, { challenge: await challengeFor(verifier), state }));
}

export function saveTokens(storage, t, now = Date.now()) {
  storage.setItem(TOKEN_KEY, t.access_token);
  storage.setItem(AUTH_KEY, JSON.stringify({ refresh_token: t.refresh_token ?? null, id_token: t.id_token ?? null, expires_at: now + Number(t.expires_in ?? 0) * 1000 }));
}

export function clearTokens(storage) {
  storage.removeItem(TOKEN_KEY);
  storage.removeItem(AUTH_KEY);
}

const tokenUrl = (cfg) => `${cfg.issuer}/protocol/openid-connect/token`;

async function postForm(cfg, fetchImpl, form) {
  const res = await fetchImpl(tokenUrl(cfg), { method: 'POST', headers: { 'content-type': 'application/x-www-form-urlencoded' }, body: new URLSearchParams(form).toString() });
  return { res, body: await res.json().catch(() => ({})) };
}

/** The callback: returns the path to go back to. Throws AuthError with something a person can read if anything is off. */
export async function finishLogin(cfg, { search, storage, fetchImpl = globalThis.fetch, now = Date.now }) {
  const q = new URLSearchParams(search);
  const pendingText = storage.getItem(PKCE_KEY);
  storage.removeItem(PKCE_KEY); // single use: a replayed callback finds nothing
  if (q.get('error')) throw new AuthError(`sign-in was not completed: ${q.get('error_description') || q.get('error')}`);
  if (!q.get('code') || !q.get('state')) throw new AuthError('the sign-in reply is missing its code or state');
  let pending;
  try {
    pending = JSON.parse(pendingText ?? 'null');
  } catch {
    pending = null;
  }
  if (!pending) throw new AuthError('there is no sign-in in progress in this tab (start again)');
  if (pending.state !== q.get('state')) throw new AuthError('the sign-in reply does not belong to this tab (state mismatch)');
  const { res, body } = await postForm(cfg, fetchImpl, { grant_type: 'authorization_code', client_id: cfg.clientId, code: q.get('code'), redirect_uri: cfg.redirectUri, code_verifier: pending.verifier });
  if (!res.ok || !body.access_token) throw new AuthError(`the sign-in server refused the code: ${body.error_description || body.error || res.status}`);
  saveTokens(storage, body, now());
  return safeReturn(pending.returnTo);
}

export function decodeClaims(jwt) {
  try {
    const part = String(jwt).split('.')[1].replace(/-/g, '+').replace(/_/g, '/');
    return JSON.parse(decodeURIComponent(escape(atob(part + '='.repeat((4 - (part.length % 4)) % 4)))));
  } catch {
    return null;
  }
}

let renewing = null; // one renewal at a time: callers that arrive while it runs share its result

/**
 * The access token for a request, renewed first if it expires within 30 s. null when there is no session, or when the server rejected the refresh token (the
 * session is then cleared: sign in again). If the server cannot be reached the old token is returned: offline is not signed out.
 */
export async function getValidToken(cfg, { storage, fetchImpl = globalThis.fetch, now = Date.now, skewMs = RENEW_BEFORE_MS } = {}) {
  const token = storage.getItem(TOKEN_KEY);
  if (!token) return null;
  let meta = null;
  try {
    meta = JSON.parse(storage.getItem(AUTH_KEY) ?? 'null');
  } catch {
    meta = null;
  }
  const expiresAt = meta?.expires_at ?? (decodeClaims(token)?.exp ?? 0) * 1000;
  if (expiresAt - now() > skewMs) return token;
  if (!meta?.refresh_token) {
    clearTokens(storage);
    return null;
  }
  if (!renewing) {
    renewing = (async () => {
      try {
        const { res, body } = await postForm(cfg, fetchImpl, { grant_type: 'refresh_token', client_id: cfg.clientId, refresh_token: meta.refresh_token });
        if (res.ok && body.access_token) {
          saveTokens(storage, { ...body, refresh_token: body.refresh_token ?? meta.refresh_token, id_token: body.id_token ?? meta.id_token }, now());
          return body.access_token;
        }
        if (res.status === 400 || res.status === 401) {
          clearTokens(storage);
          return null;
        }
        return token;
      } catch {
        return token; // no network: keep the session
      } finally {
        renewing = null;
      }
    })();
  }
  return renewing;
}

export function logout(cfg, { storage, assign }) {
  let idToken = null;
  try {
    idToken = JSON.parse(storage.getItem(AUTH_KEY) ?? 'null')?.id_token ?? null;
  } catch {
    idToken = null;
  }
  clearTokens(storage);
  const q = new URLSearchParams({ client_id: cfg.clientId, post_logout_redirect_uri: `${cfg.origin}/`, ...(idToken ? { id_token_hint: idToken } : {}) });
  assign(`${cfg.issuer}/protocol/openid-connect/logout?${q}`);
}
