// Service Worker bootstrap for proxied apps.
// Loaded via <script src="/sw-bootstrap.js?base=/api/networks/.../proxy/{port}">
// Ensures the SW is controlling before the proxied app's scripts execute,
// then patches WebSocket (SW can't intercept WebSocket connections).
(function () {
  var script = document.currentScript;
  if (!script) return;
  var params = new URL(script.src).searchParams;
  var base = params.get('base');
  if (!base) return;

  // --- URL Rewrite ---
  // The proxied app (e.g., Nomad) checks window.location.pathname to match
  // routes. Inside the iframe, pathname is /api/networks/.../proxy/4646/ui/
  // but the app expects /ui/. Strip the proxy prefix so the app's router works.
  // Safe because we're in an iframe — the URL bar is invisible.
  if (location.pathname.startsWith(base + '/')) {
    var appPath = location.pathname.slice(base.length);
    history.replaceState(null, '', appPath + location.search + location.hash);
  } else if (location.pathname === base) {
    history.replaceState(null, '', '/' + location.search + location.hash);
  }

  if (!('serviceWorker' in navigator)) return;

  // --- SW Bootstrap ---
  // If the SW is already controlling, just send the mapping and continue.
  // If not (first visit), we MUST prevent the app's scripts from executing
  // before the SW is ready — otherwise XHR/fetch calls (e.g., /v1/regions)
  // go directly to hopssh and get 404, crashing the app irrecoverably.
  //
  // Strategy: call window.stop() to halt page loading (prevents subsequent
  // <script> tags from executing), register the SW, wait for activation,
  // then reload. The reload is invisible inside the iframe.

  if (navigator.serviceWorker.controller) {
    // SW already controlling — send mapping, let app scripts run normally.
    navigator.serviceWorker.controller.postMessage({
      type: 'SET_PROXY_BASE',
      proxyBase: base,
    });
  } else {
    // No SW controlling — halt the page to prevent app scripts from firing.
    window.stop();

    navigator.serviceWorker.register('/sw.js', { scope: '/' }).then(function (r) {
      var sw = r.installing || r.waiting || r.active;
      if (!sw) return;
      function onActive() {
        navigator.serviceWorker.ready.then(function () {
          location.reload();
        });
      }
      if (sw.state === 'activated') {
        onActive();
        return;
      }
      sw.addEventListener('statechange', function () {
        if (sw.state === 'activated') onActive();
      });
    });

    // Don't set up WebSocket patch — page will reload anyway.
    return;
  }

  // --- URL Rewriting for fetch, XHR, and WebSocket ---
  // Rewrite all same-origin URLs through the proxy. Only skip URLs that
  // already include the proxy prefix (to avoid double-rewriting).
  // No framework-specific paths — this runs inside the proxied app's iframe,
  // so every same-origin request should go through the proxy.
  function rewriteUrl(url) {
    try {
      var parsed = new URL(url, location.href);
      if (parsed.origin === location.origin &&
          parsed.pathname !== base &&
          !parsed.pathname.startsWith(base + '/')) {
        parsed.pathname = base + parsed.pathname;
        return parsed.toString();
      }
    } catch (e) {}
    return url;
  }

  // --- Fetch ---
  var origFetch = window.fetch;
  window.fetch = function (input, init) {
    if (typeof input === 'string') {
      var rewritten = rewriteUrl(input);
      if (rewritten !== input) {
        input = rewritten;
        // Force credentials — the proxied app may use 'omit' but hopssh
        // needs the session cookie for authentication.
        init = Object.assign({}, init, { credentials: 'include' });
      }
    } else if (input instanceof Request) {
      var rewritten = rewriteUrl(input.url);
      if (rewritten !== input.url) {
        input = new Request(rewritten, input);
        init = Object.assign({}, init, { credentials: 'include' });
      }
    }
    return origFetch.call(this, input, init);
  };

  // --- XMLHttpRequest ---
  // Nomad's Ember adapter uses XHR (via _fetchRequest → ajax → XMLHttpRequest).
  // We rewrite the URL and force withCredentials so the session cookie is sent.
  // Per spec, same-origin XHR always sends cookies, but Ember's fetch polyfill
  // may explicitly strip them when credentials:'omit' is set.
  var origXHROpen = XMLHttpRequest.prototype.open;
  XMLHttpRequest.prototype.open = function (method, url) {
    var rewritten = rewriteUrl(url);
    if (rewritten !== url) {
      arguments[1] = rewritten;
      this.withCredentials = true;
    }
    return origXHROpen.apply(this, arguments);
  };

  // --- WebSocket ---
  // SW cannot intercept WebSocket connections, so we monkey-patch the
  // constructor to rewrite same-origin and localhost URLs.
  var OrigWS = window.WebSocket;
  window.WebSocket = function (url, protocols) {
    if (url && typeof url === 'string') {
      // Rewrite same-origin WebSocket URLs (rewriteUrl handles http/https).
      // Convert ws/wss to http/https for URL parsing, rewrite, convert back.
      var httpUrl = url.replace(/^ws(s?):/, 'http$1:');
      var rewritten = rewriteUrl(httpUrl);
      if (rewritten !== httpUrl) {
        url = rewritten.replace(/^http(s?):/, 'ws$1:');
      }
      // Also rewrite ws://localhost:{port}/... URLs (direct connections).
      var m = url.match(
        /^wss?:\/\/(localhost|127\.0\.0\.1)(:\d+)?(\/.*)?$/
      );
      if (m) {
        var path = m[3] || '/';
        url =
          (location.protocol === 'https:' ? 'wss:' : 'ws:') +
          '//' +
          location.host +
          base +
          path;
      }
    }
    return protocols !== undefined
      ? new OrigWS(url, protocols)
      : new OrigWS(url);
  };
  window.WebSocket.prototype = OrigWS.prototype;
  window.WebSocket.CONNECTING = OrigWS.CONNECTING;
  window.WebSocket.OPEN = OrigWS.OPEN;
  window.WebSocket.CLOSING = OrigWS.CLOSING;
  window.WebSocket.CLOSED = OrigWS.CLOSED;
})();
