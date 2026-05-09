package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/trustos/hopssh/internal/db"
)

// OAuthProvider is the configured set of providers a server can offer.
// All fields are read-only after server start; mutating them while the
// process is running is unsupported.
type OAuthProvider struct {
	GitHubClientID     string
	GitHubClientSecret string
	// PublicEndpoint is the server's user-facing URL — used to build
	// the OAuth callback URI (`<endpoint>/api/auth/github/callback`).
	// Must match the Authorization callback URL configured on the
	// GitHub OAuth app.
	PublicEndpoint string
}

// Configured returns true when GitHub OAuth has both client ID and
// secret set. The frontend uses /api/auth/oauth/status to decide
// whether to enable the "Continue with GitHub" button.
func (p *OAuthProvider) Configured() bool {
	return p != nil && p.GitHubClientID != "" && p.GitHubClientSecret != ""
}

// OAuthHandler wraps the OAuth flow handlers. Constructed with the
// same UserStore + SessionStore as AuthHandler so a successful OAuth
// login produces a session cookie identical to the password path.
type OAuthHandler struct {
	Provider *OAuthProvider
	Users    *db.UserStore
	Sessions *db.SessionStore
	Audit    *db.AuditStore
}

// oauthStateCookie is the name of the temporary cookie that holds the
// CSRF state token + intended post-login redirect path between the
// /start and /callback hops.
const oauthStateCookie = "oauth_state"
const oauthStateTTL = 10 * time.Minute

// Status returns whether OAuth providers are configured. Public
// endpoint; called by the frontend on every login-page load to decide
// which buttons to enable.
func (h *OAuthHandler) Status(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]any{
		"github": h.Provider.Configured(),
	})
}

// StartGitHub kicks off the OAuth dance. Generates a cryptographically
// random state token, stores it (along with the post-login redirect
// path) in a short-lived HTTP-only cookie, then 302-redirects the
// browser to GitHub's authorize endpoint.
//
// CSRF protection: the state value is opaque + must echo back exactly
// in the callback. Without the cookie, an attacker forging a callback
// can't make us mint a session for a user.
func (h *OAuthHandler) StartGitHub(w http.ResponseWriter, r *http.Request) {
	if !h.Provider.Configured() {
		http.Error(w, "GitHub OAuth not configured on this server", http.StatusServiceUnavailable)
		return
	}

	state, err := randomToken()
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	// Carry the post-login redirect path through the OAuth round-trip.
	// Whitelist the path to internal app routes — never honour
	// arbitrary redirect URLs an attacker might inject.
	redirect := r.URL.Query().Get("redirect")
	if redirect == "" || !strings.HasPrefix(redirect, "/") || strings.HasPrefix(redirect, "//") {
		redirect = "/"
	}

	// Pack state + redirect into the cookie value: "<state>|<redirect>".
	// State is hex (no '|'), so the split is unambiguous.
	cookieVal := state + "|" + redirect
	secure := r.TLS != nil || (TrustedProxy && r.Header.Get("X-Forwarded-Proto") == "https")
	http.SetCookie(w, &http.Cookie{
		Name:     oauthStateCookie,
		Value:    cookieVal,
		Path:     "/api/auth",
		MaxAge:   int(oauthStateTTL.Seconds()),
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
	})

	authURL := buildGitHubAuthURL(h.Provider, state, h.callbackURL())
	http.Redirect(w, r, authURL, http.StatusFound)
}

// CallbackGitHub completes the OAuth flow. Verifies state, exchanges
// the authorization code for an access token, fetches the GitHub user
// profile, finds-or-creates a hopssh user, and mints a session. On
// success, 302-redirects to the path the user wanted before login.
func (h *OAuthHandler) CallbackGitHub(w http.ResponseWriter, r *http.Request) {
	if !h.Provider.Configured() {
		http.Error(w, "GitHub OAuth not configured on this server", http.StatusServiceUnavailable)
		return
	}

	// Pull the cookie set by /start. Missing / expired cookie is the
	// usual failure mode for a bookmarked or stale callback URL.
	c, err := r.Cookie(oauthStateCookie)
	if err != nil {
		http.Error(w, "OAuth state missing — start the login flow again", http.StatusBadRequest)
		return
	}
	parts := strings.SplitN(c.Value, "|", 2)
	if len(parts) != 2 {
		http.Error(w, "OAuth state malformed", http.StatusBadRequest)
		return
	}
	expectedState, redirectPath := parts[0], parts[1]

	// Clear the state cookie either way — this is one-shot.
	clearOAuthStateCookie(w, r)

	if r.URL.Query().Get("state") != expectedState {
		http.Error(w, "OAuth state mismatch — possible CSRF", http.StatusBadRequest)
		return
	}
	code := r.URL.Query().Get("code")
	if code == "" {
		// GitHub returns ?error=... when the user denies consent.
		errParam := r.URL.Query().Get("error")
		if errParam != "" {
			http.Error(w, "GitHub OAuth declined: "+errParam, http.StatusBadRequest)
			return
		}
		http.Error(w, "OAuth code missing", http.StatusBadRequest)
		return
	}

	// Exchange + fetch profile in a tight context so a slow GitHub
	// API doesn't hold the session.
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	accessToken, err := exchangeGitHubCode(ctx, h.Provider, code, h.callbackURL())
	if err != nil {
		http.Error(w, "OAuth code exchange failed: "+err.Error(), http.StatusBadGateway)
		return
	}

	profile, err := fetchGitHubProfile(ctx, accessToken)
	if err != nil {
		http.Error(w, "OAuth profile fetch failed: "+err.Error(), http.StatusBadGateway)
		return
	}

	user, err := h.findOrCreateUser(profile)
	if err != nil {
		http.Error(w, "internal error: "+err.Error(), http.StatusInternalServerError)
		return
	}

	token := generateSessionToken()
	if err := h.Sessions.Create(token, user.ID, sessionTTL); err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	setSessionCookie(w, r, token)

	if h.Audit != nil {
		h.Audit.Log(uuid.New().String(), user.ID, "login.github", nil, nil, nil)
	}

	http.Redirect(w, r, redirectPath, http.StatusFound)
}

// findOrCreateUser is the lookup-or-create primitive shared by the
// callback. Order: GitHub ID first (the canonical link); then email
// fallback to support legacy users who registered with a password
// before linking GitHub. New users get auto-created with a random
// password hash they can never use (forces them through OAuth or
// the password-reset flow we don't ship yet).
func (h *OAuthHandler) findOrCreateUser(p githubProfile) (*db.User, error) {
	if p.ID == 0 {
		return nil, errors.New("github profile missing user id")
	}
	githubID := strconv.FormatInt(p.ID, 10)

	if u, err := h.Users.GetByGitHubID(githubID); err != nil {
		return nil, err
	} else if u != nil {
		return u, nil
	}

	email := p.email()
	if email != "" {
		if u, err := h.Users.GetByEmail(email); err != nil {
			return nil, err
		} else if u != nil {
			// Existing email-registered user — link the GitHub ID.
			if err := h.Users.SetGitHubID(u.ID, githubID); err != nil {
				return nil, err
			}
			u.GitHubID = &githubID
			return u, nil
		}
	}

	// Brand-new user. Synthesise an email if GitHub didn't share one
	// (private-email setting on the user's account) using the username
	// at a synthetic domain — preserves the email-uniqueness invariant
	// without leaking real address.
	if email == "" {
		email = fmt.Sprintf("%s@users.noreply.github.com", p.Login)
	}
	name := p.Name
	if name == "" {
		name = p.Login
	}
	u := &db.User{
		ID:           uuid.New().String(),
		Email:        email,
		Name:         name,
		PasswordHash: "",
		GitHubID:     &githubID,
	}
	if err := h.Users.Create(u); err != nil {
		return nil, err
	}
	return u, nil
}

// callbackURL returns the absolute URL GitHub redirects back to. Built
// from the configured public endpoint to match the OAuth app config.
func (h *OAuthHandler) callbackURL() string {
	return strings.TrimRight(h.Provider.PublicEndpoint, "/") + "/api/auth/github/callback"
}

// --- helpers ---

func randomToken() (string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func clearOAuthStateCookie(w http.ResponseWriter, r *http.Request) {
	secure := r.TLS != nil || (TrustedProxy && r.Header.Get("X-Forwarded-Proto") == "https")
	http.SetCookie(w, &http.Cookie{
		Name:     oauthStateCookie,
		Value:    "",
		Path:     "/api/auth",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
	})
}

func buildGitHubAuthURL(p *OAuthProvider, state, callbackURL string) string {
	v := url.Values{}
	v.Set("client_id", p.GitHubClientID)
	v.Set("redirect_uri", callbackURL)
	v.Set("scope", "read:user user:email")
	v.Set("state", state)
	v.Set("allow_signup", "true")
	return "https://github.com/login/oauth/authorize?" + v.Encode()
}

// exchangeGitHubCode swaps the authorization code for an access token.
// Documented at https://docs.github.com/en/apps/oauth-apps/building-oauth-apps/authorizing-oauth-apps#2-users-are-redirected-back-to-your-site-by-github
func exchangeGitHubCode(ctx context.Context, p *OAuthProvider, code, callbackURL string) (string, error) {
	form := url.Values{}
	form.Set("client_id", p.GitHubClientID)
	form.Set("client_secret", p.GitHubClientSecret)
	form.Set("code", code)
	form.Set("redirect_uri", callbackURL)

	req, err := http.NewRequestWithContext(ctx, "POST", "https://github.com/login/oauth/access_token", strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	httpClient := &http.Client{
		Timeout:   10 * time.Second,
		Transport: &http.Transport{DisableKeepAlives: true},
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("github code exchange returned %d: %s", resp.StatusCode, string(body))
	}
	var out struct {
		AccessToken      string `json:"access_token"`
		Error            string `json:"error"`
		ErrorDescription string `json:"error_description"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return "", err
	}
	if out.Error != "" {
		return "", fmt.Errorf("github oauth error: %s — %s", out.Error, out.ErrorDescription)
	}
	if out.AccessToken == "" {
		return "", errors.New("github did not return an access token")
	}
	return out.AccessToken, nil
}

// githubProfile is the subset of GET /user we care about. ID is int64
// (GitHub user IDs fit in 64 bits — verified against api.github.com).
// Email is fetched separately because GET /user only includes the
// public email address; users with private email need GET /user/emails.
type githubProfile struct {
	ID         int64  `json:"id"`
	Login      string `json:"login"`
	Name       string `json:"name"`
	PublicMail string `json:"email"`
	emails     []githubEmail
}

type githubEmail struct {
	Email    string `json:"email"`
	Primary  bool   `json:"primary"`
	Verified bool   `json:"verified"`
}

// email returns the best available verified address. Public email
// (from /user) is preferred when set; otherwise the primary verified
// address from /user/emails. Empty string if no verified email exists.
func (p githubProfile) email() string {
	if p.PublicMail != "" {
		return p.PublicMail
	}
	for _, e := range p.emails {
		if e.Primary && e.Verified {
			return e.Email
		}
	}
	for _, e := range p.emails {
		if e.Verified {
			return e.Email
		}
	}
	return ""
}

func fetchGitHubProfile(ctx context.Context, accessToken string) (githubProfile, error) {
	httpClient := &http.Client{
		Timeout:   10 * time.Second,
		Transport: &http.Transport{DisableKeepAlives: true},
	}

	// 1) Profile.
	req, err := http.NewRequestWithContext(ctx, "GET", "https://api.github.com/user", nil)
	if err != nil {
		return githubProfile{}, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	resp, err := httpClient.Do(req)
	if err != nil {
		return githubProfile{}, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return githubProfile{}, fmt.Errorf("/user returned %d: %s", resp.StatusCode, string(body))
	}
	var p githubProfile
	if err := json.Unmarshal(body, &p); err != nil {
		return githubProfile{}, err
	}

	// 2) Emails (only needed if profile email is private).
	if p.PublicMail == "" {
		req2, err := http.NewRequestWithContext(ctx, "GET", "https://api.github.com/user/emails", nil)
		if err != nil {
			return p, nil // don't fail the whole flow
		}
		req2.Header.Set("Authorization", "Bearer "+accessToken)
		req2.Header.Set("Accept", "application/vnd.github+json")
		resp2, err := httpClient.Do(req2)
		if err != nil {
			return p, nil
		}
		defer resp2.Body.Close()
		if resp2.StatusCode == http.StatusOK {
			body2, _ := io.ReadAll(resp2.Body)
			_ = json.Unmarshal(body2, &p.emails)
		}
	}
	return p, nil
}
