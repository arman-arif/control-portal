package api

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"net/http"
	"net/mail"
	"strings"
	"sync"
	"time"

	"github.com/arman-arif/control-portal/panel/store"
	"golang.org/x/crypto/bcrypt"
)

const (
	sessionCookie = "keel_session"
	sessionTTL    = 30 * 24 * time.Hour
	loginWindow   = 15 * time.Minute
)

// dummyHash keeps login timing the same whether or not the email exists.
var dummyHash, _ = bcrypt.GenerateFromPassword([]byte("keel-dummy-password"), bcrypt.DefaultCost)

func newToken() string {
	b := make([]byte, 32)
	rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

func hashToken(tok string) []byte {
	h := sha256.Sum256([]byte(tok))
	return h[:]
}

type ctxKey struct{}

func userFrom(r *http.Request) store.User {
	return r.Context().Value(ctxKey{}).(store.User)
}

func (a *API) requireUser(next http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie(sessionCookie)
		if err != nil || c.Value == "" {
			writeError(w, http.StatusUnauthorized, "not logged in")
			return
		}
		u, err := a.store.SessionUser(r.Context(), hashToken(c.Value))
		if isNotFound(err) {
			writeError(w, http.StatusUnauthorized, "not logged in")
			return
		} else if err != nil {
			internalError(w, err)
			return
		}
		next(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, u)))
	})
}

func (a *API) secureCookies() bool {
	return strings.HasPrefix(a.cfg.PublicURL, "https://")
}

func (a *API) startSession(w http.ResponseWriter, r *http.Request, userID int64) error {
	tok := newToken()
	expires := time.Now().Add(sessionTTL)
	if err := a.store.CreateSession(r.Context(), userID, hashToken(tok), expires); err != nil {
		return err
	}
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: tok, Path: "/", Expires: expires,
		HttpOnly: true, Secure: a.secureCookies(), SameSite: http.SameSiteLaxMode,
	})
	return nil
}

func (a *API) signupOpen(ctx context.Context) (bool, error) {
	if a.cfg.AllowSignup {
		return true, nil
	}
	n, err := a.store.CountUsers(ctx)
	return n == 0, err
}

func (a *API) handleAuthStatus(w http.ResponseWriter, r *http.Request) {
	open, err := a.signupOpen(r.Context())
	if err != nil {
		internalError(w, err)
		return
	}
	n, err := a.store.CountUsers(r.Context())
	if err != nil {
		internalError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"signup_open": open, "needs_setup": n == 0})
}

type credentials struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

func normalizeEmail(s string) (string, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	if len(s) > 254 {
		return "", errors.New("email is too long")
	}
	addr, err := mail.ParseAddress(s)
	if err != nil || addr.Address != s {
		return "", errors.New("enter a valid email address")
	}
	return s, nil
}

func (a *API) handleSignup(w http.ResponseWriter, r *http.Request) {
	var in credentials
	if !readJSON(w, r, &in) {
		return
	}
	open, err := a.signupOpen(r.Context())
	if err != nil {
		internalError(w, err)
		return
	}
	if !open {
		writeError(w, http.StatusForbidden, "sign-up is closed on this panel")
		return
	}
	email, err := normalizeEmail(in.Email)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if len(in.Password) < 8 || len(in.Password) > 72 {
		writeError(w, http.StatusBadRequest, "password must be 8 to 72 characters")
		return
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(in.Password), bcrypt.DefaultCost)
	if err != nil {
		internalError(w, err)
		return
	}
	u, err := a.store.CreateUser(r.Context(), email, string(hash))
	if errors.Is(err, store.ErrEmailTaken) {
		writeError(w, http.StatusConflict, "an account with this email already exists")
		return
	} else if err != nil {
		internalError(w, err)
		return
	}
	if err := a.startSession(w, r, u.ID); err != nil {
		internalError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, u)
}

func (a *API) handleLogin(w http.ResponseWriter, r *http.Request) {
	var in credentials
	if !readJSON(w, r, &in) {
		return
	}
	key := a.clientIP(r)
	if !a.logins.Allow(key) {
		writeError(w, http.StatusTooManyRequests, "too many failed logins; try again in a few minutes")
		return
	}
	email := strings.ToLower(strings.TrimSpace(in.Email))
	u, hash, err := a.store.UserByEmail(r.Context(), email)
	if err != nil && !isNotFound(err) {
		internalError(w, err)
		return
	}
	if isNotFound(err) {
		bcrypt.CompareHashAndPassword(dummyHash, []byte(in.Password))
		a.logins.Fail(key)
		writeError(w, http.StatusUnauthorized, "wrong email or password")
		return
	}
	if bcrypt.CompareHashAndPassword([]byte(hash), []byte(in.Password)) != nil {
		a.logins.Fail(key)
		writeError(w, http.StatusUnauthorized, "wrong email or password")
		return
	}
	a.logins.Reset(key)
	if err := a.startSession(w, r, u.ID); err != nil {
		internalError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, u)
}

func (a *API) handleLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookie); err == nil && c.Value != "" {
		if err := a.store.DeleteSession(r.Context(), hashToken(c.Value)); err != nil {
			internalError(w, err)
			return
		}
	}
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: "", Path: "/", MaxAge: -1,
		HttpOnly: true, Secure: a.secureCookies(), SameSite: http.SameSiteLaxMode,
	})
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) handleMe(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, userFrom(r))
}

// rateLimiter allows a fixed number of failures per key within a window.
type rateLimiter struct {
	mu     sync.Mutex
	max    int
	window time.Duration
	hits   map[string]*rateEntry
}

type rateEntry struct {
	count int
	reset time.Time
}

func newRateLimiter(max int, window time.Duration) *rateLimiter {
	return &rateLimiter{max: max, window: window, hits: map[string]*rateEntry{}}
}

func (l *rateLimiter) Allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	e := l.hits[key]
	if e == nil || time.Now().After(e.reset) {
		return true
	}
	return e.count < l.max
}

func (l *rateLimiter) Fail(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	for k, e := range l.hits { // keep the map from growing without bound
		if now.After(e.reset) {
			delete(l.hits, k)
		}
	}
	e := l.hits[key]
	if e == nil {
		e = &rateEntry{reset: now.Add(l.window)}
		l.hits[key] = e
	}
	e.count++
}

func (l *rateLimiter) Reset(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.hits, key)
}
