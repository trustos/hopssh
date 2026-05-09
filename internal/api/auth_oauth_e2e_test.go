package api

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/trustos/hopssh/internal/db"
)

// TestOAuth_FullRoundTrip exercises the complete OAuth dance against a
// hermetic GitHub stub: /api/auth/github/start → GitHub authorize →
// /api/auth/github/callback → token exchange → user fetch → session
// minted. Covers the seam between handlers, real cookie flow, real
// querystring parsing, and the JSON shapes the GitHub API actually
// returns.
//
// Failure modes this catches that the unit tests miss:
// - Wrong query param names in the authorize URL (GitHub silently
//   ignores unknown params; users would see "redirect_uri mismatch").
// - Cookie scope mismatches (state cookie set on /api/auth but read
//   from /api/auth/github/callback works only if the path scope
//   includes the callback path).
// - State token round-trip — encoded once, decoded once, byte-for-byte
//   equal across the user agent.
// - Email-fallback ordering when GitHub's /user returns null email
//   AND /user/emails has the primary verified address.
// - End-to-end session cookie set on the final redirect.
func TestOAuth_FullRoundTrip(t *testing.T) {
	// 1. Spin up a stub GitHub that handles /login/oauth/access_token,
	//    /user, and /user/emails. Token + email content seeded below.
	const fakeAccessToken = "ghs_test_access_token"
	const githubUserID = int64(987654321)
	const githubLogin = "test-user"
	githubStub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/login/oauth/access_token":
			// Verify we sent the right form + secret.
			if err := r.ParseForm(); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			if r.PostFormValue("client_id") != "test-client-id" {
				http.Error(w, "wrong client_id", http.StatusBadRequest)
				return
			}
			if r.PostFormValue("client_secret") != "test-client-secret" {
				http.Error(w, "wrong client_secret", http.StatusBadRequest)
				return
			}
			if r.PostFormValue("code") == "" {
				http.Error(w, "missing code", http.StatusBadRequest)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"access_token":"%s","token_type":"bearer","scope":"read:user user:email"}`, fakeAccessToken)
		case "/user":
			if r.Header.Get("Authorization") != "Bearer "+fakeAccessToken {
				http.Error(w, "wrong token", http.StatusUnauthorized)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			// Note: empty `email` — forces fallback to /user/emails,
			// covers the private-email path.
			fmt.Fprintf(w, `{"id":%d,"login":"%s","name":"Test User","email":""}`, githubUserID, githubLogin)
		case "/user/emails":
			if r.Header.Get("Authorization") != "Bearer "+fakeAccessToken {
				http.Error(w, "wrong token", http.StatusUnauthorized)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `[{"email":"primary@example.com","primary":true,"verified":true},{"email":"old@example.com","primary":false,"verified":true}]`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer githubStub.Close()

	// 2. Redirect the OAuth handler's GitHub URLs at the test stub.
	//    These are package-level vars (not constants) precisely so
	//    this hermetic E2E can run without a real GitHub OAuth app.
	prevAuthorize, prevToken, prevUser, prevEmails := githubAuthorizeURL, githubTokenURL, githubUserURL, githubEmailsURL
	githubAuthorizeURL = githubStub.URL + "/login/oauth/authorize"
	githubTokenURL = githubStub.URL + "/login/oauth/access_token"
	githubUserURL = githubStub.URL + "/user"
	githubEmailsURL = githubStub.URL + "/user/emails"
	defer func() {
		githubAuthorizeURL, githubTokenURL, githubUserURL, githubEmailsURL = prevAuthorize, prevToken, prevUser, prevEmails
	}()

	// 3. Set up the real OAuthHandler against an in-memory DB.
	store, cleanup := newTestUserStore(t)
	defer cleanup()
	sessions := newTestSessionStore(t, store)
	auditPath := filepath.Join(t.TempDir(), "audit.db")
	_ = auditPath // not asserting on audit; just constructing handlers

	h := &OAuthHandler{
		Provider: &OAuthProvider{
			GitHubClientID:     "test-client-id",
			GitHubClientSecret: "test-client-secret",
			PublicEndpoint:     "https://hopssh.example",
		},
		Users:    store,
		Sessions: sessions,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/api/auth/oauth/status", h.Status)
	mux.HandleFunc("/api/auth/github/start", h.StartGitHub)
	mux.HandleFunc("/api/auth/github/callback", h.CallbackGitHub)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	// 4. Use a real cookie jar so the state cookie set by /start is
	//    sent back on /callback automatically — same way the browser
	//    would do it.
	jar, _ := cookiejar.New(nil)
	client := &http.Client{
		Jar: jar,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			// Don't follow the GitHub authorize redirect — we'll
			// inspect it + simulate the user-consent step ourselves.
			return http.ErrUseLastResponse
		},
	}

	// 5. Hit /api/auth/oauth/status — should report github: true.
	statusResp, err := client.Get(srv.URL + "/api/auth/oauth/status")
	if err != nil {
		t.Fatal(err)
	}
	defer statusResp.Body.Close()
	body, _ := io.ReadAll(statusResp.Body)
	var statusBody struct{ Github bool }
	_ = json.Unmarshal(body, &statusBody)
	if !statusBody.Github {
		t.Fatalf("/api/auth/oauth/status returned github=false; configured handler should report true. body=%s", body)
	}

	// 6. Hit /api/auth/github/start — should 302 to the (stub) GitHub authorize.
	startResp, err := client.Get(srv.URL + "/api/auth/github/start?redirect=/networks")
	if err != nil {
		t.Fatal(err)
	}
	defer startResp.Body.Close()
	if startResp.StatusCode != http.StatusFound {
		t.Fatalf("/api/auth/github/start returned %d, want 302", startResp.StatusCode)
	}
	authorizeURL := startResp.Header.Get("Location")
	if !strings.HasPrefix(authorizeURL, githubStub.URL+"/login/oauth/authorize") {
		t.Fatalf("authorize URL wrong: %q (want prefix %s)", authorizeURL, githubStub.URL+"/login/oauth/authorize")
	}

	// 7. Parse out the state token + verify the redirect_uri.
	authU, _ := url.Parse(authorizeURL)
	stateToken := authU.Query().Get("state")
	if stateToken == "" {
		t.Fatal("state token missing from authorize URL")
	}
	if got := authU.Query().Get("redirect_uri"); got != "https://hopssh.example/api/auth/github/callback" {
		t.Errorf("redirect_uri = %q, want https://hopssh.example/api/auth/github/callback", got)
	}
	if got := authU.Query().Get("scope"); got != "read:user user:email" {
		t.Errorf("scope = %q, want 'read:user user:email'", got)
	}

	// 8. Cookie jar should have the oauth_state cookie scoped to
	//    /api/auth. Probe by querying the jar with a URL inside that
	//    path scope (the callback URL); a bare base URL would NOT
	//    match because Go's cookiejar respects Path scoping.
	probeU, _ := url.Parse(srv.URL + "/api/auth/github/callback")
	cookies := jar.Cookies(probeU)
	var stateCookieFound bool
	for _, c := range cookies {
		if c.Name == oauthStateCookie {
			stateCookieFound = true
			break
		}
	}
	if !stateCookieFound {
		t.Fatalf("oauth_state cookie missing from jar at /api/auth scope; cookies=%v", cookies)
	}

	// 9. Simulate GitHub redirecting back to our callback with code +
	//    matching state. This is the post-consent moment.
	const fakeCode = "auth-code-from-github"
	callbackURL := fmt.Sprintf("%s/api/auth/github/callback?code=%s&state=%s",
		srv.URL, fakeCode, url.QueryEscape(stateToken))
	cbResp, err := client.Get(callbackURL)
	if err != nil {
		t.Fatal(err)
	}
	defer cbResp.Body.Close()

	// 10. Callback should 302 to the original redirect=/networks.
	if cbResp.StatusCode != http.StatusFound {
		body, _ := io.ReadAll(cbResp.Body)
		t.Fatalf("/api/auth/github/callback returned %d (want 302). body=%s", cbResp.StatusCode, body)
	}
	if loc := cbResp.Header.Get("Location"); loc != "/networks" {
		t.Errorf("post-OAuth redirect = %q, want /networks", loc)
	}

	// 11. Session cookie must have been set on the response.
	var sessionCookieValue string
	for _, c := range cbResp.Cookies() {
		if c.Name == "session" {
			sessionCookieValue = c.Value
			break
		}
	}
	if sessionCookieValue == "" {
		t.Fatal("session cookie not set on callback response")
	}

	// 12. The user record must exist with the right GitHub ID + the
	//     fallback email from /user/emails.
	user, err := h.Users.GetByGitHubID(fmt.Sprintf("%d", githubUserID))
	if err != nil {
		t.Fatal(err)
	}
	if user == nil {
		t.Fatal("user not created for GitHub ID")
	}
	if user.Email != "primary@example.com" {
		t.Errorf("user.Email = %q, want primary@example.com (from /user/emails)", user.Email)
	}
	if user.Name != "Test User" {
		t.Errorf("user.Name = %q, want Test User", user.Name)
	}

	// 13. Second login (returning user) — should NOT create a duplicate.
	jar2, _ := cookiejar.New(nil)
	client2 := &http.Client{
		Jar:           jar2,
		CheckRedirect: func(req *http.Request, via []*http.Request) error { return http.ErrUseLastResponse },
	}
	startResp2, _ := client2.Get(srv.URL + "/api/auth/github/start")
	authU2, _ := url.Parse(startResp2.Header.Get("Location"))
	stateToken2 := authU2.Query().Get("state")
	cb2URL := fmt.Sprintf("%s/api/auth/github/callback?code=second-code&state=%s",
		srv.URL, url.QueryEscape(stateToken2))
	cb2Resp, err := client2.Get(cb2URL)
	if err != nil {
		t.Fatal(err)
	}
	defer cb2Resp.Body.Close()
	if cb2Resp.StatusCode != http.StatusFound {
		body, _ := io.ReadAll(cb2Resp.Body)
		t.Fatalf("second login returned %d. body=%s", cb2Resp.StatusCode, body)
	}

	// Count users — must be exactly 1 (no duplicate created).
	count, _ := h.Users.Count()
	if count != 1 {
		t.Errorf("user count after second login = %d, want 1 (no duplicate)", count)
	}
}

// (The old oauthRoundTripperRedirect helper was replaced with direct
// override of githubAuthorizeURL / githubTokenURL / githubUserURL /
// githubEmailsURL package vars in the test setup above. Cleaner than
// patching http.DefaultTransport, which doesn't reach the OAuth
// handler's per-request httpClient anyway.)

// newTestUserStore + newTestSessionStore set up SQLite stores wired to
// a tempdir DB. Mirrors what cmd/server/main.go does in production but
// without the encryption layer (not needed for OAuth flow tests).
func newTestUserStore(t *testing.T) (*db.UserStore, func()) {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	pair, err := db.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(pair.WriteDB); err != nil {
		t.Fatal(err)
	}
	cleanup := func() {
		pair.Close()
	}
	return db.NewUserStore(pair), cleanup
}

func newTestSessionStore(t *testing.T, users *db.UserStore) *db.SessionStore {
	t.Helper()
	// SessionStore needs the same DBPair the UserStore uses.
	// Reach into UserStore is overkill — open a sibling pair against
	// the same path. In production both stores share one pair.
	// Fast path: open the pair via reflection-on-sibling? Simpler: skip
	// — we just need a pair with the schema applied. The user store
	// already initialised the schema; opening a fresh pair against
	// the same path is fine for read+write.
	dir := filepath.Dir(t.TempDir())
	_ = dir
	// Use the same DBPair pattern; the UserStore exposes nothing.
	// For the test we just need `Sessions.Create(token, userID, ttl)`
	// to succeed. Construct a no-op fake that matches the production
	// SessionStore interface. Easiest: open a fresh in-memory DB.
	pair, err := db.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(pair.WriteDB); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pair.Close() })
	return db.NewSessionStore(pair)
}

// Suppress unused-import warnings if rand / hex / sql become dead.
var _ = rand.Read
var _ = hex.EncodeToString
var _ = sql.ErrNoRows
var _ = os.Stdin
