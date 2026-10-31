// Command archsim-api serves the ArchSim web app behind single-user login.
//
//	archsim-api                 start the server (configured via ARCHSIM_* env vars)
//	archsim-api hash-password   read a password from stdin, print its bcrypt hash
package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"mime"
	"net/http"
	"os"
	"os/signal"
	"path"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/crypto/bcrypt"
	"golang.org/x/term"
)

const (
	bcryptCost       = 12
	minSecretLength  = 32
	loginMaxFailures = 5
	loginWindow      = 15 * time.Minute
)

type config struct {
	addr         string
	webDir       string
	username     string
	passwordHash []byte
	secret       []byte
	ttl          time.Duration
	secureCookie bool
	trustProxy   bool
}

func main() {
	if len(os.Args) > 1 && os.Args[1] == "hash-password" {
		if err := hashPassword(); err != nil {
			fmt.Fprintln(os.Stderr, "archsim-api:", err)
			os.Exit(1)
		}
		return
	}
	cfg, err := loadConfig(os.Getenv)
	if err != nil {
		fmt.Fprintln(os.Stderr, "archsim-api: config:", err)
		os.Exit(1)
	}
	if err := serve(cfg); err != nil {
		slog.Error("server stopped", "err", err)
		os.Exit(1)
	}
}

func loadConfig(getenv func(string) string) (config, error) {
	cfg := config{
		addr:     envOr(getenv, "ARCHSIM_ADDR", ":8080"),
		webDir:   envOr(getenv, "ARCHSIM_WEB_DIR", "apps/web/dist"),
		username: getenv("ARCHSIM_USERNAME"),
		secret:   []byte(getenv("ARCHSIM_SESSION_SECRET")),
	}
	var errs []error
	if cfg.username == "" {
		errs = append(errs, errors.New("ARCHSIM_USERNAME is required"))
	}
	hash := getenv("ARCHSIM_PASSWORD_HASH")
	if _, err := bcrypt.Cost([]byte(hash)); err != nil {
		errs = append(errs, errors.New("ARCHSIM_PASSWORD_HASH must be a bcrypt hash (generate one with: archsim-api hash-password)"))
	}
	cfg.passwordHash = []byte(hash)
	if len(cfg.secret) < minSecretLength {
		errs = append(errs, fmt.Errorf("ARCHSIM_SESSION_SECRET must be at least %d characters (e.g. openssl rand -hex 32)", minSecretLength))
	}
	var err error
	if cfg.ttl, err = time.ParseDuration(envOr(getenv, "ARCHSIM_SESSION_TTL", "168h")); err != nil || cfg.ttl <= 0 {
		errs = append(errs, errors.New("ARCHSIM_SESSION_TTL must be a positive duration such as 168h"))
	}
	if cfg.secureCookie, err = strconv.ParseBool(envOr(getenv, "ARCHSIM_COOKIE_SECURE", "true")); err != nil {
		errs = append(errs, errors.New("ARCHSIM_COOKIE_SECURE must be true or false"))
	}
	if cfg.trustProxy, err = strconv.ParseBool(envOr(getenv, "ARCHSIM_TRUST_PROXY", "false")); err != nil {
		errs = append(errs, errors.New("ARCHSIM_TRUST_PROXY must be true or false"))
	}
	return cfg, errors.Join(errs...)
}

func envOr(getenv func(string) string, key, def string) string {
	if v := getenv(key); v != "" {
		return v
	}
	return def
}

func newHandler(cfg config) http.Handler {
	a := &auth{
		username: cfg.username, passwordHash: cfg.passwordHash, secret: cfg.secret, ttl: cfg.ttl,
		secureCookie: cfg.secureCookie, trustProxy: cfg.trustProxy,
		limiter: newLoginLimiter(loginMaxFailures, loginWindow), now: time.Now,
	}
	public := http.NewServeMux()
	public.HandleFunc("/login", a.handleLogin)
	public.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte("ok")) })

	private := http.NewServeMux()
	private.HandleFunc("/logout", a.handleLogout)
	private.Handle("/", spa(os.DirFS(cfg.webDir)))

	root := http.NewServeMux()
	root.Handle("/login", public)
	root.Handle("/healthz", public)
	root.Handle("/", a.require(private))
	return securityHeaders(root)
}

// appCSP allows only same-origin code plus WebAssembly compilation (the
// engine). Inline styles are needed by React Flow's positioning. The login
// page sets its own, stricter policy.
const appCSP = "default-src 'self'; script-src 'self' 'wasm-unsafe-eval'; worker-src 'self'; style-src 'self' 'unsafe-inline'; " +
	"img-src 'self' data: blob:; connect-src 'self'; object-src 'none'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'"

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", appCSP)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "same-origin")
		next.ServeHTTP(w, r)
	})
}

// spa serves the built web app, falling back to index.html for client-side
// routes (paths without a file extension).
func spa(fsys fs.FS) http.Handler {
	files := http.FileServerFS(fsys)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if name := strings.TrimPrefix(path.Clean(r.URL.Path), "/"); name != "" {
			// Directories are never listed; they fall through to the SPA routes.
			if info, err := fs.Stat(fsys, name); err == nil && !info.IsDir() {
				// Vite puts content-hashed files under assets/; everything else
				// (engine.wasm included) is revalidated on each load.
				if strings.HasPrefix(name, "assets/") {
					w.Header().Set("Cache-Control", "private, max-age=31536000, immutable")
				} else {
					w.Header().Set("Cache-Control", "no-cache")
				}
				if acceptsGzip(r) && serveGzipped(w, r, fsys, name) {
					return
				}
				files.ServeHTTP(w, r)
				return
			}
			if path.Ext(name) != "" {
				http.NotFound(w, r)
				return
			}
		}
		index, err := fs.ReadFile(fsys, "index.html")
		if err != nil {
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			w.WriteHeader(http.StatusServiceUnavailable)
			w.Write([]byte("ArchSim web app is not built yet (set ARCHSIM_WEB_DIR to the build output).\n"))
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		w.Write(index)
	})
}

func acceptsGzip(r *http.Request) bool {
	for _, part := range strings.Split(r.Header.Get("Accept-Encoding"), ",") {
		enc, q, _ := strings.Cut(strings.TrimSpace(part), ";")
		if strings.TrimSpace(enc) == "gzip" && strings.ReplaceAll(q, " ", "") != "q=0" {
			return true
		}
	}
	return false
}

// serveGzipped serves name.gz, precompressed at build time (make web), with
// the content type of name. It reports false when there is no .gz sibling.
func serveGzipped(w http.ResponseWriter, r *http.Request, fsys fs.FS, name string) bool {
	f, err := fsys.Open(name + ".gz")
	if err != nil {
		return false
	}
	defer f.Close()
	info, err := f.Stat()
	content, seekable := f.(io.ReadSeeker)
	if err != nil || info.IsDir() || !seekable {
		return false
	}
	h := w.Header()
	h.Add("Vary", "Accept-Encoding")
	h.Set("Content-Encoding", "gzip")
	if ct := mime.TypeByExtension(path.Ext(name)); ct != "" {
		h.Set("Content-Type", ct)
	}
	http.ServeContent(w, r, name, info.ModTime(), content)
	return true
}

func serve(cfg config) error {
	srv := &http.Server{
		Addr:              cfg.addr,
		Handler:           newHandler(cfg),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	if !cfg.secureCookie {
		slog.Warn("ARCHSIM_COOKIE_SECURE=false: session cookie is sent over plain HTTP; use only for local development")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	errc := make(chan error, 1)
	go func() {
		slog.Info("listening", "addr", cfg.addr, "webDir", cfg.webDir)
		errc <- srv.ListenAndServe()
	}()
	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return srv.Shutdown(shutdownCtx)
}

func hashPassword() error {
	var pw []byte
	if term.IsTerminal(int(os.Stdin.Fd())) {
		fmt.Fprint(os.Stderr, "Password: ")
		var err error
		if pw, err = term.ReadPassword(int(os.Stdin.Fd())); err != nil {
			return err
		}
		fmt.Fprintln(os.Stderr)
	} else {
		line, err := bufio.NewReader(os.Stdin).ReadString('\n')
		if err != nil && line == "" {
			return fmt.Errorf("read password: %w", err)
		}
		pw = []byte(strings.TrimRight(line, "\r\n"))
	}
	if len(pw) < 12 {
		return errors.New("use a password of at least 12 characters")
	}
	hash, err := bcrypt.GenerateFromPassword(pw, bcryptCost)
	if err != nil {
		return err
	}
	fmt.Println(string(hash))
	return nil
}
