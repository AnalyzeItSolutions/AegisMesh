// Browser-side glue for lib/auth.js: the configured sign-in server and the token getter the pages hand to the clients.
import { authConfig, getValidToken } from './auth.js';
import { authHeaders } from './gateway.js';

export function browserAuth() {
  return authConfig({ issuer: process.env.NEXT_PUBLIC_AUTH_ISSUER, clientId: process.env.NEXT_PUBLIC_AUTH_CLIENT || 'web', origin: window.location.origin });
}

/** A fresh access token (renewed if close to expiry) or null. */
export const currentToken = () => getValidToken(browserAuth(), { storage: window.sessionStorage });

/** Pass to fetchTransport / fetchTimeline: asked on every request. */
export const liveHeaders = async () => authHeaders(await currentToken());
