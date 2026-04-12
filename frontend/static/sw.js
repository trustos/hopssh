// Service Worker for hopssh proxy URL rewriting.
// Intercepts requests from proxy tabs and rewrites absolute paths
// (e.g., /ui/assets/app.js) to include the proxy prefix
// (e.g., /api/networks/.../proxy/4646/ui/assets/app.js).

// Per-tab proxy session tracking: clientId → proxyBase string
const proxyClients = new Map();

// Pattern: /api/networks/{id}/nodes/{id}/proxy/{port}
const PROXY_PATTERN = /^(\/api\/networks\/[^/]+\/nodes\/[^/]+\/proxy\/\d+)/;

self.addEventListener('install', () => {
  self.skipWaiting();
});

self.addEventListener('activate', (event) => {
  event.waitUntil(self.clients.claim());
});

// Accept proxy base mappings from the injected bootstrap script.
self.addEventListener('message', (event) => {
  if (event.data && event.data.type === 'SET_PROXY_BASE') {
    const clientId = event.source && event.source.id;
    if (clientId && event.data.proxyBase) {
      proxyClients.set(clientId, event.data.proxyBase);
    }
  }
});

self.addEventListener('fetch', (event) => {
  const url = new URL(event.request.url);

  // Only rewrite same-origin requests.
  if (url.origin !== self.location.origin) return;

  // Never rewrite API routes — they already include the proxy prefix.
  if (url.pathname.startsWith('/api/')) return;

  // Never rewrite SvelteKit internals or the SW itself.
  if (url.pathname.startsWith('/_app/') || url.pathname === '/sw.js') return;

  // Never rewrite known hopssh static assets.
  if (url.pathname === '/favicon.svg' || url.pathname === '/robots.txt') return;

  maybeCleanup();
  event.respondWith(handleFetch(event));
});

async function handleFetch(event) {
  const proxyBase = await resolveProxyBase(event);

  if (!proxyBase) {
    // Not a proxy tab — pass through normally.
    return fetch(event.request);
  }

  // Rewrite: prepend proxy base to the absolute path.
  const url = new URL(event.request.url);
  const rewrittenUrl = new URL(
    proxyBase + url.pathname + url.search,
    self.location.origin
  );

  const newRequest = new Request(rewrittenUrl, {
    method: event.request.method,
    headers: event.request.headers,
    body: event.request.body,
    mode: event.request.mode,
    credentials: event.request.credentials,
    redirect: event.request.redirect,
    referrer: event.request.referrer,
    signal: event.request.signal,
  });

  return fetch(newRequest);
}

async function resolveProxyBase(event) {
  // Try resultingClientId first (navigation creating new client).
  var clientId = event.resultingClientId || event.clientId;
  if (!clientId) return null;

  // Check persisted mapping (survives SPA navigations that change client URL).
  var proxyBase = proxyClients.get(clientId);
  if (proxyBase) {
    // Copy mapping to new client if navigating.
    if (event.resultingClientId && event.resultingClientId !== event.clientId) {
      proxyClients.set(event.resultingClientId, proxyBase);
    }
    return proxyBase;
  }

  // Discover from client URL.
  try {
    var client = await self.clients.get(clientId);
    if (client) {
      proxyBase = extractProxyBase(client.url);
      if (proxyBase) {
        proxyClients.set(clientId, proxyBase);
        return proxyBase;
      }
    }
  } catch (e) {
    // clients.get() can fail for opaque clients.
  }

  // Try the opener's mapping (for window.open() from a proxy tab).
  if (event.clientId && event.clientId !== clientId) {
    proxyBase = proxyClients.get(event.clientId);
    if (proxyBase) {
      proxyClients.set(clientId, proxyBase);
      return proxyBase;
    }
  }

  return null;
}

function extractProxyBase(urlString) {
  try {
    var url = new URL(urlString);
    var match = url.pathname.match(PROXY_PATTERN);
    return match ? match[1] : null;
  } catch (e) {
    return null;
  }
}

// Periodic cleanup of stale client entries.
var fetchCount = 0;
function maybeCleanup() {
  if (++fetchCount % 200 !== 0) return;
  self.clients.matchAll().then(function (allClients) {
    var activeIds = new Set(allClients.map(function (c) { return c.id; }));
    proxyClients.forEach(function (_, id) {
      if (!activeIds.has(id)) proxyClients.delete(id);
    });
  });
}
