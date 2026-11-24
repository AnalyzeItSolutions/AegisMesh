'use client';

import { useEffect, useRef, useState } from 'react';
import { finishLogin } from '../../../lib/auth.js';
import { browserAuth } from '../../../lib/session.js';

export default function CallbackPage() {
  const [problem, setProblem] = useState(null);
  const started = useRef(false); // React may run the effect twice in development; the code can be used only once
  useEffect(() => {
    if (started.current) return;
    started.current = true;
    finishLogin(browserAuth(), { search: window.location.search, storage: window.sessionStorage })
      .then((to) => window.location.replace(to))
      .catch((e) => setProblem(e.message));
  }, []);
  return (
    <main>
      <h1>Signing in</h1>
      {problem ? (
        <>
          <p role="alert">{problem}</p>
          <p><a href="/login">Try again</a></p>
        </>
      ) : (
        <p className="muted">One moment…</p>
      )}
    </main>
  );
}
