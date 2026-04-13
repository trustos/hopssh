// Service Worker for hopssh proxy URL rewriting.
// Intercepts requests from proxy iframes and rewrites absolute paths
// (e.g., /ui/assets/app.js) to include the proxy prefix
// (e.g., /api/networks/.../proxy/4646/ui/assets/app.js).
//
// The proxy page loads inside an iframe, so the client URL always
// contains the proxy prefix — no complex mapping persistence needed.

// Per-tab proxy session tracking: clientId → proxyBase string.
var proxyClients = new Map();

// Pattern: /api/networks/{id}/nodes/{id}/proxy/{port}
var PROXY_PATTERN = /^(\/api\/networks\/[^/]+\/nodes\/[^/]+\/proxy\/\d+)/;

// Proxy request timeout (30 seconds).
var PROXY_TIMEOUT_MS = 30000;

// --- Lifecycle ---

self.addEventListener('install', function () {
  self.skipWaiting();
});

self.addEventListener('activate', function (event) {
  event.waitUntil(self.clients.claim());
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

  // Never rewrite the proxy wrapper page itself.
  if (url.pathname.startsWith('/proxy/')) return;

  // Only intercept if we have a potential clientId to resolve.
  var cid = event.clientId || event.resultingClientId;
  if (!cid) return;

  event.respondWith(handleFetch(event));
});

async function handleFetch(event) {
  var proxyBase = await resolveProxyBase(event);

  if (!proxyBase) {
    // Not a proxy tab — pass through normally.
    return fetch(event.request);
  }

  // Rewrite: prepend proxy base to the absolute path.
  var url = new URL(event.request.url);
  var rewrittenUrl = new URL(proxyBase + url.pathname + url.search, self.location.origin);

  // Build a clean Request init.
  // Always use credentials:'include' — the proxied app may not send cookies
  // (e.g., Nomad uses 'omit'), but the hopssh proxy endpoint requires the
  // session cookie for authentication.
  var init = {
    method: event.request.method,
    headers: event.request.headers,
    credentials: 'include',
    redirect: event.request.redirect,
  };
  // Force 'same-origin' mode for all non-navigate rewritten requests.
  // Original <script>/<link> tags use 'no-cors' which can cause the browser
  // to strip cookies even with credentials:'include'. Since rewritten requests
  // are always same-origin, 'same-origin' mode ensures cookies are sent.
  if (event.request.mode !== 'navigate') {
    init.mode = 'same-origin';
  }
  if (event.request.method !== 'GET' && event.request.method !== 'HEAD') {
    init.body = event.request.body;
    init.duplex = 'half';
  }

  var newRequest = new Request(rewrittenUrl, init);

  // Fetch with timeout.
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
    return new Response('Proxy error: ' + (err.message || 'network request failed'), {
      status: 502,
      statusText: 'Bad Gateway',
      headers: { 'Content-Type': 'text/plain' },
    });
  }
}

// --- Proxy base resolution ---

async function resolveProxyBase(event) {
  var clientId = event.clientId || event.resultingClientId;
  if (!clientId) return null;

  // 1. Check in-memory mapping (fastest).
  var proxyBase = proxyClients.get(clientId);
  if (proxyBase) return proxyBase;

  // 2. Discover from client URL (iframe always has proxy URL).
  try {
    var client = await self.clients.get(clientId);
    if (client) {
      proxyBase = extractProxyBase(client.url);
      if (proxyBase) {
        proxyClients.set(clientId, proxyBase);
        return proxyBase;
      }
    }
  } catch (e) {}

  // 3. Try the referrer.
  var referrer = event.request.referrer;
  if (referrer) {
    proxyBase = extractProxyBase(referrer);
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
