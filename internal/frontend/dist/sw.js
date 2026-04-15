// Service Worker for hopssh proxy URL rewriting.
// Intercepts requests from proxy iframes and rewrites absolute paths
// (e.g., /ui/assets/app.js) to include the proxy prefix
// (e.g., /api/networks/.../proxy/4646/ui/assets/app.js).
//
// Mappings are persisted to the Cache API so they survive SW termination.
// The browser can kill and restart the SW at any time; without persistence,
// mappings are lost when the proxied app has changed the iframe URL via
// pushState (e.g., /ui/jobs) so clients.get() no longer returns a URL
// matching the proxy pattern.

// Per-tab proxy session tracking: clientId → proxyBase string.
var proxyClients = new Map();

// Eagerly load persisted mappings on every SW startup, not just activation.
// activate only fires once per SW version; the browser can terminate and
// restart the SW without re-firing activate, leaving the in-memory map empty.
var _mappingsReady = loadMappings();

// Pattern: /api/networks/{id}/nodes/{id}/proxy/{port}
var PROXY_PATTERN = /^(\/api\/networks\/[^/]+\/nodes\/[^/]+\/proxy\/\d+)/;

// Proxy request timeout (30 seconds).
var PROXY_TIMEOUT_MS = 30000;

// Cache key for persisted mappings.
var CACHE_NAME = 'hopssh-proxy-mappings';
var CACHE_KEY = '/_proxy-client-mappings';

// --- Persistence via Cache API ---

function persistMappings() {
  var obj = {};
  proxyClients.forEach(function (v, k) { obj[k] = v; });
  caches.open(CACHE_NAME).then(function (cache) {
    cache.put(CACHE_KEY, new Response(JSON.stringify(obj)));
  }).catch(function () {});
}

function loadMappings() {
  return caches.open(CACHE_NAME).then(function (cache) {
    return cache.match(CACHE_KEY);
  }).then(function (resp) {
    if (!resp) return;
    return resp.text().then(function (text) {
      try {
        var obj = JSON.parse(text);
        for (var k in obj) {
          if (!proxyClients.has(k)) proxyClients.set(k, obj[k]);
        }
      } catch (e) {}
    });
  }).catch(function () {});
}

function storeMapping(clientId, proxyBase) {
  proxyClients.set(clientId, proxyBase);
  persistMappings();
}

// --- Lifecycle ---

self.addEventListener('install', function () {
  self.skipWaiting();
});

self.addEventListener('activate', function (event) {
  event.waitUntil(
    Promise.all([
      self.clients.claim(),
      loadMappings()
    ])
  );
});

// Accept proxy base mappings pushed from the injected bootstrap script.
self.addEventListener('message', function (event) {
  if (event.data && event.data.type === 'SET_PROXY_BASE') {
    var clientId = event.source && event.source.id;
    if (clientId && event.data.proxyBase) {
      storeMapping(clientId, event.data.proxyBase);
    }
  }
});

// --- Fetch interception ---

self.addEventListener('fetch', function (event) {
  var url = new URL(event.request.url);

  // Only rewrite same-origin requests.
  if (url.origin !== self.location.origin) return;

  // Skip paths already containing the proxy prefix (avoid double-rewriting).
  if (PROXY_PATTERN.test(url.pathname)) return;

  // Skip hopssh-internal paths. The SW serves BOTH dashboard tabs and proxy
  // iframe tabs. These paths belong to hopssh itself and must never be
  // rewritten, even if a stale proxy mapping exists for this clientId.
  // (The bootstrap rewriteUrl doesn't need these — it only runs in the iframe.)
  if (url.pathname.startsWith('/_app/') ||
      url.pathname === '/sw.js' ||
      url.pathname === '/sw-bootstrap.js' ||
      url.pathname === '/favicon.svg' ||
      url.pathname === '/robots.txt' ||
      url.pathname === '/logo.svg' ||
      url.pathname.startsWith('/proxy/')) return;

  // Only intercept if this client has a proxy mapping. The check is
  // synchronous — if the map has no entry, the request passes through
  // natively without respondWith. This avoids a Chrome bug where
  // respondWith(fetch(event.request)) fails to commit navigations,
  // leaving the tab stuck on a loading spinner.
  var cid = event.clientId || event.resultingClientId;
  if (!cid || !proxyClients.has(cid)) return;

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

  // 1. Check in-memory mapping.
  var proxyBase = proxyClients.get(clientId);
  if (proxyBase) {
    // Validate: verify the client is still a proxy tab (not a reused clientId).
    // Browser can reuse clientIds — if a proxy tab closes and a dashboard tab
    // gets the same ID, the stale mapping would rewrite dashboard requests
    // through the proxy, breaking the entire site.
    try {
      var client = await self.clients.get(clientId);
      if (client) {
        // Client URL has proxy pattern → mapping is valid (even if pushState changed it,
        // the mapping was set when the URL did match).
        // Client URL does NOT have proxy pattern AND is not a pushState'd path
        // from the proxied app → stale mapping, remove it.
        var clientBase = extractProxyBase(client.url);
        if (!clientBase) {
          // Check if this looks like a hopssh dashboard page (not a proxied app page).
          // Proxied app pages have paths like /ui/jobs, /ui/storage (Nomad).
          // Dashboard pages have paths like /, /login, /networks/..., /proxy/...
          var path = new URL(client.url).pathname;
          if (path === '/' || path.startsWith('/login') || path.startsWith('/register') ||
              path.startsWith('/networks') || path.startsWith('/proxy/') ||
              path.startsWith('/terminal') || path.startsWith('/invite')) {
            // This is a hopssh page, not a proxied app — stale mapping.
            proxyClients.delete(clientId);
            persistMappings();
            return null;
          }
        }
      }
    } catch (e) {}
    return proxyBase;
  }

  // 2. Discover from client URL.
  try {
    var client = await self.clients.get(clientId);
    if (client) {
      proxyBase = extractProxyBase(client.url);
      if (proxyBase) {
        storeMapping(clientId, proxyBase);
        return proxyBase;
      }
    }
  } catch (e) {}

  // 3. Try the referrer.
  var referrer = event.request.referrer;
  if (referrer) {
    proxyBase = extractProxyBase(referrer);
    if (proxyBase) {
      storeMapping(clientId, proxyBase);
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
