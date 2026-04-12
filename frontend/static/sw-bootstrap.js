// Service Worker bootstrap for proxied apps.
// Loaded via <script src="/sw-bootstrap.js?base=/api/networks/.../proxy/{port}">
// Ensures the SW is active and has the proxy base mapping, then patches
// WebSocket for URL rewriting (SW can't intercept WebSocket connections).
(function () {
  var script = document.currentScript;
  if (!script) return;
  var params = new URL(script.src).searchParams;
  var base = params.get('base');
  if (!base) return;

  // --- SW Bootstrap ---
  if (!('serviceWorker' in navigator)) return;

  navigator.serviceWorker.register('/sw.js', { scope: '/' }).then(function (r) {
    if (navigator.serviceWorker.controller) {
      // SW already active — push the proxy base mapping.
      navigator.serviceWorker.controller.postMessage({
        type: 'SET_PROXY_BASE',
        proxyBase: base,
      });
      return;
    }
    // First visit: SW not yet controlling. Wait for activation, then reload.
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

  // --- WebSocket URL Rewriting ---
  // SW cannot intercept WebSocket connections, so we monkey-patch the
  // constructor to rewrite same-origin and localhost URLs.
  var OrigWS = window.WebSocket;
  window.WebSocket = function (url, protocols) {
    if (url && typeof url === 'string') {
      try {
        var parsed = new URL(url, location.href);
        if (
          parsed.origin === location.origin &&
          !parsed.pathname.startsWith('/api/')
        ) {
          parsed.pathname = base + parsed.pathname;
          url = parsed.toString();
        }
      } catch (e) {}
      // Rewrite ws://localhost:{port}/... URLs.
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
