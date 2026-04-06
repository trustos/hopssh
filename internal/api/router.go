package api

import (
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	httpSwagger "github.com/swaggo/http-swagger/v2"
	"github.com/trustos/hopssh/internal/auth"
	"github.com/trustos/hopssh/internal/db"
)

func NewRouter(
	users *db.UserStore,
	sessions *db.SessionStore,
	authH *AuthHandler,
	networkH *NetworkHandler,
	enrollH *EnrollHandler,
	proxyH *ProxyHandler,
) chi.Router {
	r := chi.NewRouter()
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)

	// Swagger UI.
	r.Get("/swagger/*", httpSwagger.WrapHandler)

	// Public endpoints.
	r.Get("/api/auth/status", authH.Status)
	r.Post("/api/auth/register", authH.Register)
	r.Post("/api/auth/login", authH.Login)
	r.Post("/api/enroll", enrollH.Enroll)

	// Authenticated endpoints.
	r.Group(func(r chi.Router) {
		r.Use(auth.RequireAuth(sessions, users))

		r.Post("/api/auth/logout", authH.Logout)
		r.Get("/api/auth/me", authH.Me)

		// Networks.
		r.Post("/api/networks", networkH.CreateNetwork)
		r.Get("/api/networks", networkH.ListNetworks)
		r.Get("/api/networks/{networkID}", networkH.GetNetwork)
		r.Delete("/api/networks/{networkID}", networkH.DeleteNetwork)

		// Nodes.
		r.Post("/api/networks/{networkID}/nodes", enrollH.CreateNode)
		r.Get("/api/networks/{networkID}/nodes", proxyH.ListNodes)

		// Node proxy (health, shell, exec).
		r.Get("/api/networks/{networkID}/nodes/{nodeID}/health", proxyH.NodeHealth)
		r.Get("/api/networks/{networkID}/nodes/{nodeID}/shell", proxyH.NodeShell)
		r.Post("/api/networks/{networkID}/nodes/{nodeID}/exec", proxyH.NodeExec)

		// Port forwards.
		r.Post("/api/networks/{networkID}/nodes/{nodeID}/port-forwards", proxyH.StartPortForward)
		r.Delete("/api/networks/{networkID}/port-forwards/{fwdID}", proxyH.StopPortForward)
		r.Get("/api/networks/{networkID}/port-forwards", proxyH.ListPortForwards)
	})

	return r
}
