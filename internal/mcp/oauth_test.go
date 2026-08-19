package mcp

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// The well-known suffix is INSERTED between host and path, not appended. Get
// this wrong and every path-scoped server 404s during discovery.
func TestWellKnownURLs(t *testing.T) {
	cases := []struct {
		name   string
		raw    string
		suffix string
		want   string
	}{
		{"path is scoped after the suffix", "https://mcp.example.com/mcp", "oauth-protected-resource",
			"https://mcp.example.com/.well-known/oauth-protected-resource/mcp"},
		{"no path", "https://mcp.example.com", "oauth-protected-resource",
			"https://mcp.example.com/.well-known/oauth-protected-resource"},
		{"trailing slash is stripped first", "https://mcp.example.com/mcp/", "oauth-protected-resource",
			"https://mcp.example.com/.well-known/oauth-protected-resource/mcp"},
		{"deep path", "https://mcp.example.com/a/b/c", "oauth-authorization-server",
			"https://mcp.example.com/.well-known/oauth-authorization-server/a/b/c"},
		{"issuer with tenant", "https://auth.example.com/tenant1", "oauth-authorization-server",
			"https://auth.example.com/.well-known/oauth-authorization-server/tenant1"},
		{"query and fragment dropped", "https://mcp.example.com/mcp?x=1#f", "oauth-protected-resource",
			"https://mcp.example.com/.well-known/oauth-protected-resource/mcp"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := wellKnown(c.raw, c.suffix); got != c.want {
				t.Errorf("wellKnown(%q, %q) = %q, want %q", c.raw, c.suffix, got, c.want)
			}
		})
	}
}

func TestLegacyWellKnown(t *testing.T) {
	got := legacyWellKnown("https://auth.example.com/tenant1", "openid-configuration")
	want := "https://auth.example.com/tenant1/.well-known/openid-configuration"
	if got != want {
		t.Errorf("legacyWellKnown = %q, want %q", got, want)
	}
}

// A path-bearing issuer has three candidate locations because RFC 8414 and the
// older OpenID form disagree; a root issuer has two.
func TestAuthServerMetadataURLs(t *testing.T) {
	cases := []struct {
		issuer string
		want   []string
	}{
		{"https://auth.example.com/tenant1", []string{
			"https://auth.example.com/.well-known/oauth-authorization-server/tenant1",
			"https://auth.example.com/.well-known/openid-configuration/tenant1",
			"https://auth.example.com/tenant1/.well-known/openid-configuration",
		}},
		{"https://auth.example.com", []string{
			"https://auth.example.com/.well-known/oauth-authorization-server",
			"https://auth.example.com/.well-known/openid-configuration",
		}},
		{"https://auth.example.com/", []string{
			"https://auth.example.com/.well-known/oauth-authorization-server",
			"https://auth.example.com/.well-known/openid-configuration",
		}},
	}
	for _, c := range cases {
		got := authServerMetadataURLs(c.issuer)
		if len(got) != len(c.want) {
			t.Fatalf("%s: got %v, want %v", c.issuer, got, c.want)
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("%s: [%d] = %q, want %q", c.issuer, i, got[i], c.want[i])
			}
		}
	}
}

// The path-scoped document is tried before the host-wide one, so a host serving
// several MCP servers gets the right metadata.
func TestResourceMetadataURLOrder(t *testing.T) {
	got := resourceMetadataURLs("https://mcp.example.com/mcp")
	want := []string{
		"https://mcp.example.com/.well-known/oauth-protected-resource/mcp",
		"https://mcp.example.com/.well-known/oauth-protected-resource",
	}
	if len(got) != 2 || got[0][0] != want[0] || got[1][0] != want[1] {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestCanonicalResource(t *testing.T) {
	cases := []struct{ in, want string }{
		{"HTTPS://MCP.Example.COM/MCP", "https://mcp.example.com/MCP"},
		{"https://mcp.example.com/mcp/", "https://mcp.example.com/mcp"},
		{"https://mcp.example.com/mcp#frag", "https://mcp.example.com/mcp"},
		{"https://mcp.example.com", "https://mcp.example.com"},
		{"not a url", ""},
		{"", ""},
	}
	for _, c := range cases {
		if got := canonicalResource(c.in); got != c.want {
			t.Errorf("canonicalResource(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// The verifier must be 43 unpadded base64url characters, and the challenge must
// hash the verifier STRING — hashing the raw random bytes instead is the classic
// PKCE bug and only shows up as a rejection at the token endpoint.
func TestPKCE(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 50; i++ {
		v, err := newVerifier()
		if err != nil {
			t.Fatalf("newVerifier: %v", err)
		}
		if len(v) != 43 {
			t.Fatalf("verifier length = %d, want 43 (%q)", len(v), v)
		}
		if strings.ContainsAny(v, "=+/") {
			t.Fatalf("verifier %q is not unpadded base64url", v)
		}
		if _, err := base64.RawURLEncoding.DecodeString(v); err != nil {
			t.Fatalf("verifier %q does not decode: %v", v, err)
		}
		if seen[v] {
			t.Fatalf("verifier repeated: %q", v)
		}
		seen[v] = true

		sum := sha256.Sum256([]byte(v))
		want := base64.RawURLEncoding.EncodeToString(sum[:])
		if got := codeChallenge(v); got != want {
			t.Fatalf("codeChallenge(%q) = %q, want %q", v, got, want)
		}
	}
}

// A challenge parser that splits on every comma breaks the moment a server
// sends a multi-scope value, which is exactly when it matters.
func TestParseChallenge(t *testing.T) {
	cases := []struct {
		name  string
		in    string
		md    string
		scope []string
		err   string
	}{
		{
			name: "simple",
			in:   `Bearer resource_metadata="https://mcp.example.com/.well-known/oauth-protected-resource"`,
			md:   "https://mcp.example.com/.well-known/oauth-protected-resource",
		},
		{
			name:  "quoted value containing commas",
			in:    `Bearer error="insufficient_scope", scope="read,write profile", resource_metadata="https://a/b"`,
			md:    "https://a/b",
			scope: []string{"read,write", "profile"},
			err:   "insufficient_scope",
		},
		{
			name:  "case-insensitive param names",
			in:    `Bearer Resource_Metadata="https://a/b", SCOPE="x y", Error="invalid_token"`,
			md:    "https://a/b",
			scope: []string{"x", "y"},
			err:   "invalid_token",
		},
		{
			name: "unquoted value",
			in:   `Bearer resource_metadata=https://a/b`,
			md:   "https://a/b",
		},
		{
			name: "realm first",
			in:   `Bearer realm="example", resource_metadata="https://a/b"`,
			md:   "https://a/b",
		},
		{
			name: "no bearer params",
			in:   `Basic realm="x"`,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := parseChallenge(c.in)
			if got.ResourceMetadata != c.md {
				t.Errorf("resource_metadata = %q, want %q", got.ResourceMetadata, c.md)
			}
			if got.Err != c.err {
				t.Errorf("error = %q, want %q", got.Err, c.err)
			}
			if strings.Join(got.Scopes, "|") != strings.Join(c.scope, "|") {
				t.Errorf("scope = %v, want %v", got.Scopes, c.scope)
			}
		})
	}
}

// A server may send WWW-Authenticate more than once; all instances have to be
// considered, not just the first.
func TestChallengeFromMultipleHeaders(t *testing.T) {
	resp := &http.Response{Header: http.Header{}}
	resp.Header.Add("WWW-Authenticate", `Basic realm="legacy"`)
	resp.Header.Add("WWW-Authenticate", `Bearer resource_metadata="https://a/b", scope="read write"`)
	c := parseChallenge(authChallenge(resp))
	if c.ResourceMetadata != "https://a/b" {
		t.Errorf("resource_metadata = %q", c.ResourceMetadata)
	}
	if strings.Join(c.Scopes, " ") != "read write" {
		t.Errorf("scope = %v", c.Scopes)
	}
}

// A 403 that says insufficient_scope is a step-up request and must be retried;
// a plain 403 is a refusal and must not be.
func TestNeedsAuth(t *testing.T) {
	mk := func(code int, hdr string) *http.Response {
		r := &http.Response{StatusCode: code, Header: http.Header{}}
		if hdr != "" {
			r.Header.Add("WWW-Authenticate", hdr)
		}
		return r
	}
	if !needsAuth(mk(401, "")) {
		t.Error("401 should need auth")
	}
	if !needsAuth(mk(403, `Bearer error="insufficient_scope", scope="admin"`)) {
		t.Error("403 insufficient_scope should need auth")
	}
	if needsAuth(mk(403, "")) {
		t.Error("plain 403 should not need auth")
	}
	if needsAuth(mk(200, "")) {
		t.Error("200 should not need auth")
	}
}

// fakeAS is an authorization server: metadata, dynamic registration, an
// authorize endpoint that redirects straight back, and a token endpoint that
// verifies PKCE and the resource parameter.
type fakeAS struct {
	srv *httptest.Server
	mu  sync.Mutex

	// issued maps an access token to the scope it was granted with.
	issued map[string]string
	// codes maps an authorization code to the challenge it was bound to.
	codes map[string]string
	// refreshTokens are the ones currently valid.
	refreshTokens map[string]bool

	registrations int
	// rotateRefresh makes every refresh mint a new refresh token, as a rotating
	// server does, so the persist-before-use rule can be checked.
	rotateRefresh bool
	// noPKCE drops code_challenge_methods_supported from the metadata.
	noPKCE bool
	// wrongIssuer makes the metadata claim someone else's identity.
	wrongIssuer string
	// lastAuthQuery is the query of the most recent authorization request.
	lastAuthQuery url.Values
	// lastTokenForm is the form of the most recent token request.
	lastTokenForm url.Values
	// nextAccessToken, when set, is what the next exchange or refresh issues.
	nextAccessToken string
	seq             int
}

func newFakeAS(t *testing.T) *fakeAS {
	t.Helper()
	as := &fakeAS{
		issued:        map[string]string{},
		codes:         map[string]string{},
		refreshTokens: map[string]bool{},
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/oauth-authorization-server", func(w http.ResponseWriter, r *http.Request) {
		as.mu.Lock()
		defer as.mu.Unlock()
		iss := as.srv.URL
		if as.wrongIssuer != "" {
			iss = as.wrongIssuer
		}
		meta := map[string]any{
			"issuer":                 iss,
			"authorization_endpoint": as.srv.URL + "/authorize",
			"token_endpoint":         as.srv.URL + "/token",
			"registration_endpoint":  as.srv.URL + "/register",
			"grant_types_supported":  []string{"authorization_code", "refresh_token"},
		}
		if !as.noPKCE {
			meta["code_challenge_methods_supported"] = []string{"S256"}
		}
		writeJSON(w, 200, meta)
	})
	mux.HandleFunc("/register", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		// A public client must ask for no token endpoint auth and must list
		// refresh_token, or it will never be given one.
		if body["token_endpoint_auth_method"] != "none" {
			http.Error(w, "expected token_endpoint_auth_method=none", 400)
			return
		}
		gts, _ := body["grant_types"].([]any)
		if len(gts) != 2 || gts[0] != "authorization_code" || gts[1] != "refresh_token" {
			http.Error(w, "expected authorization_code and refresh_token", 400)
			return
		}
		uris, _ := body["redirect_uris"].([]any)
		if len(uris) != 1 || !strings.HasPrefix(uris[0].(string), "http://127.0.0.1:") {
			http.Error(w, "expected a loopback redirect uri", 400)
			return
		}
		as.mu.Lock()
		as.registrations++
		n := as.registrations
		as.mu.Unlock()
		// Substitute the client id so the caller is forced to read it back.
		writeJSON(w, 201, map[string]any{"client_id": fmt.Sprintf("client-%d", n)})
	})
	mux.HandleFunc("/authorize", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		as.mu.Lock()
		as.lastAuthQuery = q
		as.seq++
		code := fmt.Sprintf("code-%d", as.seq)
		as.codes[code] = q.Get("code_challenge")
		as.mu.Unlock()
		if q.Get("code_challenge_method") != "S256" {
			http.Error(w, "expected S256", 400)
			return
		}
		back, err := url.Parse(q.Get("redirect_uri"))
		if err != nil {
			http.Error(w, "bad redirect_uri", 400)
			return
		}
		rq := back.Query()
		rq.Set("code", code)
		rq.Set("state", q.Get("state"))
		back.RawQuery = rq.Encode()
		http.Redirect(w, r, back.String(), http.StatusFound)
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		as.mu.Lock()
		defer as.mu.Unlock()
		as.lastTokenForm = r.PostForm
		if r.Header.Get("Authorization") != "" {
			http.Error(w, "public client must not authenticate", 400)
			return
		}
		switch r.PostForm.Get("grant_type") {
		case "authorization_code":
			want, ok := as.codes[r.PostForm.Get("code")]
			if !ok {
				writeJSON(w, 400, map[string]any{"error": "invalid_grant"})
				return
			}
			delete(as.codes, r.PostForm.Get("code"))
			if codeChallenge(r.PostForm.Get("code_verifier")) != want {
				writeJSON(w, 400, map[string]any{"error": "invalid_grant", "error_description": "pkce mismatch"})
				return
			}
		case "refresh_token":
			if !as.refreshTokens[r.PostForm.Get("refresh_token")] {
				writeJSON(w, 400, map[string]any{"error": "invalid_grant"})
				return
			}
			if as.rotateRefresh {
				delete(as.refreshTokens, r.PostForm.Get("refresh_token"))
			}
		default:
			writeJSON(w, 400, map[string]any{"error": "unsupported_grant_type"})
			return
		}
		as.seq++
		at := as.nextAccessToken
		if at == "" {
			at = fmt.Sprintf("access-%d", as.seq)
		}
		as.nextAccessToken = ""
		rt := fmt.Sprintf("refresh-%d", as.seq)
		as.issued[at] = r.PostForm.Get("scope")
		as.refreshTokens[rt] = true
		writeJSON(w, 200, map[string]any{
			"access_token":  at,
			"token_type":    "Bearer",
			"expires_in":    3600,
			"refresh_token": rt,
		})
	})
	as.srv = httptest.NewServer(mux)
	t.Cleanup(as.srv.Close)
	return as
}

func (a *fakeAS) valid(tok string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	_, ok := a.issued[tok]
	return ok
}

func (a *fakeAS) revoke(tok string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.issued, tok)
}

// fakeMCP is a Streamable-HTTP MCP server behind OAuth: it 401s an
// unauthenticated request with a resource_metadata pointer, and answers the
// handshake once a valid token arrives.
type fakeMCP struct {
	srv *httptest.Server
	as  *fakeAS
	mu  sync.Mutex
	// prmResource, when set, is what the metadata document claims — used to
	// check that a mismatched identifier is rejected.
	prmResource string
	// calls counts authenticated JSON-RPC posts.
	calls int
	// tokensSeen records the bearer of every authenticated post.
	tokensSeen []string
}

func newFakeMCP(t *testing.T, as *fakeAS) *fakeMCP {
	t.Helper()
	m := &fakeMCP{as: as}
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/oauth-protected-resource/mcp", func(w http.ResponseWriter, r *http.Request) {
		m.mu.Lock()
		res := m.prmResource
		if res == "" {
			res = m.srv.URL + "/mcp"
		}
		m.mu.Unlock()
		writeJSON(w, 200, map[string]any{
			"resource":              res,
			"authorization_servers": []string{as.srv.URL},
			"scopes_supported":      []string{"mcp:read"},
		})
	})
	mux.HandleFunc("/mcp", func(w http.ResponseWriter, r *http.Request) {
		tok := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if tok == "" || !as.valid(tok) {
			w.Header().Set("WWW-Authenticate",
				fmt.Sprintf(`Bearer resource_metadata="%s/.well-known/oauth-protected-resource/mcp", error="invalid_token"`, m.srv.URL))
			http.Error(w, `{"error":"unauthorized"}`, 401)
			return
		}
		m.mu.Lock()
		m.calls++
		m.tokensSeen = append(m.tokensSeen, tok)
		m.mu.Unlock()

		var req struct {
			ID     int    `json:"id"`
			Method string `json:"method"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		switch req.Method {
		case "initialize":
			writeJSON(w, 200, map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": map[string]any{
				"protocolVersion": "2024-11-05",
				"capabilities":    map[string]any{"tools": map[string]any{}},
			}})
		case "tools/list":
			writeJSON(w, 200, map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": map[string]any{
				"tools": []map[string]any{{"name": "echo", "description": "echo it back"}},
			}})
		case "ping":
			writeJSON(w, 200, map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": map[string]any{}})
		default:
			w.WriteHeader(202)
		}
	})
	m.srv = httptest.NewServer(mux)
	t.Cleanup(m.srv.Close)
	return m
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

// browserFor returns an open func that plays the part of a browser: it fetches
// the authorization URL and follows the redirect back to the loopback listener,
// which is exactly what a real login ends with.
func browserFor(t *testing.T) func(string) error {
	t.Helper()
	return func(u string) error {
		go func() {
			resp, err := http.Get(u)
			if err != nil {
				t.Logf("browser: %v", err)
				return
			}
			resp.Body.Close()
		}()
		return nil
	}
}

// The whole point: an unauthenticated 401 must lead to a login and a working
// session, without the caller doing anything.
func TestFullFlow(t *testing.T) {
	as := newFakeAS(t)
	m := newFakeMCP(t, as)
	store := NewFileStore(filepath.Join(t.TempDir(), "tokens.json"))

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	c, err := Start(ctx, "auth", Server{
		Type:        "http",
		URL:         m.srv.URL + "/mcp",
		OAuth:       &OAuth{},
		TokenStore:  store,
		OpenBrowser: browserFor(t),
	})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	defer c.Close()

	if len(c.Tools()) != 1 {
		t.Fatalf("tools = %d, want 1", len(c.Tools()))
	}

	// The token must have been bound to this resource, at both ends of the flow.
	as.mu.Lock()
	authRes := as.lastAuthQuery.Get("resource")
	authScope := as.lastAuthQuery.Get("scope")
	tokRes := as.lastTokenForm.Get("resource")
	tokRedirect := as.lastTokenForm.Get("redirect_uri")
	regs := as.registrations
	as.mu.Unlock()
	want := canonicalResource(m.srv.URL + "/mcp")
	if authRes != want {
		t.Errorf("authorize resource = %q, want %q", authRes, want)
	}
	if tokRes != want {
		t.Errorf("token resource = %q, want %q", tokRes, want)
	}
	if !strings.HasPrefix(tokRedirect, "http://127.0.0.1:") {
		t.Errorf("redirect_uri = %q, want a 127.0.0.1 loopback", tokRedirect)
	}
	// scopes_supported from the resource metadata is what gets asked for when
	// nothing else names a scope.
	if authScope != "mcp:read" {
		t.Errorf("authorize scope = %q, want mcp:read", authScope)
	}
	if regs != 1 {
		t.Errorf("registrations = %d, want 1", regs)
	}

	// The token landed on disk, so a second connection reuses it rather than
	// opening another browser.
	stored, err := store.Load("", want)
	if err != nil || stored == nil {
		t.Fatalf("token not stored: %v", err)
	}
	if stored.AccessToken == "" || stored.RefreshToken == "" {
		t.Fatal("stored token is missing its access or refresh token")
	}
	if stored.Issuer != as.srv.URL {
		t.Errorf("stored issuer = %q, want %q", stored.Issuer, as.srv.URL)
	}

	c2, err := Start(ctx, "auth2", Server{
		Type:       "http",
		URL:        m.srv.URL + "/mcp",
		OAuth:      &OAuth{},
		TokenStore: store,
		OpenBrowser: func(string) error {
			t.Error("second connection should not have opened a browser")
			return nil
		},
	})
	if err != nil {
		t.Fatalf("second start: %v", err)
	}
	defer c2.Close()
	as.mu.Lock()
	regs2 := as.registrations
	as.mu.Unlock()
	if regs2 != 1 {
		t.Errorf("registrations after reuse = %d, want 1 — the client id should be remembered", regs2)
	}
}

// A token revoked mid-session must be renewed from the refresh token, without a
// browser, and the rotated refresh token must be on disk before it is relied on.
func TestRefreshOn401(t *testing.T) {
	as := newFakeAS(t)
	as.mu.Lock()
	as.rotateRefresh = true
	as.mu.Unlock()
	m := newFakeMCP(t, as)
	path := filepath.Join(t.TempDir(), "tokens.json")
	store := NewFileStore(path)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	c, err := Start(ctx, "auth", Server{
		Type:        "http",
		URL:         m.srv.URL + "/mcp",
		OAuth:       &OAuth{},
		TokenStore:  store,
		OpenBrowser: browserFor(t),
	})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	defer c.Close()

	resource := canonicalResource(m.srv.URL + "/mcp")
	before, _ := store.Load("", resource)
	if before == nil {
		t.Fatal("no token after login")
	}

	// Pull the rug: the server now rejects the access token it just issued.
	as.revoke(before.AccessToken)
	as.mu.Lock()
	as.registrations = 0 // any new registration means the browser flow ran
	as.mu.Unlock()

	if err := c.Ping(ctx); err != nil {
		t.Fatalf("ping after revocation should have refreshed: %v", err)
	}

	as.mu.Lock()
	grant := as.lastTokenForm.Get("grant_type")
	sentResource := as.lastTokenForm.Get("resource")
	regs := as.registrations
	as.mu.Unlock()
	if grant != "refresh_token" {
		t.Errorf("grant_type = %q, want refresh_token", grant)
	}
	if sentResource != resource {
		t.Errorf("refresh resource = %q, want %q", sentResource, resource)
	}
	if regs != 0 {
		t.Error("a refresh must not re-register the client or reopen a browser")
	}

	after, _ := store.Load("", resource)
	if after == nil {
		t.Fatal("no token after refresh")
	}
	if after.AccessToken == before.AccessToken {
		t.Error("access token was not replaced")
	}
	// The rotated refresh token has to be persisted, not just held in memory: a
	// crash here would leave the stored one already invalidated by the server.
	if after.RefreshToken == before.RefreshToken {
		t.Error("rotated refresh token was not persisted")
	}
	if !as.valid(after.AccessToken) {
		t.Error("the persisted access token is not the one the server issued")
	}
	// And the retry actually reached the server with the new token.
	m.mu.Lock()
	last := m.tokensSeen[len(m.tokensSeen)-1]
	m.mu.Unlock()
	if last != after.AccessToken {
		t.Error("the retried request did not carry the refreshed token")
	}
}

// A refresh token the server no longer honours must fall back to a full login
// rather than failing the session.
func TestReauthorizeWhenRefreshFails(t *testing.T) {
	as := newFakeAS(t)
	m := newFakeMCP(t, as)
	store := NewFileStore(filepath.Join(t.TempDir(), "tokens.json"))
	resource := canonicalResource(m.srv.URL + "/mcp")

	// Seed a token that is dead at both ends.
	if err := store.Save(as.srv.URL, resource, &Token{
		AccessToken:   "stale",
		RefreshToken:  "stale-refresh",
		ClientID:      "client-0",
		TokenEndpoint: as.srv.URL + "/token",
		Issuer:        as.srv.URL,
		Resource:      resource,
	}); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	c, err := Start(ctx, "auth", Server{
		Type:        "http",
		URL:         m.srv.URL + "/mcp",
		OAuth:       &OAuth{},
		TokenStore:  store,
		OpenBrowser: browserFor(t),
	})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	defer c.Close()

	fresh, _ := store.Load("", resource)
	if fresh == nil || fresh.AccessToken == "stale" {
		t.Fatal("the dead token should have been replaced by a full login")
	}
}

// A metadata document that claims a different resource could redirect us to an
// authorization server that mints tokens for someone else. Discard it.
func TestResourceMismatchRejected(t *testing.T) {
	as := newFakeAS(t)
	m := newFakeMCP(t, as)
	m.mu.Lock()
	m.prmResource = "https://evil.example.com/mcp"
	m.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	_, err := Authorize(ctx, m.srv.URL+"/mcp", "", OAuth{},
		NewFileStore(filepath.Join(t.TempDir(), "t.json")),
		func(string) error { t.Error("browser must not open"); return nil })
	if err == nil {
		t.Fatal("a mismatched resource identifier was accepted")
	}
	if !strings.Contains(err.Error(), "declares resource") {
		t.Errorf("error = %v, want a resource mismatch", err)
	}
}

// Same reasoning for the authorization server: the issuer it declares must be
// the one we looked up.
func TestIssuerMismatchRejected(t *testing.T) {
	as := newFakeAS(t)
	as.mu.Lock()
	as.wrongIssuer = "https://evil.example.com"
	as.mu.Unlock()
	m := newFakeMCP(t, as)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	_, err := Authorize(ctx, m.srv.URL+"/mcp", "", OAuth{},
		NewFileStore(filepath.Join(t.TempDir(), "t.json")),
		func(string) error { t.Error("browser must not open"); return nil })
	if err == nil {
		t.Fatal("a mismatched issuer was accepted")
	}
	if !strings.Contains(err.Error(), "declares issuer") {
		t.Errorf("error = %v, want an issuer mismatch", err)
	}
}

// No code_challenge_methods_supported means no PKCE, and a public client
// without PKCE is a code sitting unprotected in the browser's history.
func TestNoPKCERefused(t *testing.T) {
	as := newFakeAS(t)
	as.mu.Lock()
	as.noPKCE = true
	as.mu.Unlock()
	m := newFakeMCP(t, as)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	_, err := Authorize(ctx, m.srv.URL+"/mcp", "", OAuth{},
		NewFileStore(filepath.Join(t.TempDir(), "t.json")),
		func(string) error { t.Error("browser must not open"); return nil })
	if err == nil {
		t.Fatal("an authorization server without PKCE was accepted")
	}
	if !strings.Contains(err.Error(), "PKCE") {
		t.Errorf("error = %v, want a PKCE refusal", err)
	}
}

// A scope named in the challenge is the server telling us what the token lacks,
// so it must win over the configured scopes.
func TestChallengeScopeWins(t *testing.T) {
	as := newFakeAS(t)
	m := newFakeMCP(t, as)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	_, err := Authorize(ctx, m.srv.URL+"/mcp",
		`Bearer error="insufficient_scope", scope="admin:write reports"`,
		OAuth{Scopes: []string{"mcp:read"}},
		NewFileStore(filepath.Join(t.TempDir(), "t.json")),
		browserFor(t))
	if err != nil {
		t.Fatalf("authorize: %v", err)
	}
	as.mu.Lock()
	got := as.lastAuthQuery.Get("scope")
	as.mu.Unlock()
	if got != "admin:write reports" {
		t.Errorf("scope = %q, want the challenge scopes", got)
	}
}

// A pinned issuer skips protected-resource discovery entirely, which is the
// escape hatch for a server that publishes no metadata.
func TestPinnedIssuerSkipsResourceDiscovery(t *testing.T) {
	as := newFakeAS(t)
	m := newFakeMCP(t, as)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	tok, err := Authorize(ctx, m.srv.URL+"/mcp", "",
		OAuth{Issuer: as.srv.URL, Scopes: []string{"mcp:read"}},
		NewFileStore(filepath.Join(t.TempDir(), "t.json")),
		browserFor(t))
	if err != nil {
		t.Fatalf("authorize: %v", err)
	}
	if tok.Issuer != as.srv.URL {
		t.Errorf("issuer = %q, want %q", tok.Issuer, as.srv.URL)
	}
}

// A callback carrying the wrong state is an injected code from somewhere else
// and must be refused.
func TestStateMismatchRejected(t *testing.T) {
	as := newFakeAS(t)
	m := newFakeMCP(t, as)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	_, err := Authorize(ctx, m.srv.URL+"/mcp", "", OAuth{},
		NewFileStore(filepath.Join(t.TempDir(), "t.json")),
		func(u string) error {
			// A browser that comes back with somebody else's state.
			au, _ := url.Parse(u)
			back, _ := url.Parse(au.Query().Get("redirect_uri"))
			q := back.Query()
			q.Set("code", "injected")
			q.Set("state", "not-the-state-we-sent")
			back.RawQuery = q.Encode()
			go func() {
				resp, err := http.Get(back.String())
				if err == nil {
					resp.Body.Close()
				}
			}()
			return nil
		})
	if err == nil || !strings.Contains(err.Error(), "state mismatch") {
		t.Fatalf("error = %v, want a state mismatch", err)
	}
}

// A user who declines at the provider must get a clean error, not a hang.
func TestCallbackErrorSurfaces(t *testing.T) {
	as := newFakeAS(t)
	m := newFakeMCP(t, as)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	_, err := Authorize(ctx, m.srv.URL+"/mcp", "", OAuth{},
		NewFileStore(filepath.Join(t.TempDir(), "t.json")),
		func(u string) error {
			au, _ := url.Parse(u)
			back, _ := url.Parse(au.Query().Get("redirect_uri"))
			q := back.Query()
			q.Set("error", "access_denied")
			q.Set("error_description", "user said no")
			back.RawQuery = q.Encode()
			go func() {
				resp, err := http.Get(back.String())
				if err == nil {
					resp.Body.Close()
				}
			}()
			return nil
		})
	if err == nil || !strings.Contains(err.Error(), "access_denied") {
		t.Fatalf("error = %v, want the provider's denial", err)
	}
}

// A cancelled context must free the listener rather than leave a browser wait
// running for three minutes.
func TestAuthorizeRespectsContext(t *testing.T) {
	as := newFakeAS(t)
	m := newFakeMCP(t, as)
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(200 * time.Millisecond)
		cancel()
	}()
	start := time.Now()
	_, err := Authorize(ctx, m.srv.URL+"/mcp", "", OAuth{},
		NewFileStore(filepath.Join(t.TempDir(), "t.json")),
		func(string) error { return nil }) // a browser that never comes back
	if err == nil {
		t.Fatal("expected a cancellation error")
	}
	if time.Since(start) > 10*time.Second {
		t.Fatal("cancellation did not free the flow")
	}
}

// A browser that cannot be opened must not fail the flow — the URL is printed
// and the login can still be completed by hand.
func TestOpenFailureIsNotFatal(t *testing.T) {
	as := newFakeAS(t)
	m := newFakeMCP(t, as)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	var authURL string
	var mu sync.Mutex
	_, err := Authorize(ctx, m.srv.URL+"/mcp", "", OAuth{},
		NewFileStore(filepath.Join(t.TempDir(), "t.json")),
		func(u string) error {
			mu.Lock()
			authURL = u
			mu.Unlock()
			// Pretend there is no desktop, then complete the login by hand.
			go func() {
				resp, err := http.Get(u)
				if err == nil {
					resp.Body.Close()
				}
			}()
			return fmt.Errorf("no browser here")
		})
	if err != nil {
		t.Fatalf("authorize: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if !strings.Contains(authURL, "code_challenge=") {
		t.Errorf("authorization url is missing its pkce challenge: %q", authURL)
	}
}

// The store holds access and refresh tokens, so it must be no more readable
// than the config it sits beside.
func TestFileStoreRoundTripAndMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "tokens.json")
	s := NewFileStore(path)

	if got, err := s.Load("https://auth", "https://mcp/a"); err != nil || got != nil {
		t.Fatalf("empty store: %v %v", got, err)
	}

	in := &Token{
		AccessToken:   "at",
		RefreshToken:  "rt",
		TokenType:     "Bearer",
		Expiry:        time.Now().Add(time.Hour).Round(time.Second),
		ClientID:      "cid",
		TokenEndpoint: "https://auth/token",
	}
	if err := s.Save("https://auth", "https://mcp/a", in); err != nil {
		t.Fatal(err)
	}
	out, err := s.Load("https://auth", "https://mcp/a")
	if err != nil || out == nil {
		t.Fatalf("load: %v %v", out, err)
	}
	if out.AccessToken != "at" || out.RefreshToken != "rt" || out.ClientID != "cid" ||
		out.TokenEndpoint != "https://auth/token" || !out.Expiry.Equal(in.Expiry) {
		t.Errorf("round trip lost something: %+v", out)
	}
	if out.Issuer != "https://auth" || out.Resource != "https://mcp/a" {
		t.Errorf("issuer/resource not recorded: %+v", out)
	}

	// An empty issuer means "whatever this resource was last authorized with".
	if got, _ := s.Load("", "https://mcp/a"); got == nil {
		t.Error("an empty issuer should match any stored issuer")
	}
	// A different issuer must not hand back a token minted elsewhere.
	if got, _ := s.Load("https://other", "https://mcp/a"); got != nil {
		t.Error("a token from another issuer was returned")
	}
	// A different resource is a different audience.
	if got, _ := s.Load("https://auth", "https://mcp/b"); got != nil {
		t.Error("a token for another resource was returned")
	}

	// Two resources coexist rather than overwriting each other.
	if err := s.Save("https://auth", "https://mcp/b", &Token{AccessToken: "at2"}); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Load("https://auth", "https://mcp/a"); got == nil || got.AccessToken != "at" {
		t.Error("saving a second resource clobbered the first")
	}

	cs := s.(ClientStore)
	if got := cs.LoadClient("https://auth"); got != "" {
		t.Errorf("client id = %q, want empty", got)
	}
	if err := cs.SaveClient("https://auth", "cid-1"); err != nil {
		t.Fatal(err)
	}
	if got := cs.LoadClient("https://auth"); got != "cid-1" {
		t.Errorf("client id = %q, want cid-1", got)
	}
	// The client id must not have cost us the tokens.
	if got, _ := s.Load("https://auth", "https://mcp/a"); got == nil {
		t.Error("saving a client id dropped the tokens")
	}

	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("mode = %o, want 600", fi.Mode().Perm())
	}
	// A leftover temp file at looser permissions would defeat the point.
	if _, err := os.Stat(path + ".tmp"); err == nil {
		t.Error("the temp file was left behind")
	}
}

// A corrupt store must not stop raunen from starting; it just means logging in
// again.
func TestFileStoreSurvivesGarbage(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tokens.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	s := NewFileStore(path)
	if got, err := s.Load("", "https://mcp/a"); err != nil || got != nil {
		t.Fatalf("garbage store: %v %v", got, err)
	}
	if err := s.Save("https://auth", "https://mcp/a", &Token{AccessToken: "at"}); err != nil {
		t.Fatalf("save over garbage: %v", err)
	}
}

func TestTokenExpired(t *testing.T) {
	cases := []struct {
		name string
		tok  *Token
		want bool
	}{
		{"nil", nil, true},
		{"no access token", &Token{}, true},
		{"no expiry means trust it", &Token{AccessToken: "a"}, false},
		{"long lived", &Token{AccessToken: "a", Expiry: time.Now().Add(time.Hour)}, false},
		{"past", &Token{AccessToken: "a", Expiry: time.Now().Add(-time.Minute)}, true},
		{"within the skew", &Token{AccessToken: "a", Expiry: time.Now().Add(5 * time.Second)}, true},
	}
	for _, c := range cases {
		if got := c.tok.expired(); got != c.want {
			t.Errorf("%s: expired = %v, want %v", c.name, got, c.want)
		}
	}
}

// A server with no OAuth block must behave exactly as it did before: no token,
// no Authorization header, and a 401 reported as a plain error.
func TestNoOAuthBlockUnchanged(t *testing.T) {
	var sawAuth bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawAuth = r.Header.Get("Authorization") != ""
		http.Error(w, "nope", 401)
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := Start(ctx, "plain", Server{Type: "http", URL: srv.URL})
	if err == nil {
		t.Fatal("expected the 401 to fail the start")
	}
	if !strings.Contains(err.Error(), "401") {
		t.Errorf("error = %v, want a plain http 401", err)
	}
	if sawAuth {
		t.Error("an Authorization header was sent without an oauth block")
	}
}

// The legacy SSE transport carries the same token on its POSTs and on its
// long-lived GET stream, and retries a 401 the same way.
func TestSSETransportAuthorizes(t *testing.T) {
	as := newFakeAS(t)
	var mu sync.Mutex
	getStreamAuthed := false
	var srv *httptest.Server
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/oauth-protected-resource/sse", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]any{
			"resource":              srv.URL + "/sse",
			"authorization_servers": []string{as.srv.URL},
		})
	})
	mux.HandleFunc("/sse", func(w http.ResponseWriter, r *http.Request) {
		tok := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if r.Method == http.MethodGet {
			mu.Lock()
			getStreamAuthed = tok != "" && as.valid(tok)
			mu.Unlock()
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(200)
			w.(http.Flusher).Flush()
			// Held open: a legacy SSE stream lives for the session, and closing
			// it would take the transport down with it.
			<-r.Context().Done()
			return
		}
		if tok == "" || !as.valid(tok) {
			w.Header().Set("WWW-Authenticate",
				fmt.Sprintf(`Bearer resource_metadata="%s/.well-known/oauth-protected-resource/sse"`, srv.URL))
			http.Error(w, "unauthorized", 401)
			return
		}
		var req struct {
			ID     int    `json:"id"`
			Method string `json:"method"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		switch req.Method {
		case "initialize":
			writeJSON(w, 200, map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": map[string]any{
				"protocolVersion": "2024-11-05",
			}})
		case "tools/list":
			writeJSON(w, 200, map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": map[string]any{
				"tools": []map[string]any{{"name": "echo"}},
			}})
		default:
			w.WriteHeader(202)
		}
	})
	srv = httptest.NewServer(mux)
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	c, err := Start(ctx, "sseauth", Server{
		Type:        "sse",
		URL:         srv.URL + "/sse",
		OAuth:       &OAuth{},
		TokenStore:  NewFileStore(filepath.Join(t.TempDir(), "tokens.json")),
		OpenBrowser: browserFor(t),
	})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	defer c.Close()
	if len(c.Tools()) != 1 {
		t.Fatalf("tools = %d, want 1", len(c.Tools()))
	}
	// The GET stream is opened in the background before the first POST, so give
	// it a moment; the point is that when it does open it is authorized.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		ok := getStreamAuthed
		mu.Unlock()
		if ok {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Error("the GET stream was opened without a bearer token")
}

// An SSE server that turns out not to want a token must not leave the GET
// stream waiting for a login that will never happen.
func TestSSEStreamOpensWhenServerWantsNoToken(t *testing.T) {
	opened := make(chan struct{})
	var once sync.Once
	mux := http.NewServeMux()
	mux.HandleFunc("/sse", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			once.Do(func() { close(opened) })
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(200)
			w.(http.Flusher).Flush()
			<-r.Context().Done()
			return
		}
		var req struct {
			ID     int    `json:"id"`
			Method string `json:"method"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		switch req.Method {
		case "initialize":
			writeJSON(w, 200, map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": map[string]any{"protocolVersion": "2024-11-05"}})
		case "tools/list":
			writeJSON(w, 200, map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": map[string]any{"tools": []map[string]any{}}})
		default:
			w.WriteHeader(202)
		}
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	c, err := Start(ctx, "sseopen", Server{
		Type:        "sse",
		URL:         srv.URL + "/sse",
		OAuth:       &OAuth{},
		TokenStore:  NewFileStore(filepath.Join(t.TempDir(), "tokens.json")),
		OpenBrowser: func(string) error { t.Error("no browser should open"); return nil },
	})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	defer c.Close()
	select {
	case <-opened:
	case <-time.After(5 * time.Second):
		t.Fatal("the GET stream never opened against a server that wants no token")
	}
}

// An expired token must be renewed before the request goes out, rather than
// waiting for the server to reject it.
func TestBearerRefreshesBeforeExpiry(t *testing.T) {
	as := newFakeAS(t)
	store := NewFileStore(filepath.Join(t.TempDir(), "tokens.json"))
	resource := "https://mcp.example.com/mcp"
	as.mu.Lock()
	as.refreshTokens["rt"] = true
	as.mu.Unlock()
	if err := store.Save(as.srv.URL, resource, &Token{
		AccessToken:   "old",
		RefreshToken:  "rt",
		Expiry:        time.Now().Add(-time.Minute),
		ClientID:      "cid",
		TokenEndpoint: as.srv.URL + "/token",
	}); err != nil {
		t.Fatal(err)
	}

	ts := newTokenSource(resource, OAuth{}, store, func(string) error {
		t.Error("an expiring token should refresh, not reopen a browser")
		return nil
	})
	got := ts.bearer(context.Background())
	if got == "" || got == "old" {
		t.Fatalf("bearer = %q, want a refreshed token", got)
	}
	if stored, _ := store.Load("", resource); stored == nil || stored.AccessToken != got {
		t.Error("the refreshed token was not persisted")
	}
}
