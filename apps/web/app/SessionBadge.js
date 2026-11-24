'use client';

import { useEffect, useState } from 'react';
import { decodeClaims, logout } from '../lib/auth.js';
import { readToken } from '../lib/gateway.js';
import { browserAuth } from '../lib/session.js';

/** Top-right: who is signed in, with Sign out; or a Sign in link. Display only: the gateway is what enforces access. */
export default function SessionBadge() {
  const [who, setWho] = useState(undefined);
  useEffect(() => {
    const read = () => {
      const t = readToken();
      const c = t ? decodeClaims(t) : null;
      setWho(c ? c.preferred_username || c.email || c.sub : null);
    };
    read();
    const timer = setInterval(read, 5000);
    return () => clearInterval(timer);
  }, []);
  if (who === undefined) return null;
  const here = typeof window === 'undefined' ? '/field' : window.location.pathname;
  return (
    <div className="session-badge" style={{ position: 'fixed', bottom: 10, left: 10, fontSize: 13, zIndex: 10, display: 'flex', gap: 8, alignItems: 'center', background: 'rgba(15,23,42,0.85)', padding: '4px 10px', borderRadius: 8 }}>
      {who ? (
        <>
          <span data-testid="who">Signed in as {who}</span>{' '}
          <button type="button" style={{ width: 'auto', padding: '2px 10px' }} onClick={() => logout(browserAuth(), { storage: window.sessionStorage, assign: (u) => window.location.assign(u) })}>Sign out</button>
        </>
      ) : (
        <a href={`/login?return=${encodeURIComponent(here)}`}>Sign in</a>
      )}
    </div>
  );
}
