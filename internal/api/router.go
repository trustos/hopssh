package api

import (
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	httpSwagger "github.com/swaggo/http-swagger/v2"
	"github.com/trustos/hopssh/internal/auth"
	"github.com/trustos/hopssh/internal/db"
	"github.com/trustos/hopssh/internal/frontend"
)

// AllowedOrigins controls CORS. Empty = same-origin only (no Access-Control-Allow-Origin header).
// Set via --allowed-origins flag (comma-separated).
var AllowedOrigins []string

// writeTimeout wraps a handler with a write deadline for non-streaming endpoints.
func writeTimeout(timeout time.Duration) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.TimeoutHandler(next, timeout, "request timeout")
	}
}

// cors handles CORS preflight and response headers.
func cors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin == "" {
			next.ServeHTTP(w, r)
			return
		}

		allowed := false
		for _, o := range AllowedOrigins {
			if o == "*" || o == origin {
				allowed = true
				break
			}
		}
		if !allowed {
			// Same-origin check: Origin must match Host.
			host := r.Host
			if origin == "http://"+host || origin == "https://"+host {
				allowed = true
			}
		}

		if !allowed {
			next.ServeHTTP(w, r)
			return
		}

		w.Header().Set("Access-Control-Allow-Origin", origin)
		w.Header().Set("Access-Control-Allow-Credentials", "true")
		w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, DELETE, OPTIONS")
		w.Header().Set("Vary", "Origin")

		if r.Method == http.MethodOptions {
			w.Header().Set("Access-Control-Max-Age", "86400")
			w.WriteHeader(http.StatusNoContent)
			return
		}

		next.ServeHTTP(w, r)
	})
}

// ParseOriginsFlag splits a comma-separated origin string.
func ParseOriginsFlag(s string) []string {
	if s == "" {
		return nil
	}
	var origins []string
	for _, o := range strings.Split(s, ",") {
		o = strings.TrimSpace(o)
		if o != "" {
			origins = append(origins, o)
		}
	}
	return origins
}

func NewRouter(
	users *db.UserStore,
	sessions *db.SessionStore,
	authH *AuthHandler,
	networkH *NetworkHandler,
	enrollH *EnrollHandler,
	proxyH *ProxyHandler,
	deviceH *DeviceHandler,
	bundleH *BundleHandler,
	renewH *RenewHandler,
) chi.Router {
	r := chi.NewRouter()
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)
	r.Use(cors)

	// Swagger UI.
	r.Get("/swagger/*", httpSwagger.WrapHandler)

	// Rate limiter for public auth endpoints: 10 requests/minute burst 20.
	publicRL := auth.NewRateLimiter(10, 20, time.Minute, TrustedProxy)
	wt := writeTimeout(30 * time.Second)

	// Public endpoints (rate limited + write timeout).
	r.With(publicRL.Limit, wt).Get("/api/auth/status", authH.Status)
	r.With(publicRL.Limit, wt).Post("/api/auth/register", authH.Register)
	r.With(publicRL.Limit, wt).Post("/api/auth/login", authH.Login)
	r.With(publicRL.Limit, wt).Post("/api/enroll", enrollH.Enroll)

	// Device flow (public — agent-initiated).
	r.With(publicRL.Limit, wt).Post("/api/device/code", deviceH.RequestCode)
	r.With(publicRL.Limit, wt).Post("/api/device/poll", deviceH.Poll)

	// Cert renewal (public — agent authenticates via bearer token).
	r.With(publicRL.Limit, wt).Post("/api/renew", renewH.Renew)

	// Bundle download (public — token is the auth).
	r.With(wt).Get("/api/bundles/{token}", bundleH.DownloadBundle)

	// Authenticated endpoints.
	r.Group(func(r chi.Router) {
		r.Use(auth.RequireAuth(sessions, users))

		r.With(wt).Post("/api/auth/logout", authH.Logout)
		r.With(wt).Get("/api/auth/me", authH.Me)

		// Networks.
		r.With(wt).Post("/api/networks", networkH.CreateNetwork)
		r.With(wt).Get("/api/networks", networkH.ListNetworks)
		r.With(wt).Get("/api/networks/{networkID}", networkH.GetNetwork)
		r.With(wt).Delete("/api/networks/{networkID}", networkH.DeleteNetwork)

		// Nodes.
		r.With(wt).Post("/api/networks/{networkID}/nodes", enrollH.CreateNode)
		r.With(wt).Get("/api/networks/{networkID}/nodes", proxyH.ListNodes)

		// Node management.
		r.With(wt).Delete("/api/networks/{networkID}/nodes/{nodeID}", proxyH.DeleteNode)

		// Node proxy (health has timeout; shell + exec are streaming — no timeout).
		r.With(wt).Get("/api/networks/{networkID}/nodes/{nodeID}/health", proxyH.NodeHealth)
		r.Get("/api/networks/{networkID}/nodes/{nodeID}/shell", proxyH.NodeShell)
		r.Post("/api/networks/{networkID}/nodes/{nodeID}/exec", proxyH.NodeExec)

		// Port forwards.
		r.With(wt).Post("/api/networks/{networkID}/nodes/{nodeID}/port-forwards", proxyH.StartPortForward)
		r.With(wt).Delete("/api/networks/{networkID}/port-forwards/{fwdID}", proxyH.StopPortForward)
		r.With(wt).Get("/api/networks/{networkID}/port-forwards", proxyH.ListPortForwards)

		// Device flow (browser-side authorization).
		r.With(wt).Post("/api/device/authorize", deviceH.Authorize)
		r.With(wt).Get("/api/device/verify/{code}", deviceH.VerifyCode)

		// Enrollment bundles.
		r.With(wt).Post("/api/networks/{networkID}/bundles", bundleH.CreateBundle)
	})

	// Serve frontend SPA (catch-all — must be last).
	r.NotFound(frontend.Handler().ServeHTTP)

	return r
}
