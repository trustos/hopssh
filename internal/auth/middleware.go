package auth

import (
	"context"
	"net/http"
	"strings"

	"github.com/trustos/hopssh/internal/db"
)

type contextKey string

const userKey contextKey = "user"

// RequireAuth is middleware that validates session tokens from the Authorization
// header or session cookie. Sets the user in the request context.
func RequireAuth(sessions *db.SessionStore, users *db.UserStore) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			token := extractToken(r)
			if token == "" {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}

			userID, err := sessions.GetUserID(token)
			if err != nil || userID == "" {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}

			profile, err := users.GetProfileByID(userID)
			if err != nil || profile == nil {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}

			ctx := context.WithValue(r.Context(), userKey, profile)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// UserFromContext returns the authenticated user profile from the request context.
// The profile never contains the password hash.
func UserFromContext(ctx context.Context) *db.UserProfile {
	u, _ := ctx.Value(userKey).(*db.UserProfile)
	return u
}

// WithUser returns a new context with the given user profile set.
// Used in tests to simulate authenticated requests without a real session.
func WithUser(ctx context.Context, user *db.UserProfile) context.Context {
	return context.WithValue(ctx, userKey, user)
}

func extractToken(r *http.Request) string {
	if auth := r.Header.Get("Authorization"); strings.HasPrefix(auth, "Bearer ") {
		return strings.TrimPrefix(auth, "Bearer ")
	}
	if c, err := r.Cookie("session"); err == nil {
		return c.Value
	}
	return ""
}
