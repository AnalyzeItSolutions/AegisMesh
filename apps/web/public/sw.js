// Service worker for the field app: the app shell is cached so a responder can open the app with no connection.
// Pages are network-first (fresh when online, the cached copy when not); static files are cache-first; anything that is not a same-origin GET,
// and every API or sync call, goes straight to the network untouched (a cached answer to a write would be a lie).
const VERSION = 'v1';
const CACHE_PREFIX = 'aegismesh-shell-';
const CACHE = `${CACHE_PREFIX}${VERSION}`;
const SHELL_URLS = ['/', '/field', '/command', '/manifest.webmanifest'];
const PASS_THROUGH = ['/api/', '/sync/', '/ws'];

self.addEventListener('install', (event) => {
  event.waitUntil(caches.open(CACHE).then((cache) => cache.addAll(SHELL_URLS)).then(() => self.skipWaiting()));
});

self.addEventListener('activate', (event) => {
  event.waitUntil(
    caches.keys()
      .then((names) => Promise.all(names.filter((n) => n.startsWith(CACHE_PREFIX) && n !== CACHE).map((n) => caches.delete(n))))
      .then(() => self.clients.claim()),
  );
});

self.addEventListener('fetch', (event) => {
  const req = event.request;
  if (req.method !== 'GET') return;
  const url = new URL(req.url);
  if (url.origin !== self.location.origin || PASS_THROUGH.some((p) => url.pathname.startsWith(p))) return;

  if (req.mode === 'navigate') {
    event.respondWith(
      fetch(req)
        .then((res) => {
          const copy = res.clone();
          caches.open(CACHE).then((c) => c.put(req, copy));
          return res;
        })
        .catch(async () => (await caches.match(req)) || (await caches.match('/field')) || (await caches.match('/')) || Response.error()),
    );
    return;
  }
  event.respondWith(
    caches.match(req).then((hit) => hit || fetch(req).then((res) => {
      if (res.ok) {
        const copy = res.clone();
        caches.open(CACHE).then((c) => c.put(req, copy));
      }
      return res;
    })),
  );
});
