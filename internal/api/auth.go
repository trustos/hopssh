package api

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/trustos/hopssh/internal/auth"
	"github.com/trustos/hopssh/internal/db"
	"golang.org/x/crypto/bcrypt"
)

const sessionTTL = 30 * 24 * time.Hour // 30 days

// AuthHandler manages registration, login, and session lifecycle.
type AuthHandler struct {
	Users    *db.UserStore
	Sessions *db.SessionStore
}

// Register creates a new user account.
// @Summary      Register a new user
// @Description  Create a user account with email and password. Returns session token.
// @Tags         auth
// @Accept       json
// @Produce      json
// @Param        body body RegisterRequest true "Registration details"
// @Success      200 {object} AuthResponse
// @Failure      400 {object} ErrorResponse "Missing email or password"
// @Failure      409 {object} ErrorResponse "Email already registered"
// @Router       /api/auth/register [post]
func (h *AuthHandler) Register(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Email    string `json:"email"`
		Name     string `json:"name"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	if body.Email == "" || body.Password == "" {
		http.Error(w, "email and password are required", http.StatusBadRequest)
		return
	}

	existing, _ := h.Users.GetByEmail(body.Email)
	if existing != nil {
		http.Error(w, "email already registered", http.StatusConflict)
		return
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(body.Password), bcrypt.DefaultCost)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	user := &db.User{
		ID:           uuid.New().String(),
		Email:        body.Email,
		Name:         body.Name,
		PasswordHash: string(hash),
	}
	if err := h.Users.Create(user); err != nil {
		http.Error(w, "failed to create user", http.StatusInternalServerError)
		return
	}

	token := generateSessionToken()
	h.Sessions.Create(token, user.ID, sessionTTL)

	http.SetCookie(w, &http.Cookie{
		Name:     "session",
		Value:    token,
		Path:     "/",
		MaxAge:   int(sessionTTL.Seconds()),
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"id":    user.ID,
		"email": user.Email,
		"name":  user.Name,
		"token": token,
	})
}

// Login authenticates a user and returns a session token.
// @Summary      Login
// @Description  Authenticate with email and password. Returns session token and sets cookie.
// @Tags         auth
// @Accept       json
// @Produce      json
// @Param        body body LoginRequest true "Login credentials"
// @Success      200 {object} AuthResponse
// @Failure      401 {object} ErrorResponse "Invalid credentials"
// @Router       /api/auth/login [post]
func (h *AuthHandler) Login(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}

	user, _ := h.Users.GetByEmail(body.Email)
	if user == nil {
		http.Error(w, "invalid credentials", http.StatusUnauthorized)
		return
	}

	if err := bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(body.Password)); err != nil {
		http.Error(w, "invalid credentials", http.StatusUnauthorized)
		return
	}

	token := generateSessionToken()
	h.Sessions.Create(token, user.ID, sessionTTL)

	http.SetCookie(w, &http.Cookie{
		Name:     "session",
		Value:    token,
		Path:     "/",
		MaxAge:   int(sessionTTL.Seconds()),
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"id":    user.ID,
		"email": user.Email,
		"name":  user.Name,
		"token": token,
	})
}

// Logout destroys the current session.
// @Summary      Logout
// @Description  Destroy the current session and clear the session cookie.
// @Tags         auth
// @Security     BearerAuth
// @Success      200
// @Router       /api/auth/logout [post]
func (h *AuthHandler) Logout(w http.ResponseWriter, r *http.Request) {
	user := auth.UserFromContext(r.Context())
	if user == nil {
		w.WriteHeader(http.StatusOK)
		return
	}
	// Delete current session token
	if c, err := r.Cookie("session"); err == nil {
		h.Sessions.Delete(c.Value)
	}
	http.SetCookie(w, &http.Cookie{
		Name:   "session",
		Value:  "",
		Path:   "/",
		MaxAge: -1,
	})
	w.WriteHeader(http.StatusOK)
}

// Me returns the current authenticated user.
// @Summary      Current user
// @Description  Returns the authenticated user's profile.
// @Tags         auth
// @Security     BearerAuth
// @Produce      json
// @Success      200 {object} UserResponse
// @Failure      401 {object} ErrorResponse
// @Router       /api/auth/me [get]
func (h *AuthHandler) Me(w http.ResponseWriter, r *http.Request) {
	user := auth.UserFromContext(r.Context())
	if user == nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"id":    user.ID,
		"email": user.Email,
		"name":  user.Name,
	})
}

// Status checks whether any users exist (for showing register vs login page).
// @Summary      Auth status
// @Description  Returns whether any users are registered. Used by frontend to show register or login form.
// @Tags         auth
// @Produce      json
// @Success      200 {object} StatusResponse
// @Router       /api/auth/status [get]
func (h *AuthHandler) Status(w http.ResponseWriter, r *http.Request) {
	count, _ := h.Users.Count()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"hasUsers": count > 0,
	})
}

func generateSessionToken() string {
	b := make([]byte, 32)
	rand.Read(b)
	return hex.EncodeToString(b)
}
