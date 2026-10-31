package main

import (
	"bytes"
	"compress/gzip"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"
)

const (
	testUser     = "rizky"
	testPassword = "correct horse battery staple"
)

func testConfig(t *testing.T) config {
	t.Helper()
	hash, err := bcrypt.GenerateFromPassword([]byte(testPassword), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte("<h1>app</h1>"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "assets"), 0o755); err != nil {
		t.Fatal(err)
	}
	return config{
		webDir: dir, username: testUser, passwordHash: hash,
		secret: []byte(strings.Repeat("s", 32)), ttl: time.Hour, secureCookie: true,
	}
}

func testAuth() *auth {
	return &auth{username: testUser, secret: []byte(strings.Repeat("k", 32)), ttl: time.Hour, now: time.Now}
}

func TestSessionTokens(t *testing.T) {
	a := testAuth()
	token, _ := a.issue()
	if !a.valid(token) {
		t.Fatal("fresh token must be valid")
	}
	enc, mac, _ := strings.Cut(token, ".")
	if a.valid(enc + "x." + mac) {
		t.Error("tampered payload must be rejected")
	}
	if a.valid(enc + "." + mac + "x") {
		t.Error("tampered signature must be rejected")
	}

	other := testAuth()
	other.username = "someone-else"
	if other.valid(token) {
		t.Error("token for another username must be rejected")
	}

	expired := testAuth()
	expired.now = func() time.Time { return time.Now().Add(2 * time.Hour) }
	if expired.valid(token) {
		t.Error("expired token must be rejected")
	}
}

func TestSafeNext(t *testing.T) {
	for in, want := range map[string]string{
		"/designs/1":           "/designs/1",
		"":                     "/",
		"https://evil.example": "/",
		"//evil.example":       "/",
		"/\\evil.example":      "/",
	} {
		if got := safeNext(in); got != want {
			t.Errorf("safeNext(%q) = %q, want %q", in, got, want)
		}
	}
}

func postLogin(h http.Handler, user, pass string, header http.Header) *httptest.ResponseRecorder {
	form := url.Values{"username": {user}, "password": {pass}, "next": {"/designs"}}
	req := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	for k, v := range header {
		req.Header[k] = v
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestLoginFlow(t *testing.T) {
	h := newHandler(testConfig(t))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/designs", nil))
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/login?next=%2Fdesigns" {
		t.Fatalf("unauthenticated page: %d %q", rec.Code, rec.Header().Get("Location"))
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/designs", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated API: %d, want 401", rec.Code)
	}

	if rec := postLogin(h, testUser, "wrong", nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("wrong password: %d, want 401", rec.Code)
	}

	rec = postLogin(h, testUser, testPassword, nil)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/designs" {
		t.Fatalf("login: %d %q", rec.Code, rec.Header().Get("Location"))
	}
	cookies := rec.Result().Cookies()
	if len(cookies) != 1 || !cookies[0].HttpOnly || !cookies[0].Secure || cookies[0].SameSite != http.SameSiteLaxMode {
		t.Fatalf("session cookie flags wrong: %+v", cookies)
	}

	for path, want := range map[string]int{"/designs": 200, "/": 200, "/assets": 200, "/missing.js": 404} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.AddCookie(cookies[0])
		rec = httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != want {
			t.Errorf("GET %s with session: %d, want %d", path, rec.Code, want)
		}
		if want == 200 && !strings.Contains(rec.Body.String(), "<h1>app</h1>") {
			t.Errorf("GET %s did not serve the app (directory listing?): %q", path, rec.Body.String())
		}
	}
}

func TestLoginRejectsCrossOrigin(t *testing.T) {
	h := newHandler(testConfig(t))
	rec := postLogin(h, testUser, testPassword, http.Header{"Origin": {"https://evil.example"}})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("cross-origin login: %d, want 403", rec.Code)
	}
}

func TestLoginRateLimit(t *testing.T) {
	h := newHandler(testConfig(t))
	for range loginMaxFailures {
		postLogin(h, testUser, "wrong", nil)
	}
	rec := postLogin(h, testUser, testPassword, nil)
	if rec.Code != http.StatusTooManyRequests || rec.Header().Get("Retry-After") == "" {
		t.Fatalf("after %d failures: %d, want 429 with Retry-After", loginMaxFailures, rec.Code)
	}
}

func TestLoadConfigValidates(t *testing.T) {
	_, err := loadConfig(func(string) string { return "" })
	if err == nil {
		t.Fatal("empty config must fail")
	}
	for _, want := range []string{"ARCHSIM_USERNAME", "ARCHSIM_PASSWORD_HASH", "ARCHSIM_SESSION_SECRET"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not mention %s: %v", want, err)
		}
	}
}

func TestServesPrecompressedAssets(t *testing.T) {
	cfg := testConfig(t)
	wasm := []byte("\x00asm fake module")
	var gz bytes.Buffer
	zw := gzip.NewWriter(&gz)
	zw.Write(wasm)
	zw.Close()
	for name, data := range map[string][]byte{"engine.wasm": wasm, "engine.wasm.gz": gz.Bytes()} {
		if err := os.WriteFile(filepath.Join(cfg.webDir, name), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	h := newHandler(cfg)
	login := postLogin(h, testUser, testPassword, nil)
	cookie := login.Result().Cookies()[0]

	get := func(acceptEncoding string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/engine.wasm", nil)
		req.AddCookie(cookie)
		if acceptEncoding != "" {
			req.Header.Set("Accept-Encoding", acceptEncoding)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	rec := get("gzip, deflate, br")
	if rec.Header().Get("Content-Encoding") != "gzip" || rec.Header().Get("Content-Type") != "application/wasm" || !bytes.Equal(rec.Body.Bytes(), gz.Bytes()) {
		t.Errorf("gzip client: encoding %q, type %q", rec.Header().Get("Content-Encoding"), rec.Header().Get("Content-Type"))
	}
	rec = get("")
	if rec.Header().Get("Content-Encoding") != "" || !bytes.Equal(rec.Body.Bytes(), wasm) {
		t.Errorf("identity client got encoding %q", rec.Header().Get("Content-Encoding"))
	}
	if rec = get("gzip;q=0"); rec.Header().Get("Content-Encoding") != "" {
		t.Error("gzip;q=0 must not get gzip")
	}
}
