package httpapi

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
)

const (
	browserSessionCookie = "werkt_session"
	browserFlowCookie    = "werkt_oidc_flow"
	defaultSessionTTL    = 12 * time.Hour
	flowTTL              = 10 * time.Minute
)

type BrowserAuthConfig struct {
	Issuer        string
	ClientID      string
	ClientSecret  string
	RedirectURL   string
	AllowedEmails []string
	SessionSecret string
	SessionTTL    time.Duration
}

type browserIdentity struct {
	Subject  string `json:"subject"`
	Email    string `json:"email"`
	Name     string `json:"name,omitempty"`
	Username string `json:"username,omitempty"`
}

type browserSession struct {
	Identity browserIdentity `json:"identity"`
	CSRF     string          `json:"csrf"`
	Expires  time.Time       `json:"expires"`
}

type oidcFlow struct {
	State    string    `json:"state"`
	Nonce    string    `json:"nonce"`
	Verifier string    `json:"verifier"`
	Expires  time.Time `json:"expires"`
}

type browserOIDCClient interface {
	AuthorizationURL(state, nonce, verifier string) string
	Exchange(context.Context, string, string, string) (browserIdentity, error)
}

type BrowserAuth struct {
	config  BrowserAuthConfig
	codec   cookieCodec
	allowed map[string]struct{}

	mu      sync.Mutex
	client  browserOIDCClient
	factory func(context.Context) (browserOIDCClient, error)
	now     func() time.Time
}

type cookieCodec struct{ aead cipher.AEAD }

type discoveredOIDCClient struct {
	verifier *oidc.IDTokenVerifier
	oauth    oauth2.Config
	provider *oidc.Provider
	allowed  map[string]struct{}
}

func NewBrowserAuth(configuration BrowserAuthConfig) (*BrowserAuth, error) {
	configuration.Issuer = strings.TrimRight(strings.TrimSpace(configuration.Issuer), "/")
	configuration.ClientID = strings.TrimSpace(configuration.ClientID)
	configuration.RedirectURL = strings.TrimSpace(configuration.RedirectURL)
	if configuration.SessionTTL <= 0 {
		configuration.SessionTTL = defaultSessionTTL
	}
	issuer, err := url.Parse(configuration.Issuer)
	if err != nil || issuer.Scheme != "https" || issuer.Host == "" {
		return nil, errors.New("OIDC issuer must be an absolute HTTPS URL")
	}
	redirect, err := url.Parse(configuration.RedirectURL)
	if err != nil || redirect.Scheme != "https" || redirect.Host == "" {
		return nil, errors.New("OIDC redirect URL must be an absolute HTTPS URL")
	}
	if configuration.ClientID == "" || configuration.ClientSecret == "" {
		return nil, errors.New("OIDC client ID and secret are required")
	}
	if len(configuration.SessionSecret) < 32 {
		return nil, errors.New("OIDC session secret must be at least 32 bytes")
	}
	allowed := make(map[string]struct{}, len(configuration.AllowedEmails))
	for _, email := range configuration.AllowedEmails {
		email = strings.ToLower(strings.TrimSpace(email))
		if email != "" {
			allowed[email] = struct{}{}
		}
	}
	if len(allowed) == 0 {
		return nil, errors.New("at least one OIDC allowed email is required")
	}
	codec, err := newCookieCodec(configuration.SessionSecret)
	if err != nil {
		return nil, err
	}
	auth := &BrowserAuth{config: configuration, codec: codec, allowed: allowed, now: time.Now}
	auth.factory = auth.discover
	return auth, nil
}

func newCookieCodec(secret string) (cookieCodec, error) {
	key := sha256.Sum256([]byte(secret))
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return cookieCodec{}, fmt.Errorf("create session cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return cookieCodec{}, fmt.Errorf("create session AEAD: %w", err)
	}
	return cookieCodec{aead: aead}, nil
}

func (c cookieCodec) encode(value any) (string, error) {
	plaintext, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, c.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	sealed := c.aead.Seal(nonce, nonce, plaintext, nil)
	return base64.RawURLEncoding.EncodeToString(sealed), nil
}

func (c cookieCodec) decode(encoded string, destination any) error {
	sealed, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil || len(sealed) < c.aead.NonceSize() {
		return errors.New("invalid cookie")
	}
	nonce, ciphertext := sealed[:c.aead.NonceSize()], sealed[c.aead.NonceSize():]
	plaintext, err := c.aead.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return errors.New("invalid cookie")
	}
	if err := json.Unmarshal(plaintext, destination); err != nil {
		return errors.New("invalid cookie")
	}
	return nil
}

func (a *BrowserAuth) discover(ctx context.Context) (browserOIDCClient, error) {
	provider, err := oidc.NewProvider(ctx, a.config.Issuer)
	if err != nil {
		return nil, fmt.Errorf("discover OIDC provider: %w", err)
	}
	endpoint := provider.Endpoint()
	endpoint.AuthStyle = oauth2.AuthStyleInParams
	return &discoveredOIDCClient{
		verifier: provider.Verifier(&oidc.Config{ClientID: a.config.ClientID}),
		oauth: oauth2.Config{
			ClientID:     a.config.ClientID,
			ClientSecret: a.config.ClientSecret,
			Endpoint:     endpoint,
			RedirectURL:  a.config.RedirectURL,
			Scopes:       []string{oidc.ScopeOpenID, "profile", "email"},
		},
		provider: provider,
		allowed:  a.allowed,
	}, nil
}

func (a *BrowserAuth) oidcClient(ctx context.Context) (browserOIDCClient, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.client != nil {
		return a.client, nil
	}
	client, err := a.factory(ctx)
	if err != nil {
		return nil, err
	}
	a.client = client
	return client, nil
}

func (c *discoveredOIDCClient) AuthorizationURL(state, nonce, verifier string) string {
	return c.oauth.AuthCodeURL(state, oidc.Nonce(nonce), oauth2.S256ChallengeOption(verifier))
}

func (c *discoveredOIDCClient) Exchange(ctx context.Context, code, verifier, nonce string) (browserIdentity, error) {
	token, err := c.oauth.Exchange(ctx, code, oauth2.VerifierOption(verifier))
	if err != nil {
		return browserIdentity{}, fmt.Errorf("exchange authorization code: %w", err)
	}
	rawIDToken, ok := token.Extra("id_token").(string)
	if !ok || rawIDToken == "" {
		return browserIdentity{}, errors.New("OIDC response omitted ID token")
	}
	idToken, err := c.verifier.Verify(ctx, rawIDToken)
	if err != nil {
		return browserIdentity{}, fmt.Errorf("verify ID token: %w", err)
	}
	if subtle.ConstantTimeCompare([]byte(idToken.Nonce), []byte(nonce)) != 1 {
		return browserIdentity{}, errors.New("OIDC nonce mismatch")
	}
	if idToken.AccessTokenHash != "" {
		if err := idToken.VerifyAccessToken(token.AccessToken); err != nil {
			return browserIdentity{}, fmt.Errorf("verify access token: %w", err)
		}
	}
	userinfo, err := c.provider.UserInfo(ctx, oauth2.StaticTokenSource(token))
	if err != nil {
		return browserIdentity{}, fmt.Errorf("fetch OIDC user info: %w", err)
	}
	if subtle.ConstantTimeCompare([]byte(userinfo.Subject), []byte(idToken.Subject)) != 1 {
		return browserIdentity{}, errors.New("OIDC subject mismatch")
	}
	var claims struct {
		Email             string `json:"email"`
		Name              string `json:"name"`
		PreferredUsername string `json:"preferred_username"`
	}
	if err := userinfo.Claims(&claims); err != nil {
		return browserIdentity{}, fmt.Errorf("decode OIDC user info: %w", err)
	}
	email := strings.ToLower(strings.TrimSpace(claims.Email))
	if _, ok := c.allowed[email]; !ok {
		return browserIdentity{}, errors.New("OIDC account is not allowed")
	}
	return browserIdentity{Subject: idToken.Subject, Email: email, Name: strings.TrimSpace(claims.Name), Username: strings.TrimSpace(claims.PreferredUsername)}, nil
}

func randomToken(bytes int) (string, error) {
	value := make([]byte, bytes)
	if _, err := io.ReadFull(rand.Reader, value); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
}

func (a *BrowserAuth) setCookie(response http.ResponseWriter, name, value string, expires time.Time) {
	http.SetCookie(response, &http.Cookie{
		Name: name, Value: value, Path: "/", Expires: expires, MaxAge: int(expires.Sub(a.now()).Seconds()),
		HttpOnly: true, Secure: true, SameSite: http.SameSiteLaxMode,
	})
}

func clearBrowserCookie(response http.ResponseWriter, name string) {
	http.SetCookie(response, &http.Cookie{
		Name: name, Value: "", Path: "/", MaxAge: -1, Expires: time.Unix(1, 0),
		HttpOnly: true, Secure: true, SameSite: http.SameSiteLaxMode,
	})
}

func (a *BrowserAuth) readSession(request *http.Request) (browserSession, error) {
	cookie, err := request.Cookie(browserSessionCookie)
	if err != nil {
		return browserSession{}, err
	}
	var session browserSession
	if err := a.codec.decode(cookie.Value, &session); err != nil || session.Identity.Subject == "" || session.CSRF == "" || !session.Expires.After(a.now()) {
		return browserSession{}, errors.New("invalid or expired browser session")
	}
	return session, nil
}

func (s *Server) browserSession(response http.ResponseWriter, request *http.Request) {
	response.Header().Set("Cache-Control", "no-store")
	if s.browserAuth == nil {
		writeJSON(response, http.StatusOK, map[string]any{"configured": false, "authenticated": false})
		return
	}
	session, err := s.browserAuth.readSession(request)
	if err != nil {
		writeJSON(response, http.StatusOK, map[string]any{"configured": true, "authenticated": false})
		return
	}
	writeJSON(response, http.StatusOK, map[string]any{
		"configured":    true,
		"authenticated": true,
		"identity": map[string]string{
			"email":    session.Identity.Email,
			"name":     session.Identity.Name,
			"username": session.Identity.Username,
		},
		"csrfToken": session.CSRF,
		"expiresAt": session.Expires,
	})
}

func (s *Server) browserLogin(response http.ResponseWriter, request *http.Request) {
	response.Header().Set("Cache-Control", "no-store")
	if s.browserAuth == nil {
		writeError(response, http.StatusNotFound, "browser login is not configured")
		return
	}
	state, err := randomToken(32)
	if err != nil {
		writeError(response, http.StatusInternalServerError, "start browser login")
		return
	}
	nonce, err := randomToken(32)
	if err != nil {
		writeError(response, http.StatusInternalServerError, "start browser login")
		return
	}
	verifier := oauth2.GenerateVerifier()
	expires := s.browserAuth.now().Add(flowTTL)
	encoded, err := s.browserAuth.codec.encode(oidcFlow{State: state, Nonce: nonce, Verifier: verifier, Expires: expires})
	if err != nil {
		writeError(response, http.StatusInternalServerError, "start browser login")
		return
	}
	ctx, cancel := context.WithTimeout(request.Context(), 10*time.Second)
	defer cancel()
	client, err := s.browserAuth.oidcClient(ctx)
	if err != nil {
		redirectBrowserAuthError(response, request, "provider_unavailable")
		return
	}
	s.browserAuth.setCookie(response, browserFlowCookie, encoded, expires)
	http.Redirect(response, request, client.AuthorizationURL(state, nonce, verifier), http.StatusFound)
}

func (s *Server) browserCallback(response http.ResponseWriter, request *http.Request) {
	response.Header().Set("Cache-Control", "no-store")
	if s.browserAuth == nil {
		redirectBrowserAuthError(response, request, "not_configured")
		return
	}
	clearBrowserCookie(response, browserFlowCookie)
	if request.URL.Query().Get("error") != "" {
		redirectBrowserAuthError(response, request, "cancelled")
		return
	}
	cookie, err := request.Cookie(browserFlowCookie)
	if err != nil {
		redirectBrowserAuthError(response, request, "expired")
		return
	}
	var flow oidcFlow
	if err := s.browserAuth.codec.decode(cookie.Value, &flow); err != nil || !flow.Expires.After(s.browserAuth.now()) || !constantTimeEqual(request.URL.Query().Get("state"), flow.State) {
		redirectBrowserAuthError(response, request, "invalid_flow")
		return
	}
	code := request.URL.Query().Get("code")
	if code == "" {
		redirectBrowserAuthError(response, request, "invalid_flow")
		return
	}
	ctx, cancel := context.WithTimeout(request.Context(), 15*time.Second)
	defer cancel()
	client, err := s.browserAuth.oidcClient(ctx)
	if err != nil {
		redirectBrowserAuthError(response, request, "provider_unavailable")
		return
	}
	identity, err := client.Exchange(ctx, code, flow.Verifier, flow.Nonce)
	if err != nil {
		redirectBrowserAuthError(response, request, "not_authorized")
		return
	}
	csrf, err := randomToken(32)
	if err != nil {
		redirectBrowserAuthError(response, request, "session_failed")
		return
	}
	expires := s.browserAuth.now().Add(s.browserAuth.config.SessionTTL)
	encoded, err := s.browserAuth.codec.encode(browserSession{Identity: identity, CSRF: csrf, Expires: expires})
	if err != nil {
		redirectBrowserAuthError(response, request, "session_failed")
		return
	}
	s.browserAuth.setCookie(response, browserSessionCookie, encoded, expires)
	http.Redirect(response, request, "/app/", http.StatusFound)
}

func (s *Server) browserLogout(response http.ResponseWriter, request *http.Request) {
	response.Header().Set("Cache-Control", "no-store")
	if s.browserAuth == nil {
		writeError(response, http.StatusNotFound, "browser login is not configured")
		return
	}
	session, err := s.browserAuth.readSession(request)
	if err != nil {
		clearBrowserCookie(response, browserSessionCookie)
		writeJSON(response, http.StatusOK, map[string]bool{"authenticated": false})
		return
	}
	if !constantTimeEqual(request.Header.Get("X-Werkt-CSRF"), session.CSRF) {
		writeError(response, http.StatusForbidden, "CSRF validation failed")
		return
	}
	clearBrowserCookie(response, browserSessionCookie)
	writeJSON(response, http.StatusOK, map[string]bool{"authenticated": false})
}

func redirectBrowserAuthError(response http.ResponseWriter, request *http.Request, code string) {
	destination := "/app/?auth_error=" + url.QueryEscape(code)
	http.Redirect(response, request, destination, http.StatusFound)
}
