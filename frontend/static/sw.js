// Service Worker for hopssh proxy URL rewriting.
// Intercepts requests from proxy tabs and rewrites absolute paths
// (e.g., /ui/assets/app.js) to include the proxy prefix
// (e.g., /api/networks/.../proxy/4646/ui/assets/app.js).
//
// Uses event.clientId + self.clients.get() to detect which tabs are
// proxy sessions. Mappings are stored per-tab to support multiple
// simultaneous proxy sessions without cross-contamination.

// Per-tab proxy session tracking: clientId → proxyBase string.
var proxyClients = new Map();

// Pattern: /api/networks/{id}/nodes/{id}/proxy/{port}
var PROXY_PATTERN = /^(\/api\/networks\/[^/]+\/nodes\/[^/]+\/proxy\/\d+)/;

// Proxy request timeout (30 seconds).
var PROXY_TIMEOUT_MS = 30000;

// Cleanup interval (5 minutes).
var CLEANUP_INTERVAL_MS = 5 * 60 * 1000;

// --- Lifecycle ---

self.addEventListener('install', function () {
  self.skipWaiting();
});

self.addEventListener('activate', function (event) {
  event.waitUntil(self.clients.claim());
  scheduleCleanup();
});

// Accept proxy base mappings pushed from the injected bootstrap script.
self.addEventListener('message', function (event) {
  if (event.data && event.data.type === 'SET_PROXY_BASE') {
    var clientId = event.source && event.source.id;
    if (clientId && event.data.proxyBase) {
      proxyClients.set(clientId, event.data.proxyBase);
    }
  }
});

// --- Fetch interception ---

self.addEventListener('fetch', function (event) {
  var url = new URL(event.request.url);

  // Only rewrite same-origin requests.
  if (url.origin !== self.location.origin) return;

  // Never rewrite API routes — they already include the proxy prefix.
  if (url.pathname.startsWith('/api/')) return;

  // Never rewrite SvelteKit internals, SW, or bootstrap script.
  if (url.pathname.startsWith('/_app/') || url.pathname === '/sw.js' || url.pathname === '/sw-bootstrap.js') return;

  // Never rewrite known hopssh static assets.
  if (url.pathname === '/favicon.svg' || url.pathname === '/robots.txt' || url.pathname === '/logo.svg') return;

  // Only intercept if we have a potential clientId to resolve.
  var cid = event.clientId || event.resultingClientId;
  if (!cid) return;

  event.respondWith(handleFetch(event));
});

async function handleFetch(event) {
  var proxyBase;
  try {
    proxyBase = await resolveProxyBase(event);
  } catch (e) {
    // Resolution failed — pass through.
    return fetch(event.request);
  }

  if (!proxyBase) {
    // Not a proxy tab — pass through normally.
    return fetch(event.request);
  }

  // Rewrite: prepend proxy base to the absolute path.
  var url = new URL(event.request.url);
  var rewrittenPath = proxyBase + url.pathname;

  // Validate the rewritten URL stays within the proxy scope.
  if (!rewrittenPath.startsWith(proxyBase + '/') && rewrittenPath !== proxyBase) {
    return fetch(event.request);
  }

  var rewrittenUrl = new URL(rewrittenPath + url.search, self.location.origin);

  // Build a clean Request init.
  // - mode:'navigate' cannot be used in constructed Requests
  // - duplex:'half' required for streaming request bodies
  var init = {
    method: event.request.method,
    headers: event.request.headers,
    credentials: event.request.credentials,
    redirect: event.request.redirect,
  };
  if (event.request.mode !== 'navigate') {
    init.mode = event.request.mode;
  }
  if (event.request.method !== 'GET' && event.request.method !== 'HEAD') {
    init.body = event.request.body;
    init.duplex = 'half';
  }

  var newRequest = new Request(rewrittenUrl, init);

  // Fetch with timeout to avoid hanging on unreachable services.
  try {
    var controller = new AbortController();
    var timeoutId = setTimeout(function () { controller.abort(); }, PROXY_TIMEOUT_MS);

    var response = await fetch(newRequest, { signal: controller.signal });
    clearTimeout(timeoutId);
    return response;
  } catch (err) {
    if (err.name === 'AbortError') {
      return new Response('Proxy timeout: the service did not respond within 30 seconds.', {
        status: 504,
        statusText: 'Gateway Timeout',
        headers: { 'Content-Type': 'text/plain' },
      });
    }
    // Network error — return a descriptive error instead of letting the browser show a generic failure.
    return new Response('Proxy error: ' + (err.message || 'network request failed'), {
      status: 502,
      statusText: 'Bad Gateway',
      headers: { 'Content-Type': 'text/plain' },
    });
  }
}

// --- Proxy base resolution ---

async function resolveProxyBase(event) {
  // Strategy: check cached mapping first, then discover from client URL,
  // then try the referrer, then try the opener's mapping.

  var clientId = event.clientId || event.resultingClientId;
  if (!clientId) return null;

  // 1. Check persisted mapping (survives SPA navigations that change client URL).
  var proxyBase = proxyClients.get(clientId);
  if (proxyBase) {
    // Copy mapping to new client if navigating.
    if (event.resultingClientId && event.resultingClientId !== event.clientId && event.resultingClientId !== clientId) {
      proxyClients.set(event.resultingClientId, proxyBase);
    }
    return proxyBase;
  }

  // 2. Discover from client URL.
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
    // clients.get() can fail for opaque clients — fall through.
  }

  // 3. Try the referrer (useful when clientId discovery fails).
  var referrer = event.request.referrer;
  if (referrer) {
    proxyBase = extractProxyBase(referrer);
    if (proxyBase) {
      proxyClients.set(clientId, proxyBase);
      return proxyBase;
    }
  }

  // 4. Try the opener's mapping (for window.open() from a proxy tab).
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

// --- Cleanup ---
// Time-based cleanup removes stale entries for closed tabs.

var cleanupTimer = null;

function scheduleCleanup() {
  if (cleanupTimer) return;
  cleanupTimer = setInterval(function () {
    self.clients.matchAll().then(function (allClients) {
      var activeIds = new Set(allClients.map(function (c) { return c.id; }));
      proxyClients.forEach(function (_, id) {
        if (!activeIds.has(id)) proxyClients.delete(id);
      });
    }).catch(function () {
      // Ignore cleanup errors.
    });
  }, CLEANUP_INTERVAL_MS);
}
