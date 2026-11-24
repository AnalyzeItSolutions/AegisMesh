// Where the browser talks to: ONE address, the gateway. It validates the user's token, tells the services who the caller is, and routes
// /api/sync/*, /api/events, /ws and the gRPC paths. Nothing in the browser talks to a service directly (a service would trust a header the browser sends).

export const DEFAULT_GATEWAY = 'http://localhost:8080';
export const TOKEN_KEY = 'aegismesh.token'; // the login flow leaves the access token here (sessionStorage: gone when the tab closes)

/** The gateway's base URL without a trailing slash; `configured` is NEXT_PUBLIC_GATEWAY_URL. */
export function gatewayUrl(configured) {
  const url = (configured || DEFAULT_GATEWAY).trim().replace(/\/+$/, '');
  if (!/^https?:\/\/[^/\s]+/.test(url)) throw new Error(`the gateway address must start with http:// or https://, got "${url}"`);
  return url;
}

/** Base of the JSON API: fetchTransport adds /sync/push, fetchTimeline adds /events. */
export const apiBase = (gateway) => `${gateway}/api`;

/** The WebSocket address. A browser cannot set headers on a WebSocket, so the token travels as a query parameter, which the gateway accepts. */
export function wsUrl(gateway, token) {
  const base = `${gateway.replace(/^http/, 'ws')}/ws`;
  return token ? `${base}?access_token=${encodeURIComponent(token)}` : base;
}

/** The saved access token, or null when there is none (or storage is blocked). */
export function readToken(storage) {
  try {
    const s = storage ?? globalThis.sessionStorage;
    return s?.getItem(TOKEN_KEY) || null;
  } catch {
    return null;
  }
}

export const authHeaders = (token) => (token ? { authorization: `Bearer ${token}` } : {});
