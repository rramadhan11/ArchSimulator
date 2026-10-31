package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"html/template"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"
)

const (
	sessionCookie = "archsim_session"
	maxLoginBody  = 4 << 10
)

// auth is single-user authentication: one username and bcrypt hash from the
// config, and a stateless session cookie signed with HMAC-SHA256. Rotating
// the session secret signs everyone out.
type auth struct {
	username     string
	passwordHash []byte
	secret       []byte
	ttl          time.Duration
	secureCookie bool
	trustProxy   bool
	limiter      *loginLimiter
	now          func() time.Time
}

// issue returns a cookie value "<payload>.<mac>" with payload "<user>|<expiry>".
func (a *auth) issue() (string, time.Time) {
	exp := a.now().Add(a.ttl)
	payload := a.username + "|" + strconv.FormatInt(exp.Unix(), 10)
	enc := base64.RawURLEncoding.EncodeToString([]byte(payload))
	return enc + "." + a.sign(enc), exp
}

func (a *auth) sign(s string) string {
	m := hmac.New(sha256.New, a.secret)
	m.Write([]byte(s))
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}

func (a *auth) valid(value string) bool {
	enc, mac, ok := strings.Cut(value, ".")
	if !ok || !hmac.Equal([]byte(mac), []byte(a.sign(enc))) {
		return false
	}
	raw, err := base64.RawURLEncoding.DecodeString(enc)
	if err != nil {
		return false
	}
	user, expStr, ok := strings.Cut(string(raw), "|")
	exp, err := strconv.ParseInt(expStr, 10, 64)
	return ok && err == nil && user == a.username && a.now().Unix() < exp
}

func (a *auth) authenticated(r *http.Request) bool {
	c, err := r.Cookie(sessionCookie)
	return err == nil && a.valid(c.Value)
}

func (a *auth) checkCredentials(user, password string) bool {
	userOK := subtle.ConstantTimeCompare([]byte(user), []byte(a.username)) == 1
	// Always run bcrypt so a wrong username costs the same time as a wrong password.
	passOK := bcrypt.CompareHashAndPassword(a.passwordHash, []byte(password)) == nil
	return userOK && passOK
}

// require guards every route except the public ones.
func (a *auth) require(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if a.authenticated(r) {
			next.ServeHTTP(w, r)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/api/") {
			http.Error(w, `{"error":"unauthenticated"}`, http.StatusUnauthorized)
			return
		}
		http.Redirect(w, r, "/login?next="+url.QueryEscape(r.URL.RequestURI()), http.StatusSeeOther)
	})
}

func (a *auth) handleLogin(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		if a.authenticated(r) {
			http.Redirect(w, r, safeNext(r.URL.Query().Get("next")), http.StatusSeeOther)
			return
		}
		renderLogin(w, http.StatusOK, r.URL.Query().Get("next"), "")
	case http.MethodPost:
		a.login(w, r)
	default:
		w.Header().Set("Allow", "GET, POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (a *auth) login(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) {
		http.Error(w, "cross-origin request rejected", http.StatusForbidden)
		return
	}
	ip := a.clientIP(r)
	if wait := a.limiter.blocked(ip); wait > 0 {
		w.Header().Set("Retry-After", strconv.Itoa(int(wait.Seconds())+1))
		renderLogin(w, http.StatusTooManyRequests, "", "Too many failed attempts. Try again later.")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxLoginBody)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	next := r.PostForm.Get("next")
	if !a.checkCredentials(r.PostForm.Get("username"), r.PostForm.Get("password")) {
		a.limiter.fail(ip)
		slog.Warn("login failed", "ip", ip)
		renderLogin(w, http.StatusUnauthorized, next, "Invalid username or password.")
		return
	}
	a.limiter.reset(ip)
	value, exp := a.issue()
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: value, Path: "/", Expires: exp,
		HttpOnly: true, Secure: a.secureCookie, SameSite: http.SameSiteLaxMode,
	})
	slog.Info("login succeeded", "ip", ip)
	http.Redirect(w, r, safeNext(next), http.StatusSeeOther)
}

func (a *auth) handleLogout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !sameOrigin(r) {
		http.Error(w, "cross-origin request rejected", http.StatusForbidden)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: "", Path: "/", MaxAge: -1,
		HttpOnly: true, Secure: a.secureCookie, SameSite: http.SameSiteLaxMode,
	})
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

// sameOrigin rejects state-changing requests sent by other sites. Browsers
// always send Origin on cross-origin POSTs; a missing header means a
// same-origin navigation or a non-browser client.
func sameOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	u, err := url.Parse(origin)
	return err == nil && u.Host == r.Host
}

// safeNext only allows local absolute paths, preventing open redirects.
func safeNext(next string) string {
	if !strings.HasPrefix(next, "/") || strings.HasPrefix(next, "//") || strings.HasPrefix(next, "/\\") {
		return "/"
	}
	return next
}

// clientIP uses the last X-Forwarded-For entry when behind a trusted reverse
// proxy (the one the proxy itself appended); otherwise the TCP peer.
func (a *auth) clientIP(r *http.Request) string {
	if a.trustProxy {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			parts := strings.Split(xff, ",")
			return strings.TrimSpace(parts[len(parts)-1])
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// loginLimiter blocks an IP after maxFailures failed logins within window.
type loginLimiter struct {
	mu          sync.Mutex
	maxFailures int
	window      time.Duration
	entries     map[string]*attempts
	now         func() time.Time
}

type attempts struct {
	count int
	start time.Time
}

func newLoginLimiter(maxFailures int, window time.Duration) *loginLimiter {
	return &loginLimiter{maxFailures: maxFailures, window: window, entries: map[string]*attempts{}, now: time.Now}
}

func (l *loginLimiter) blocked(ip string) time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	e, ok := l.entries[ip]
	if !ok || e.count < l.maxFailures {
		return 0
	}
	if left := e.start.Add(l.window).Sub(l.now()); left > 0 {
		return left
	}
	delete(l.entries, ip)
	return 0
}

func (l *loginLimiter) fail(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	e, ok := l.entries[ip]
	if !ok || now.Sub(e.start) > l.window {
		e = &attempts{start: now}
		l.entries[ip] = e
		l.pruneLocked(now)
	}
	e.count++
}

func (l *loginLimiter) reset(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.entries, ip)
}

// pruneLocked drops expired entries so the map cannot grow without bound.
func (l *loginLimiter) pruneLocked(now time.Time) {
	for ip, e := range l.entries {
		if now.Sub(e.start) > l.window {
			delete(l.entries, ip)
		}
	}
}

var loginTmpl = template.Must(template.New("login").Parse(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>ArchSim · Sign in</title>
<style>
  :root { color-scheme: light dark; --bg:#f6f7f9; --card:#fff; --fg:#111827; --muted:#6b7280; --line:#d1d5db; --accent:#2563eb; --err:#b91c1c; }
  @media (prefers-color-scheme: dark) { :root { --bg:#0b0f17; --card:#111827; --fg:#e5e7eb; --muted:#9ca3af; --line:#374151; --accent:#60a5fa; --err:#f87171; } }
  * { box-sizing: border-box; }
  body { margin:0; min-height:100vh; display:grid; place-items:center; background:var(--bg); color:var(--fg); font:15px/1.5 system-ui, -apple-system, "Segoe UI", sans-serif; padding:16px; }
  form { width:100%; max-width:340px; background:var(--card); border:1px solid var(--line); border-radius:12px; padding:28px; }
  h1 { margin:0 0 4px; font-size:20px; }
  p.sub { margin:0 0 20px; color:var(--muted); font-size:13px; }
  label { display:block; font-size:13px; margin:12px 0 4px; }
  input { width:100%; padding:9px 11px; border:1px solid var(--line); border-radius:8px; background:transparent; color:inherit; font:inherit; }
  input:focus { outline:2px solid var(--accent); outline-offset:1px; }
  button { width:100%; margin-top:20px; padding:10px; border:0; border-radius:8px; background:var(--accent); color:#fff; font:inherit; font-weight:600; cursor:pointer; }
  .err { color:var(--err); font-size:13px; margin:12px 0 0; }
</style>
</head>
<body>
<form method="post" action="/login">
  <h1>ArchSim</h1>
  <p class="sub">Architecture simulator · private instance</p>
  <input type="hidden" name="next" value="{{.Next}}">
  <label for="username">Username</label>
  <input id="username" name="username" autocomplete="username" required autofocus>
  <label for="password">Password</label>
  <input id="password" name="password" type="password" autocomplete="current-password" required>
  {{if .Error}}<p class="err" role="alert">{{.Error}}</p>{{end}}
  <button type="submit">Sign in</button>
</form>
</body>
</html>`))

func renderLogin(w http.ResponseWriter, status int, next, errMsg string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; form-action 'self'; frame-ancestors 'none'; base-uri 'none'")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	if err := loginTmpl.Execute(w, struct{ Next, Error string }{safeNext(next), errMsg}); err != nil {
		slog.Error("render login", "err", err)
	}
}
