'use client';

import { useEffect, useState } from 'react';
import { startLogin } from '../../lib/auth.js';
import { browserAuth } from '../../lib/session.js';

export default function LoginPage() {
  const [problem, setProblem] = useState(null);
  useEffect(() => {
    const returnTo = new URLSearchParams(window.location.search).get('return') || '/field';
    startLogin(browserAuth(), { storage: window.sessionStorage, assign: (u) => window.location.assign(u), returnTo }).catch((e) => setProblem(e.message));
  }, []);
  return (
    <main>
      <h1>Sign in</h1>
      {problem ? <p role="alert">{problem}</p> : <p className="muted">Taking you to the sign-in page…</p>}
    </main>
  );
}
