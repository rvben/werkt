package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

type fakeBrowserOIDCClient struct {
	identity browserIdentity
	code     string
	verifier string
	nonce    string
}

func (f *fakeBrowserOIDCClient) AuthorizationURL(state, nonce, verifier string) string {
	query := url.Values{"state": {state}, "nonce": {nonce}, "challenge": {verifier}}
	return "https://auth.example.test/authorize?" + query.Encode()
}

func (f *fakeBrowserOIDCClient) Exchange(_ context.Context, code, verifier, nonce string) (browserIdentity, error) {
	f.code, f.verifier, f.nonce = code, verifier, nonce
	return f.identity, nil
}

func testBrowserAuth(t *testing.T, client browserOIDCClient) *BrowserAuth {
	t.Helper()
	auth, err := NewBrowserAuth(BrowserAuthConfig{
		Issuer: "https://auth.example.test", ClientID: "werkt", ClientSecret: "client-secret",
		RedirectURL:   "https://werkt.example.test/api/v1/auth/callback",
		AllowedEmails: []string{"operator@example.test"},
		SessionSecret: "test-session-secret-that-is-at-least-thirty-two-bytes",
		SessionTTL:    time.Hour,
	})
	if err != nil {
		t.Fatalf("new browser auth: %v", err)
	}
	auth.factory = func(context.Context) (browserOIDCClient, error) { return client, nil }
	auth.now = func() time.Time { return time.Unix(1_800_000_000, 0).UTC() }
	return auth
}

func cookieNamed(t *testing.T, response *httptest.ResponseRecorder, name string) *http.Cookie {
	t.Helper()
	for _, cookie := range response.Result().Cookies() {
		if cookie.Name == name && cookie.Value != "" {
			return cookie
		}
	}
	t.Fatalf("response omitted %s cookie", name)
	return nil
}

func establishBrowserSession(t *testing.T, server *Server) (*http.Cookie, string) {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, "/api/v1/auth/login", nil)
	response := httptest.NewRecorder()
	server.server.Handler.ServeHTTP(response, request)
	if response.Code != http.StatusFound {
		t.Fatalf("login status = %d, body = %s", response.Code, response.Body.String())
	}
	flowCookie := cookieNamed(t, response, browserFlowCookie)
	authorizationURL, err := url.Parse(response.Header().Get("Location"))
	if err != nil {
		t.Fatalf("parse authorization URL: %v", err)
	}
	state := authorizationURL.Query().Get("state")
	if state == "" || authorizationURL.Query().Get("nonce") == "" || authorizationURL.Query().Get("challenge") == "" {
		t.Fatalf("authorization URL omitted security parameters: %s", authorizationURL)
	}

	request = httptest.NewRequest(http.MethodGet, "/api/v1/auth/callback?code=test-code&state="+url.QueryEscape(state), nil)
	request.AddCookie(flowCookie)
	response = httptest.NewRecorder()
	server.server.Handler.ServeHTTP(response, request)
	if response.Code != http.StatusFound || response.Header().Get("Location") != "/app/" {
		t.Fatalf("callback status = %d, location = %q", response.Code, response.Header().Get("Location"))
	}
	sessionCookie := cookieNamed(t, response, browserSessionCookie)

	request = httptest.NewRequest(http.MethodGet, "/api/v1/auth/session", nil)
	request.AddCookie(sessionCookie)
	response = httptest.NewRecorder()
	server.server.Handler.ServeHTTP(response, request)
	var session struct {
		Authenticated bool   `json:"authenticated"`
		CSRF          string `json:"csrfToken"`
	}
	if err := json.NewDecoder(response.Body).Decode(&session); err != nil {
		t.Fatalf("decode session response: %v", err)
	}
	if !session.Authenticated || session.CSRF == "" {
		t.Fatalf("session response = %#v", session)
	}
	return sessionCookie, session.CSRF
}

func TestBrowserOIDCSessionAuthenticatesManagementRequestsAndProtectsMutations(t *testing.T) {
	identity := browserIdentity{Subject: "authelia|operator", Email: "operator@example.test", Name: "Operator", Username: "operator"}
	client := &fakeBrowserOIDCClient{identity: identity}
	auth := testBrowserAuth(t, client)
	store := &fakeStore{}
	server := New(store, ":0", "management-secret", WithBrowserAuth(auth))
	sessionCookie, csrf := establishBrowserSession(t, server)

	request := httptest.NewRequest(http.MethodGet, "/api/v1/automations", nil)
	request.AddCookie(sessionCookie)
	response := httptest.NewRecorder()
	server.server.Handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !store.listCalled {
		t.Fatalf("session GET status = %d, called = %t", response.Code, store.listCalled)
	}

	request = httptest.NewRequest(http.MethodPatch, "/api/v1/automations/example", strings.NewReader(`{"enabled":false}`))
	request.Header.Set("Content-Type", "application/json")
	request.AddCookie(sessionCookie)
	response = httptest.NewRecorder()
	server.server.Handler.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden || store.setEnabled != nil {
		t.Fatalf("mutation without CSRF status = %d, reached store = %t", response.Code, store.setEnabled != nil)
	}

	request = httptest.NewRequest(http.MethodPatch, "/api/v1/automations/example", strings.NewReader(`{"enabled":false}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Werkt-CSRF", csrf)
	request.Header.Set("X-Werkt-Actor", "spoofed")
	request.AddCookie(sessionCookie)
	response = httptest.NewRecorder()
	server.server.Handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || store.setEnabled == nil {
		t.Fatalf("mutation with CSRF status = %d, body = %s", response.Code, response.Body.String())
	}
	digest := sha256.Sum256([]byte(identity.Subject))
	wantActor := "workspace:oidc:" + hex.EncodeToString(digest[:8])
	if store.setActor != wantActor {
		t.Fatalf("actor = %q, want %q", store.setActor, wantActor)
	}
	if client.code != "test-code" || client.verifier == "" || client.nonce == "" {
		t.Fatalf("exchange parameters were not preserved: %#v", client)
	}
}

func TestBrowserOIDCCallbackRejectsMismatchedState(t *testing.T) {
	auth := testBrowserAuth(t, &fakeBrowserOIDCClient{})
	server := New(&fakeStore{}, ":0", "management-secret", WithBrowserAuth(auth))

	request := httptest.NewRequest(http.MethodGet, "/api/v1/auth/login", nil)
	response := httptest.NewRecorder()
	server.server.Handler.ServeHTTP(response, request)
	flowCookie := cookieNamed(t, response, browserFlowCookie)

	request = httptest.NewRequest(http.MethodGet, "/api/v1/auth/callback?code=test&state=wrong", nil)
	request.AddCookie(flowCookie)
	response = httptest.NewRecorder()
	server.server.Handler.ServeHTTP(response, request)
	if response.Code != http.StatusFound || response.Header().Get("Location") != "/app/?auth_error=invalid_flow" {
		t.Fatalf("callback status = %d, location = %q", response.Code, response.Header().Get("Location"))
	}
	for _, cookie := range response.Result().Cookies() {
		if cookie.Name == browserSessionCookie && cookie.Value != "" {
			t.Fatal("mismatched state created a browser session")
		}
	}
}

func TestBrowserOIDCLogoutRequiresCSRFAndClearsSession(t *testing.T) {
	auth := testBrowserAuth(t, &fakeBrowserOIDCClient{identity: browserIdentity{Subject: "operator", Email: "operator@example.test"}})
	server := New(&fakeStore{}, ":0", "management-secret", WithBrowserAuth(auth))
	sessionCookie, csrf := establishBrowserSession(t, server)

	request := httptest.NewRequest(http.MethodPost, "/api/v1/auth/logout", nil)
	request.AddCookie(sessionCookie)
	response := httptest.NewRecorder()
	server.server.Handler.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("logout without CSRF status = %d", response.Code)
	}

	request = httptest.NewRequest(http.MethodPost, "/api/v1/auth/logout", nil)
	request.AddCookie(sessionCookie)
	request.Header.Set("X-Werkt-CSRF", csrf)
	response = httptest.NewRecorder()
	server.server.Handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("logout status = %d", response.Code)
	}
	cleared := false
	for _, cookie := range response.Result().Cookies() {
		cleared = cleared || cookie.Name == browserSessionCookie && cookie.MaxAge < 0
	}
	if !cleared {
		t.Fatal("logout did not clear the browser session cookie")
	}
}

func TestBrowserOIDCConfigurationRequiresSecureCompleteValues(t *testing.T) {
	tests := []BrowserAuthConfig{
		{},
		{Issuer: "http://auth.example.test", RedirectURL: "https://werkt.example.test/callback", ClientID: "werkt", ClientSecret: "secret", AllowedEmails: []string{"operator@example.test"}, SessionSecret: strings.Repeat("x", 32)},
		{Issuer: "https://auth.example.test", RedirectURL: "http://werkt.example.test/callback", ClientID: "werkt", ClientSecret: "secret", AllowedEmails: []string{"operator@example.test"}, SessionSecret: strings.Repeat("x", 32)},
		{Issuer: "https://auth.example.test", RedirectURL: "https://werkt.example.test/callback", ClientID: "werkt", ClientSecret: "secret", SessionSecret: strings.Repeat("x", 32)},
	}
	for index, configuration := range tests {
		if _, err := NewBrowserAuth(configuration); err == nil {
			t.Errorf("configuration %d unexpectedly succeeded", index)
		}
	}
}
