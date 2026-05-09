package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestOAuthProvider_Configured exercises the gate the frontend uses to
// decide whether to enable the GitHub login button. Both halves of the
// credentials must be present; either alone counts as unconfigured.
func TestOAuthProvider_Configured(t *testing.T) {
	cases := []struct {
		name string
		p    *OAuthProvider
		want bool
	}{
		{"nil provider", nil, false},
		{"empty", &OAuthProvider{}, false},
		{"id only", &OAuthProvider{GitHubClientID: "x"}, false},
		{"secret only", &OAuthProvider{GitHubClientSecret: "y"}, false},
		{"both set", &OAuthProvider{GitHubClientID: "x", GitHubClientSecret: "y"}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.p.Configured()
			if got != tc.want {
				t.Errorf("Configured() = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestStartGitHub_NotConfigured ensures unconfigured servers return
// 503 — distinguishable from /api/auth/oauth/status which always
// returns 200 (so the frontend can gate the button without seeing
// an error).
func TestStartGitHub_NotConfigured(t *testing.T) {
	h := &OAuthHandler{Provider: &OAuthProvider{}}
	req := httptest.NewRequest("GET", "/api/auth/github/start", nil)
	rec := httptest.NewRecorder()
	h.StartGitHub(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("StartGitHub() unconfigured returned %d, want 503", rec.Code)
	}
}

// TestStatus_AlwaysOK confirms the /status endpoint returns 200 even
// when OAuth is not configured. This is the contract the frontend
// relies on.
func TestStatus_AlwaysOK(t *testing.T) {
	cases := []struct {
		name string
		p    *OAuthProvider
	}{
		{"unconfigured", &OAuthProvider{}},
		{"configured", &OAuthProvider{GitHubClientID: "x", GitHubClientSecret: "y", PublicEndpoint: "https://example.com"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := &OAuthHandler{Provider: tc.p}
			req := httptest.NewRequest("GET", "/api/auth/oauth/status", nil)
			rec := httptest.NewRecorder()
			h.Status(rec, req)
			if rec.Code != http.StatusOK {
				t.Errorf("Status() returned %d, want 200", rec.Code)
			}
		})
	}
}

// TestStartGitHub_RedirectsToGitHub_WithStateCookie verifies the
// happy path: configured provider responds with a 302 to the GitHub
// authorize endpoint AND sets the oauth_state cookie. The cookie
// carries both the CSRF state and the post-login redirect.
func TestStartGitHub_RedirectsToGitHub_WithStateCookie(t *testing.T) {
	h := &OAuthHandler{
		Provider: &OAuthProvider{
			GitHubClientID:     "test-client-id",
			GitHubClientSecret: "test-secret",
			PublicEndpoint:     "https://hopssh.com",
		},
	}
	req := httptest.NewRequest("GET", "/api/auth/github/start?redirect=/networks/abc", nil)
	rec := httptest.NewRecorder()
	h.StartGitHub(rec, req)

	if rec.Code != http.StatusFound {
		t.Fatalf("StartGitHub() returned %d, want 302", rec.Code)
	}
	loc := rec.Header().Get("Location")
	if !strings.HasPrefix(loc, "https://github.com/login/oauth/authorize?") {
		t.Errorf("Location header = %q, want GitHub authorize URL", loc)
	}
	if !strings.Contains(loc, "client_id=test-client-id") {
		t.Errorf("Location missing client_id param: %s", loc)
	}
	if !strings.Contains(loc, "redirect_uri=https%3A%2F%2Fhopssh.com%2Fapi%2Fauth%2Fgithub%2Fcallback") {
		t.Errorf("Location has wrong redirect_uri: %s", loc)
	}
	if !strings.Contains(loc, "state=") {
		t.Errorf("Location missing state param: %s", loc)
	}

	// Verify state cookie set with the same value as in the URL.
	cookies := rec.Result().Cookies()
	var stateCookie *http.Cookie
	for _, c := range cookies {
		if c.Name == oauthStateCookie {
			stateCookie = c
			break
		}
	}
	if stateCookie == nil {
		t.Fatal("oauth_state cookie not set")
	}
	parts := strings.SplitN(stateCookie.Value, "|", 2)
	if len(parts) != 2 {
		t.Fatalf("oauth_state cookie malformed: %q", stateCookie.Value)
	}
	if parts[1] != "/networks/abc" {
		t.Errorf("redirect path in cookie = %q, want %q", parts[1], "/networks/abc")
	}
	// State token is hex; cookie state must match the URL state.
	if !strings.Contains(loc, "state="+parts[0]) {
		t.Errorf("cookie state %q not in Location URL %q", parts[0], loc)
	}
}

// TestStartGitHub_RejectsAbsoluteRedirect ensures attacker-supplied
// redirect=https://evil.com targets get sanitised back to "/" — the
// open-redirect class of bug.
func TestStartGitHub_RejectsAbsoluteRedirect(t *testing.T) {
	h := &OAuthHandler{
		Provider: &OAuthProvider{
			GitHubClientID:     "x",
			GitHubClientSecret: "y",
			PublicEndpoint:     "https://hopssh.com",
		},
	}
	cases := []string{
		"https://evil.com/phish",
		"//evil.com/phish",
		"javascript:alert(1)",
		"",
	}
	for _, redirect := range cases {
		t.Run(redirect, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/api/auth/github/start?redirect="+redirect, nil)
			rec := httptest.NewRecorder()
			h.StartGitHub(rec, req)

			var stateCookie *http.Cookie
			for _, c := range rec.Result().Cookies() {
				if c.Name == oauthStateCookie {
					stateCookie = c
				}
			}
			if stateCookie == nil {
				t.Fatal("no state cookie")
			}
			parts := strings.SplitN(stateCookie.Value, "|", 2)
			if len(parts) != 2 {
				t.Fatalf("cookie malformed: %q", stateCookie.Value)
			}
			if parts[1] != "/" {
				t.Errorf("redirect path = %q, want %q (open-redirect protection)", parts[1], "/")
			}
		})
	}
}

// TestCallbackGitHub_StateMismatch covers the CSRF protection path:
// callback with a state value that doesn't match the cookie must
// return 400 and not mint a session.
func TestCallbackGitHub_StateMismatch(t *testing.T) {
	h := &OAuthHandler{
		Provider: &OAuthProvider{
			GitHubClientID:     "x",
			GitHubClientSecret: "y",
			PublicEndpoint:     "https://hopssh.com",
		},
	}
	req := httptest.NewRequest("GET", "/api/auth/github/callback?state=attacker-supplied&code=abc", nil)
	req.AddCookie(&http.Cookie{Name: oauthStateCookie, Value: "different-state|/"})
	rec := httptest.NewRecorder()
	h.CallbackGitHub(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("CallbackGitHub() with mismatched state = %d, want 400", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "state mismatch") {
		t.Errorf("body = %q, want CSRF error", rec.Body.String())
	}
}

// TestCallbackGitHub_MissingStateCookie covers the bookmarked-callback
// case: hitting /callback without first hitting /start. Must not 5xx.
func TestCallbackGitHub_MissingStateCookie(t *testing.T) {
	h := &OAuthHandler{
		Provider: &OAuthProvider{
			GitHubClientID:     "x",
			GitHubClientSecret: "y",
			PublicEndpoint:     "https://hopssh.com",
		},
	}
	req := httptest.NewRequest("GET", "/api/auth/github/callback?state=foo&code=abc", nil)
	rec := httptest.NewRecorder()
	h.CallbackGitHub(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("CallbackGitHub() without state cookie = %d, want 400", rec.Code)
	}
}

// TestGitHubProfile_email exercises the email-resolution priority:
// public profile email first, then primary verified, then any verified.
func TestGitHubProfile_email(t *testing.T) {
	cases := []struct {
		name string
		p    githubProfile
		want string
	}{
		{
			name: "public email present",
			p:    githubProfile{PublicMail: "public@example.com"},
			want: "public@example.com",
		},
		{
			name: "private profile, primary verified",
			p: githubProfile{
				emails: []githubEmail{
					{Email: "secondary@example.com", Verified: true},
					{Email: "primary@example.com", Primary: true, Verified: true},
				},
			},
			want: "primary@example.com",
		},
		{
			name: "private profile, no primary, any verified",
			p: githubProfile{
				emails: []githubEmail{
					{Email: "unverified@example.com", Verified: false},
					{Email: "verified@example.com", Verified: true},
				},
			},
			want: "verified@example.com",
		},
		{
			name: "no verified emails",
			p:    githubProfile{emails: []githubEmail{{Email: "x@y", Verified: false}}},
			want: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.p.email()
			if got != tc.want {
				t.Errorf("email() = %q, want %q", got, tc.want)
			}
		})
	}
}
