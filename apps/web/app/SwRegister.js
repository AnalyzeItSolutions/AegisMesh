'use client';

import { useEffect } from 'react';

/** Registers the service worker so the app shell opens with no connection. A failure is logged and the app works without it. */
export default function SwRegister() {
  useEffect(() => {
    if (!('serviceWorker' in navigator)) return;
    navigator.serviceWorker.register('/sw.js').catch((error) => console.warn('service worker not registered:', error));
  }, []);
  return null;
}
