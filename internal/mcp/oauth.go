package mcp

// OAuth 2.1 for remote MCP servers.
//
// A remote server that wants a user identity answers an unauthenticated request
// with 401 and a WWW-Authenticate header pointing at its protected-resource
// metadata. From there everything is discoverable: the metadata names the
// authorization server, the authorization server's own metadata names the
// endpoints, and a server that supports dynamic client registration will hand
// out a client_id on the spot. So raunen needs no account, no client secret and
// no per-server setup — the user logs into the MCP server's own service in a
// browser and the resulting token is kept on disk at 0600.
//
// The flow is authorization code + PKCE with a loopback redirect, which is what
// RFC 8252 prescribes for a native app: there is no secret to protect, so the
// code is bound to a one-off verifier instead. The listener is opened on an
// ephemeral port only for the duration of the flow.
//
// Tokens, codes and verifiers are never logged. The only thing printed is the
// authorization URL, and only when a browser could not be opened.

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

// OAuth is the per-server OAuth configuration. Every field is optional: an
// empty block means "discover everything", which is the intended common case.
type OAuth struct {
	// Issuer pins the authorization server, skipping protected-resource
	// discovery. Useful when a server does not publish RFC 9728 metadata.
	Issuer string `json:"issuer,omitempty"`
	// ClientID skips dynamic client registration, for an authorization server
	// where the user pre-registered raunen by hand.
	ClientID string `json:"client_id,omitempty"`
	// Scopes are requested at authorization. A scope named in a 401/403
	// challenge wins over these, since the server is telling us what it wants.
	Scopes []string `json:"scopes,omitempty"`
	// Resource overrides the canonical resource identifier sent as the RFC 8707
	// "resource" parameter. Defaults to the server URL, which is right unless
	// the server publishes a different identifier.
	Resource string `json:"resource,omitempty"`
}

// Token is one access token plus what is needed to renew it without redoing
// discovery. It is persisted, so the fields are the whole state of a login.
type Token struct {
	AccessToken  string    `json:"access_token"`
	TokenType    string    `json:"token_type,omitempty"`
	RefreshToken string    `json:"refresh_token,omitempty"`
	Expiry       time.Time `json:"expiry,omitempty"`
	Scope        string    `json:"scope,omitempty"`

	// Issuer, Resource, ClientID and TokenEndpoint are ours, not the server's:
	// they let a refresh happen straight from a stored token, with no metadata
	// round-trips and no second browser visit.
	Issuer        string `json:"issuer,omitempty"`
	Resource      string `json:"resource,omitempty"`
	ClientID      string `json:"client_id,omitempty"`
	TokenEndpoint string `json:"token_endpoint,omitempty"`
}

// expired reports whether the token is at or near the end of its life. The skew
// buys enough margin that a request does not race its own expiry in flight.
func (t *Token) expired() bool {
	if t == nil || t.AccessToken == "" {
		return true
	}
	if t.Expiry.IsZero() {
		return false // no expiry given: assume valid until the server says 401
	}
	return time.Now().After(t.Expiry.Add(-30 * time.Second))
}

// TokenStore persists tokens between runs so a user logs in once, not once per
// start. Keyed by issuer and resource because one authorization server can
// front several MCP servers, each with its own audience.
type TokenStore interface {
	// Load returns the stored token, or nil when there is none. An empty issuer
	// means "whatever issuer this resource was last authorized against", which
	// is what a caller knows before discovery has run.
	Load(issuer, resource string) (*Token, error)
	Save(issuer, resource string, t *Token) error
}

// ClientStore is the optional other half of a TokenStore: somewhere to remember
// the client_id handed out by dynamic registration. Registering on every run
// would litter the authorization server with dead clients and, on some servers,
// hit a rate limit.
type ClientStore interface {
	LoadClient(issuer string) string
	SaveClient(issuer, clientID string) error
}

// authFlowTimeout bounds how long the loopback listener stays open waiting for
// a browser that may never come back. Never block forever.
const authFlowTimeout = 3 * time.Minute

// Authorize runs the whole flow for one server: discovery, registration if
// needed, the browser round-trip, and the code exchange. challenge is the raw
// WWW-Authenticate header from the 401 that triggered this (may be empty; then
// the well-known locations are probed). open is how the authorization URL
// reaches a browser — nil uses the platform opener.
func Authorize(ctx context.Context, serverURL, challenge string, cfg OAuth, store TokenStore, open func(string) error) (*Token, error) {
	ch := parseChallenge(challenge)
	hc := &http.Client{Timeout: 30 * time.Second}

	resource := canonicalResource(cfg.Resource)
	if resource == "" {
		resource = canonicalResource(serverURL)
	}
	if resource == "" {
		return nil, fmt.Errorf("oauth: %q is not a usable resource url", serverURL)
	}

	issuer := strings.TrimSpace(cfg.Issuer)
	var prmScopes []string
	if issuer == "" {
		prm, id, err := discoverResource(ctx, hc, resource, ch.ResourceMetadata)
		if err != nil {
			return nil, err
		}
		if len(prm.AuthorizationServers) == 0 {
			return nil, fmt.Errorf("oauth: %s names no authorization server", id)
		}
		// The first entry is the server's own preference; raunen does not try to
		// pick between several.
		issuer = prm.AuthorizationServers[0]
		resource = id
		prmScopes = prm.ScopesSupported
	}

	meta, err := discoverAuthServer(ctx, hc, issuer)
	if err != nil {
		return nil, err
	}

	// A challenge that names scopes is a step-up request: the server is saying
	// exactly what the current token lacks, so it outranks anything configured.
	scopes := ch.Scopes
	if len(scopes) == 0 {
		scopes = cfg.Scopes
	}
	if len(scopes) == 0 {
		scopes = prmScopes
	}
	scope := strings.Join(scopes, " ")

	// The listener comes first because the redirect URI carries its port, and
	// the port has to be in the registration request.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("oauth: loopback listener: %w", err)
	}
	defer ln.Close()
	// 127.0.0.1 rather than localhost: RFC 8252 §8.3, since localhost can
	// resolve to an interface another process is listening on.
	redirect := fmt.Sprintf("http://127.0.0.1:%d/callback", ln.Addr().(*net.TCPAddr).Port)

	clientID := strings.TrimSpace(cfg.ClientID)
	if clientID == "" {
		if cs, ok := store.(ClientStore); ok {
			clientID = cs.LoadClient(meta.Issuer)
		}
	}
	if clientID == "" {
		if meta.RegistrationEndpoint == "" {
			return nil, fmt.Errorf("oauth: %s has no client_id configured and offers no dynamic registration", meta.Issuer)
		}
		clientID, err = register(ctx, hc, meta.RegistrationEndpoint, redirect, scope)
		if err != nil {
			return nil, err
		}
		if cs, ok := store.(ClientStore); ok {
			_ = cs.SaveClient(meta.Issuer, clientID)
		}
	}

	verifier, err := newVerifier()
	if err != nil {
		return nil, err
	}
	state, err := randomToken()
	if err != nil {
		return nil, err
	}

	q := url.Values{}
	q.Set("response_type", "code")
	q.Set("client_id", clientID)
	q.Set("code_challenge", codeChallenge(verifier))
	q.Set("code_challenge_method", "S256")
	q.Set("redirect_uri", redirect)
	q.Set("state", state)
	// resource binds the token to this MCP server; without it an authorization
	// server that fronts several resources may issue a token for the wrong one.
	q.Set("resource", resource)
	if scope != "" {
		q.Set("scope", scope)
	}
	authURL := meta.AuthorizationEndpoint
	if strings.Contains(authURL, "?") {
		authURL += "&" + q.Encode()
	} else {
		authURL += "?" + q.Encode()
	}

	if open == nil {
		open = OpenBrowserOrPrint
	}
	// An opener that fails ends the flow rather than starting a wait. Whether a
	// URL printed here can be read at all depends on where the caller is: a
	// terminal that has handed itself over to the alternate screen shows
	// nothing, and the three-minute wait that used to follow was for a browser
	// that had not opened. So the decision belongs to the opener — see
	// OpenBrowserOrPrint, which is what a caller with a visible stderr wants.
	if err := open(authURL); err != nil {
		return nil, fmt.Errorf("could not open a browser to authorize: %w; visit %s", err, authURL)
	}

	code, err := waitForCallback(ctx, ln, state)
	if err != nil {
		return nil, err
	}

	tok, err := exchange(ctx, hc, meta.TokenEndpoint, url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"code_verifier": {verifier},
		"client_id":     {clientID},
		// Byte-identical to what went out in the authorization request, port
		// included, or the server rejects the exchange.
		"redirect_uri": {redirect},
		"resource":     {resource},
	})
	if err != nil {
		return nil, err
	}
	tok.Issuer = meta.Issuer
	tok.Resource = resource
	tok.ClientID = clientID
	tok.TokenEndpoint = meta.TokenEndpoint
	if store != nil {
		if err := store.Save(meta.Issuer, resource, tok); err != nil {
			return nil, fmt.Errorf("oauth: saving token: %w", err)
		}
	}
	return tok, nil
}

// challenge is what a WWW-Authenticate header told us.
type challenge struct {
	// ResourceMetadata is the RFC 9728 metadata URL the server pointed at.
	ResourceMetadata string
	// Scopes are the scopes the server says the request needed.
	Scopes []string
	// Err is the error code, e.g. invalid_token or insufficient_scope.
	Err string
}

// parseChallenge reads one or more WWW-Authenticate headers, joined. The values
// are quoted strings that may themselves contain commas (a scope list, say), so
// the split has to be quote-aware rather than a plain strings.Split. Parameter
// names are case-insensitive per RFC 7235; the scheme token ("Bearer") sits in
// front of the first parameter and is dropped with it.
func parseChallenge(s string) challenge {
	var c challenge
	for _, part := range splitOutsideQuotes(s) {
		k, v, ok := strings.Cut(part, "=")
		if !ok {
			continue
		}
		k = strings.TrimSpace(k)
		// "Bearer realm=..." — the scheme rides along on the first parameter.
		if i := strings.LastIndexAny(k, " \t"); i >= 0 {
			k = k[i+1:]
		}
		v = unquote(strings.TrimSpace(v))
		switch strings.ToLower(k) {
		case "resource_metadata":
			c.ResourceMetadata = v
		case "scope":
			c.Scopes = strings.Fields(v)
		case "error":
			c.Err = v
		}
	}
	return c
}

// splitOutsideQuotes splits on commas that are not inside a quoted string.
func splitOutsideQuotes(s string) []string {
	var out []string
	var b strings.Builder
	inQuote, esc := false, false
	for _, r := range s {
		switch {
		case esc:
			esc = false
			b.WriteRune(r)
		case r == '\\' && inQuote:
			esc = true
			b.WriteRune(r)
		case r == '"':
			inQuote = !inQuote
			b.WriteRune(r)
		case r == ',' && !inQuote:
			out = append(out, strings.TrimSpace(b.String()))
			b.Reset()
		default:
			b.WriteRune(r)
		}
	}
	if strings.TrimSpace(b.String()) != "" {
		out = append(out, strings.TrimSpace(b.String()))
	}
	return out
}

// unquote strips the surrounding quotes of a quoted-string and undoes its
// backslash escapes.
func unquote(s string) string {
	if len(s) < 2 || s[0] != '"' || s[len(s)-1] != '"' {
		return s
	}
	s = s[1 : len(s)-1]
	if !strings.Contains(s, "\\") {
		return s
	}
	var b strings.Builder
	esc := false
	for _, r := range s {
		if esc {
			b.WriteRune(r)
			esc = false
			continue
		}
		if r == '\\' {
			esc = true
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// protectedResource is the RFC 9728 metadata document. Unknown fields are
// ignored, as the spec requires.
type protectedResource struct {
	Resource             string   `json:"resource"`
	AuthorizationServers []string `json:"authorization_servers"`
	ScopesSupported      []string `json:"scopes_supported"`
}

// authServerMeta is the RFC 8414 authorization server metadata document.
type authServerMeta struct {
	Issuer                        string   `json:"issuer"`
	AuthorizationEndpoint         string   `json:"authorization_endpoint"`
	TokenEndpoint                 string   `json:"token_endpoint"`
	RegistrationEndpoint          string   `json:"registration_endpoint"`
	CodeChallengeMethodsSupported []string `json:"code_challenge_methods_supported"`
	GrantTypesSupported           []string `json:"grant_types_supported"`
	ScopesSupported               []string `json:"scopes_supported"`
}

// wellKnown builds a well-known URL for raw by INSERTING the suffix between the
// host and the path — https://host/mcp with "oauth-protected-resource" becomes
// https://host/.well-known/oauth-protected-resource/mcp, not .../mcp/.well-known/...
// Appending is the intuitive reading and the wrong one (RFC 8414 §3.1).
func wellKnown(raw, suffix string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	p := strings.TrimSuffix(u.Path, "/")
	v := *u
	v.Path = "/.well-known/" + suffix + p
	v.RawPath = ""
	v.RawQuery = ""
	v.Fragment = ""
	return v.String()
}

// legacyWellKnown is the pre-RFC-8414 form, where the suffix is appended to the
// issuer path instead of inserted. Still what several OpenID providers serve.
func legacyWellKnown(raw, suffix string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	v := *u
	v.Path = strings.TrimSuffix(u.Path, "/") + "/.well-known/" + suffix
	v.RawPath = ""
	v.RawQuery = ""
	v.Fragment = ""
	return v.String()
}

// resourceMetadataURLs lists where to look for a server's protected-resource
// metadata, in order: the path-scoped location first, then the root one for a
// server that publishes a single document for the whole host.
func resourceMetadataURLs(resource string) [][2]string {
	u, err := url.Parse(resource)
	if err != nil {
		return nil
	}
	out := [][2]string{{wellKnown(resource, "oauth-protected-resource"), resource}}
	root := &url.URL{Scheme: u.Scheme, Host: u.Host}
	if root.String() != resource {
		out = append(out, [2]string{root.String() + "/.well-known/oauth-protected-resource", root.String()})
	}
	return out
}

// authServerMetadataURLs lists the probe order for an issuer's metadata. An
// issuer with a path has three candidates because the two specs disagree about
// where the suffix goes and real deployments serve both.
func authServerMetadataURLs(issuer string) []string {
	u, err := url.Parse(issuer)
	if err != nil {
		return nil
	}
	if strings.TrimSuffix(u.Path, "/") == "" {
		return []string{
			wellKnown(issuer, "oauth-authorization-server"),
			wellKnown(issuer, "openid-configuration"),
		}
	}
	return []string{
		wellKnown(issuer, "oauth-authorization-server"),
		wellKnown(issuer, "openid-configuration"),
		legacyWellKnown(issuer, "openid-configuration"),
	}
}

// discoverResource fetches the protected-resource metadata. hinted, when the
// 401 carried a resource_metadata parameter, is tried first — the server knows
// where its own document lives better than our probe order does.
func discoverResource(ctx context.Context, hc *http.Client, resource, hinted string) (*protectedResource, string, error) {
	candidates := resourceMetadataURLs(resource)
	if hinted != "" {
		candidates = append([][2]string{{hinted, resource}}, candidates...)
	}
	var last, mismatch error
	for _, c := range candidates {
		if c[0] == "" {
			continue
		}
		var prm protectedResource
		if err := getJSON(ctx, hc, c[0], &prm); err != nil {
			last = err
			continue
		}
		// The document must claim the identifier we asked about, byte for byte.
		// Anything else is a document for a different resource, and trusting it
		// would let one server point us at an authorization server that mints
		// tokens for another.
		if prm.Resource != c[1] {
			if mismatch == nil {
				mismatch = fmt.Errorf("oauth: %s declares resource %q, want %q", c[0], prm.Resource, c[1])
			}
			continue
		}
		return &prm, c[1], nil
	}
	// A mismatch outranks a later probe's 404: it names the actual problem,
	// while "not found" only says we ran out of places to look.
	if mismatch != nil {
		return nil, "", mismatch
	}
	if last == nil {
		last = fmt.Errorf("oauth: no protected resource metadata for %s", resource)
	}
	return nil, "", last
}

// discoverAuthServer fetches and validates the authorization server metadata.
func discoverAuthServer(ctx context.Context, hc *http.Client, issuer string) (*authServerMeta, error) {
	var last, mismatch error
	for _, u := range authServerMetadataURLs(issuer) {
		if u == "" {
			continue
		}
		var meta authServerMeta
		if err := getJSON(ctx, hc, u, &meta); err != nil {
			last = err
			continue
		}
		// The issuer in the document must be the issuer we looked up, or a
		// compromised discovery response could hand us someone else's endpoints.
		if meta.Issuer != issuer {
			if mismatch == nil {
				mismatch = fmt.Errorf("oauth: %s declares issuer %q, want %q", u, meta.Issuer, issuer)
			}
			continue
		}
		if meta.AuthorizationEndpoint == "" || meta.TokenEndpoint == "" {
			last = fmt.Errorf("oauth: %s is missing an authorization or token endpoint", u)
			continue
		}
		// No code_challenge_methods_supported means the server does not do PKCE.
		// OAuth 2.1 requires it for a public client, and without it the
		// authorization code is bearer-grade in the browser's history; refuse
		// rather than downgrade.
		if !contains(meta.CodeChallengeMethodsSupported, "S256") {
			return nil, fmt.Errorf("oauth: %s does not support PKCE with S256", issuer)
		}
		if len(meta.GrantTypesSupported) == 0 {
			// RFC 8414 default when the field is omitted.
			meta.GrantTypesSupported = []string{"authorization_code", "implicit"}
		}
		return &meta, nil
	}
	// As with the resource document, a mismatch is the more useful complaint
	// than the 404 from the next place we looked.
	if mismatch != nil {
		return nil, mismatch
	}
	if last == nil {
		last = fmt.Errorf("oauth: no metadata for authorization server %s", issuer)
	}
	return nil, last
}

func contains(xs []string, want string) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
	}
	return false
}

func getJSON(ctx context.Context, hc *http.Client, u string, dst any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("oauth: %s: http %d", u, resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	if err := json.Unmarshal(b, dst); err != nil {
		return fmt.Errorf("oauth: %s: %w", u, err)
	}
	return nil
}

// register performs RFC 7591 dynamic client registration and returns the
// client_id the server issued.
func register(ctx context.Context, hc *http.Client, endpoint, redirect, scope string) (string, error) {
	body := map[string]any{
		"client_name":   "raunen",
		"redirect_uris": []string{redirect},
		// Mandatory for a public client: the default is client_secret_basic,
		// and a server that believes we have a secret will reject the exchange.
		"token_endpoint_auth_method": "none",
		// refresh_token has to be listed explicitly or no refresh token is ever
		// issued, and every expiry becomes another browser visit.
		"grant_types":      []string{"authorization_code", "refresh_token"},
		"response_types":   []string{"code"},
		"application_type": "native",
	}
	if scope != "" {
		body["scope"] = scope
	}
	b, err := json.Marshal(body)
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(string(b)))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := hc.Do(req)
	if err != nil {
		return "", fmt.Errorf("oauth: registering client: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("oauth: registering client: http %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	// Read the values back from the response rather than assuming ours were
	// taken: the server may substitute anything it sent, client_id included.
	var out struct {
		ClientID string `json:"client_id"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", fmt.Errorf("oauth: registering client: %w", err)
	}
	if out.ClientID == "" {
		return "", fmt.Errorf("oauth: registration response had no client_id")
	}
	return out.ClientID, nil
}

// newVerifier returns a PKCE code_verifier: 32 random bytes in unpadded
// base64url, which lands on the 43 characters RFC 7636 asks for.
func newVerifier() (string, error) { return randomToken() }

// codeChallenge is the S256 transformation. The hash is taken over the ASCII of
// the verifier STRING, not the random bytes behind it — hashing the bytes is
// the classic PKCE bug and fails only at the token endpoint.
func codeChallenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func randomToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("oauth: reading random: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// waitForCallback serves the loopback redirect until the browser comes back,
// the context is cancelled, or the flow times out. It returns the code, having
// checked the state matches the one we sent.
func waitForCallback(ctx context.Context, ln net.Listener, state string) (string, error) {
	type result struct {
		code string
		err  error
	}
	res := make(chan result, 1)
	mux := http.NewServeMux()
	mux.HandleFunc("/callback", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if e := q.Get("error"); e != "" {
			msg := e
			if d := q.Get("error_description"); d != "" {
				msg += ": " + d
			}
			page(w, "Authorization failed. You can close this tab and return to the terminal.")
			select {
			case res <- result{err: fmt.Errorf("oauth: authorization denied: %s", msg)}:
			default:
			}
			return
		}
		// Constant-time, because the comparison is against a value an attacker
		// supplies and a timing leak would let them walk it out.
		got := q.Get("state")
		if subtle.ConstantTimeCompare([]byte(got), []byte(state)) != 1 {
			page(w, "Authorization failed. You can close this tab and return to the terminal.")
			select {
			case res <- result{err: fmt.Errorf("oauth: state mismatch on callback")}:
			default:
			}
			return
		}
		code := q.Get("code")
		if code == "" {
			page(w, "Authorization failed. You can close this tab and return to the terminal.")
			select {
			case res <- result{err: fmt.Errorf("oauth: callback carried no code")}:
			default:
			}
			return
		}
		page(w, "Authorized. You can close this tab and return to the terminal.")
		select {
		case res <- result{code: code}:
		default:
		}
	})
	// Anything but the exact redirect path is not our callback.
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	})

	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go srv.Serve(ln)
	defer func() {
		shutdown, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdown)
	}()

	select {
	case r := <-res:
		return r.code, r.err
	case <-ctx.Done():
		return "", ctx.Err()
	case <-time.After(authFlowTimeout):
		return "", fmt.Errorf("oauth: timed out waiting for the browser")
	}
}

func page(w http.ResponseWriter, msg string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintf(w, "<!doctype html><meta charset=utf-8><title>raunen</title>"+
		"<body style=\"font-family:system-ui,sans-serif;padding:3rem\"><p>%s</p></body>", msg)
}

// exchange posts to the token endpoint and decodes the token. No Authorization
// header: raunen is a public client with no secret, and sending one where the
// server expects none is itself a rejection.
func exchange(ctx context.Context, hc *http.Client, endpoint string, form url.Values) (*Token, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("oauth: token request: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		// The body may carry an OAuth error object; report its code, never the
		// request that produced it.
		var e struct {
			Error string `json:"error"`
			Desc  string `json:"error_description"`
		}
		if json.Unmarshal(raw, &e) == nil && e.Error != "" {
			if e.Desc != "" {
				return nil, fmt.Errorf("oauth: token request: %s: %s", e.Error, e.Desc)
			}
			return nil, fmt.Errorf("oauth: token request: %s", e.Error)
		}
		return nil, fmt.Errorf("oauth: token request: http %d", resp.StatusCode)
	}
	var body struct {
		AccessToken  string `json:"access_token"`
		TokenType    string `json:"token_type"`
		ExpiresIn    int64  `json:"expires_in"`
		RefreshToken string `json:"refresh_token"`
		Scope        string `json:"scope"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		return nil, fmt.Errorf("oauth: token response: %w", err)
	}
	if body.AccessToken == "" {
		return nil, fmt.Errorf("oauth: token response had no access_token")
	}
	if body.TokenType != "" && !strings.EqualFold(body.TokenType, "bearer") {
		return nil, fmt.Errorf("oauth: token type %q is not bearer", body.TokenType)
	}
	t := &Token{
		AccessToken:  body.AccessToken,
		TokenType:    "Bearer",
		RefreshToken: body.RefreshToken,
		Scope:        body.Scope,
	}
	if body.ExpiresIn > 0 {
		t.Expiry = time.Now().Add(time.Duration(body.ExpiresIn) * time.Second)
	}
	return t, nil
}

// tokenSource holds the live token for one server and knows how to get a new
// one. Requests can come from more than one goroutine, so it is mutex-guarded,
// and a refresh triggered by two simultaneous 401s runs once.
type tokenSource struct {
	serverURL string
	cfg       OAuth
	store     TokenStore
	open      func(string) error

	mu     sync.Mutex
	tok    *Token
	loaded bool

	// settled is closed once the authentication state is known: either a token
	// exists, or a request has gone through without one. It is for a caller that
	// cannot afford to guess — the legacy SSE GET stream, whose 401 ends the
	// stream and with it the session, with no retry to be had.
	settled     chan struct{}
	settledOnce sync.Once
}

func newTokenSource(serverURL string, cfg OAuth, store TokenStore, open func(string) error) *tokenSource {
	if store == nil {
		store = DefaultTokenStore()
	}
	return &tokenSource{
		serverURL: serverURL,
		cfg:       cfg,
		store:     store,
		open:      open,
		settled:   make(chan struct{}),
	}
}

// settle records that the authentication state is now known, releasing anyone
// waiting on it. Safe to call repeatedly.
func (ts *tokenSource) settle() { ts.settledOnce.Do(func() { close(ts.settled) }) }

// wait blocks until the authentication state is settled, reporting false if the
// context ended first.
func (ts *tokenSource) wait(ctx context.Context) bool {
	if ts.bearer(ctx) != "" {
		return true
	}
	select {
	case <-ts.settled:
		return true
	case <-ctx.Done():
		return false
	}
}

// bearer returns the access token to put on the next request, refreshing a
// token that is about to expire. An empty string means "send no Authorization
// header" — the resulting 401 is what starts the login.
func (ts *tokenSource) bearer(ctx context.Context) string {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	if !ts.loaded {
		ts.loaded = true
		resource := canonicalResource(ts.cfg.Resource)
		if resource == "" {
			resource = canonicalResource(ts.serverURL)
		}
		if t, err := ts.store.Load(ts.cfg.Issuer, resource); err == nil && t != nil {
			ts.tok = t
			ts.settle()
		}
	}
	if ts.tok == nil {
		return ""
	}
	if ts.tok.expired() && ts.tok.RefreshToken != "" {
		if err := ts.refreshLocked(ctx); err != nil {
			return ""
		}
	}
	return ts.tok.AccessToken
}

// reauthorize replaces the token after a 401. stale is the access token that
// got rejected: if it is no longer the current one, another goroutine already
// dealt with the same rejection and this caller can just retry. A refresh is
// tried first, and only if that fails does the user see a browser.
func (ts *tokenSource) reauthorize(ctx context.Context, stale, challengeHdr string) error {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	if ts.tok != nil && stale != "" && ts.tok.AccessToken != stale {
		return nil
	}
	if ts.tok != nil && ts.tok.RefreshToken != "" {
		if err := ts.refreshLocked(ctx); err == nil {
			return nil
		}
	}
	// The challenge is passed through rather than cached, because a server that
	// has moved to a different authorization server announces it there.
	tok, err := Authorize(ctx, ts.serverURL, challengeHdr, ts.cfg, ts.store, ts.open)
	if err != nil {
		return err
	}
	ts.tok = tok
	ts.settle()
	return nil
}

// refreshLocked swaps in a token from the refresh grant. The caller holds mu.
func (ts *tokenSource) refreshLocked(ctx context.Context) error {
	t := ts.tok
	if t == nil || t.RefreshToken == "" || t.TokenEndpoint == "" || t.ClientID == "" {
		return fmt.Errorf("oauth: nothing to refresh with")
	}
	form := url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {t.RefreshToken},
		"client_id":     {t.ClientID},
	}
	if t.Resource != "" {
		form.Set("resource", t.Resource)
	}
	hc := &http.Client{Timeout: 30 * time.Second}
	fresh, err := exchange(ctx, hc, t.TokenEndpoint, form)
	if err != nil {
		return err
	}
	fresh.Issuer = t.Issuer
	fresh.Resource = t.Resource
	fresh.ClientID = t.ClientID
	fresh.TokenEndpoint = t.TokenEndpoint
	if fresh.RefreshToken == "" {
		// A server that does not rotate keeps the old refresh token valid.
		fresh.RefreshToken = t.RefreshToken
	}
	// Persist BEFORE the new token is used. A rotating server invalidates the
	// old refresh token the moment it issues a new one, so a crash between using
	// the new token and writing it down would lock the user out for good.
	if ts.store != nil {
		if err := ts.store.Save(fresh.Issuer, fresh.Resource, fresh); err != nil {
			return fmt.Errorf("oauth: saving refreshed token: %w", err)
		}
	}
	ts.tok = fresh
	ts.settle()
	return nil
}

// unauthorizedError is what a transport returns when the server rejected the
// request for want of a (better) token. It carries the challenge so the retry
// can act on what the server asked for, and the token that was refused so two
// concurrent 401s do not each start a browser.
type unauthorizedError struct {
	status    int
	challenge string
	token     string
	body      string
}

func (e *unauthorizedError) Error() string {
	if e.body != "" {
		return fmt.Sprintf("http %d: %s", e.status, e.body)
	}
	return fmt.Sprintf("http %d: unauthorized", e.status)
}

// authChallenge joins every WWW-Authenticate header on a response. A server may
// send several, one per scheme, and the parser splits on commas outside quotes
// so joining them is safe.
func authChallenge(resp *http.Response) string {
	return strings.Join(resp.Header.Values("Www-Authenticate"), ", ")
}

// needsAuth reports whether a response is an authorization failure worth
// retrying with a new token: a 401, or a 403 that names insufficient_scope,
// which is a step-up request rather than a flat refusal.
func needsAuth(resp *http.Response) bool {
	switch resp.StatusCode {
	case http.StatusUnauthorized:
		return true
	case http.StatusForbidden:
		return parseChallenge(authChallenge(resp)).Err == "insufficient_scope"
	}
	return false
}

// canonicalResource normalises a URL into the resource identifier form RFC 8707
// wants: absolute, lowercase scheme and host, no fragment, no trailing slash.
func canonicalResource(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return ""
	}
	u.Scheme = strings.ToLower(u.Scheme)
	u.Host = strings.ToLower(u.Host)
	u.Fragment = ""
	u.RawFragment = ""
	u.Path = strings.TrimSuffix(u.Path, "/")
	u.RawPath = ""
	return u.String()
}

// OpenBrowserOrPrint opens the URL, and falls back to printing it on stderr so
// a headless or remote session can finish the login by hand. It is the default,
// and the right choice for anything whose stderr a human is reading.
//
// It is deliberately not the right choice for the terminal UI: once the
// alternate screen is up, a line printed here goes onto a screen that is thrown
// away on exit, leaving a login nobody was told to perform. Such a caller passes
// its own opener and handles the failure where it can be seen.
func OpenBrowserOrPrint(u string) error {
	if err := openBrowser(u); err != nil {
		fmt.Fprintf(os.Stderr, "raunen: could not open a browser — visit this url to authorize:\n%s\n", u)
	}
	return nil
}

// openBrowser hands the URL to the desktop.
func openBrowser(u string) error {
	switch runtime.GOOS {
	case "darwin":
		return exec.Command("open", u).Start()
	case "windows":
		return exec.Command("rundll32", "url.dll,FileProtocolHandler", u).Start()
	default:
		return exec.Command("xdg-open", u).Start()
	}
}

// fileStore keeps tokens in ~/.config/raunen/mcp-tokens.json at 0600, next to
// the MCP server definitions and for the same reason: it is a secret, it is
// personal, and it is not the sort of thing to bury in the main config.
type fileStore struct {
	path string
	mu   sync.Mutex
}

// storeFile is the on-disk shape. Clients are keyed by issuer because one
// registration serves every resource that authorization server fronts.
type storeFile struct {
	Tokens  map[string]*Token `json:"tokens,omitempty"`
	Clients map[string]string `json:"clients,omitempty"`
}

// TokenPath is where OAuth tokens are stored. It mirrors config.MCPPath rather
// than importing it: the mcp package has no other reason to depend on config,
// and a transport that needs a token should not need the whole config loaded.
func TokenPath() string {
	if d := os.Getenv("XDG_CONFIG_HOME"); d != "" {
		return filepath.Join(d, "raunen", "mcp-tokens.json")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "raunen-mcp-tokens.json"
	}
	return filepath.Join(home, ".config", "raunen", "mcp-tokens.json")
}

// DefaultTokenStore is the on-disk store at TokenPath.
func DefaultTokenStore() TokenStore { return NewFileStore(TokenPath()) }

// NewFileStore returns a store backed by the given file. The file is read and
// written whole on each call: it holds a handful of entries, and re-reading
// means two raunen instances do not overwrite each other's logins wholesale.
func NewFileStore(path string) TokenStore { return &fileStore{path: path} }

func (f *fileStore) read() *storeFile {
	sf := &storeFile{Tokens: map[string]*Token{}, Clients: map[string]string{}}
	b, err := os.ReadFile(f.path)
	if err != nil {
		return sf
	}
	if err := json.Unmarshal(b, sf); err != nil {
		return &storeFile{Tokens: map[string]*Token{}, Clients: map[string]string{}}
	}
	if sf.Tokens == nil {
		sf.Tokens = map[string]*Token{}
	}
	if sf.Clients == nil {
		sf.Clients = map[string]string{}
	}
	return sf
}

// write persists atomically at 0600, the same way config writes mcp.json: a
// temp file in the same directory, chmod, then rename.
func (f *fileStore) write(sf *storeFile) error {
	if err := os.MkdirAll(filepath.Dir(f.path), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(sf, "", "  ")
	if err != nil {
		return err
	}
	tmp := f.path + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		return err
	}
	if err := os.Chmod(tmp, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, f.path)
}

func (f *fileStore) Load(issuer, resource string) (*Token, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	t := f.read().Tokens[resource]
	if t == nil {
		return nil, nil
	}
	// An empty issuer means the caller has not run discovery yet and wants
	// whatever this resource was last authorized against.
	if issuer != "" && t.Issuer != "" && t.Issuer != issuer {
		return nil, nil
	}
	return t, nil
}

func (f *fileStore) Save(issuer, resource string, t *Token) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	sf := f.read()
	cp := *t
	cp.Issuer = issuer
	cp.Resource = resource
	sf.Tokens[resource] = &cp
	return f.write(sf)
}

func (f *fileStore) LoadClient(issuer string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.read().Clients[issuer]
}

func (f *fileStore) SaveClient(issuer, clientID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	sf := f.read()
	sf.Clients[issuer] = clientID
	return f.write(sf)
}
