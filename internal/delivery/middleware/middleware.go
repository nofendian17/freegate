package middleware

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"freegate/internal/delivery/respond"
	"freegate/internal/httputil"
)

// loggablePaths are the only requests written to the access log. Everything
// else — the dashboard's own htmx partials and /api/* JSON polls, static
// assets, and health probes — is response/asset traffic that would drown the
// log between real API calls.
var loggablePaths = map[string]struct{}{
	"/v1/chat/completions": {},
	"/v1/messages":         {},
	"/v1/models":           {},
}

func Logger(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := loggablePaths[r.URL.Path]; !ok {
			next.ServeHTTP(w, r)
			return
		}
		start := time.Now()
		ww := &wrapWriter{ResponseWriter: w, code: 200}
		next.ServeHTTP(ww, r)
		slog.Info("request",
			"method", r.Method,
			"path", r.URL.Path,
			"status", ww.code,
			"duration", time.Since(start).String(),
			"remote", httputil.ClientIP(r),
			"request_id", r.Header.Get("X-Request-ID"),
		)
	})
}

type wrapWriter struct {
	http.ResponseWriter
	code int
}

func (w *wrapWriter) WriteHeader(code int) {
	w.code = code
	w.ResponseWriter.WriteHeader(code)
}

// Flush implements http.Flusher for SSE streaming support.
func (w *wrapWriter) Flush() {
	if fl, ok := w.ResponseWriter.(http.Flusher); ok {
		fl.Flush()
	}
}

// compile-time interface check
var _ http.Flusher = (*wrapWriter)(nil)

func CORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("access-control-allow-origin", "*")
		w.Header().Set("access-control-allow-methods", "GET, POST, OPTIONS")
		w.Header().Set("access-control-allow-headers", "Content-Type, Authorization, X-API-Key")
		w.Header().Set("access-control-max-age", "86400")
		if r.Method == "OPTIONS" {
			w.WriteHeader(204)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func Recoverer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				slog.Error("panic recovered", "error", rec)
				respond.JSONError(w, http.StatusInternalServerError, "internal", "internal server error")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// RequestID generates a unique request ID and attaches it to the request.
func RequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get("X-Request-ID")
		if id == "" {
			b := make([]byte, 8)
			if _, err := rand.Read(b); err != nil {
				slog.Error("failed to generate request ID", "error", err)
				id = "fallback-" + time.Now().Format("150405.000")
			} else {
				id = hex.EncodeToString(b)
			}
		}
		w.Header().Set("X-Request-ID", id)
		r.Header.Set("X-Request-ID", id)
		next.ServeHTTP(w, r)
	})
}

func HmacForToken(token string) string {
	h := hmac.New(sha256.New, []byte(token))
	h.Write([]byte(token))
	return hex.EncodeToString(h.Sum(nil))
}

func hmacForToken(token string) string { return HmacForToken(token) }

func ApiAuth(apiKeys []string, adminToken string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if len(apiKeys) == 0 && adminToken == "" {
				next.ServeHTTP(w, r)
				return
			}
			key := r.Header.Get("X-API-Key")
			if key == "" {
				if auth := r.Header.Get("Authorization"); len(auth) > 7 && auth[:7] == "Bearer " {
					key = auth[7:]
				}
			}
			for _, k := range apiKeys {
				if subtle.ConstantTimeCompare([]byte(key), []byte(k)) == 1 {
					next.ServeHTTP(w, r)
					return
				}
			}
			if adminToken != "" && subtle.ConstantTimeCompare([]byte(key), []byte(adminToken)) == 1 {
				next.ServeHTTP(w, r)
				return
			}
			respond.JSONError(w, http.StatusUnauthorized, "unauthorized", "invalid or missing API key")
		})
	}
}

func AdminAuth(adminToken string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// check cookie
			if c, err := r.Cookie("fg_admin"); err == nil {
				exp := HmacForToken(adminToken)
				if subtle.ConstantTimeCompare([]byte(c.Value), []byte(exp)) == 1 {
					next.ServeHTTP(w, r)
					return
				}
			}
			// check header
			key := r.Header.Get("X-Admin-Token")
			if key == "" {
				if auth := r.Header.Get("Authorization"); len(auth) > 7 && auth[:7] == "Bearer " {
					key = auth[7:]
				}
			}
			if adminToken != "" && subtle.ConstantTimeCompare([]byte(key), []byte(adminToken)) == 1 {
				next.ServeHTTP(w, r)
				return
			}
			if r.Header.Get("HX-Request") == "true" || strings.Contains(r.Header.Get("Accept"), "application/json") {
				respond.JSONError(w, http.StatusUnauthorized, "unauthorized", "admin authentication required")
				return
			}
			http.Redirect(w, r, "/login?next="+url.QueryEscape(r.URL.Path), http.StatusFound)
		})
	}
}

// Auth validates the API key if configured. Skips validation if no API key is set.
// Deprecated: use ApiAuth instead.
func Auth(requiredKey string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if requiredKey == "" {
				next.ServeHTTP(w, r)
				return
			}
			key := r.Header.Get("X-API-Key")
			if key == "" {
				// Also check Authorization header: "Bearer <key>"
				auth := r.Header.Get("Authorization")
				if len(auth) > 7 && auth[:7] == "Bearer " {
					key = auth[7:]
				}
			}
			if subtle.ConstantTimeCompare([]byte(key), []byte(requiredKey)) != 1 {
				respond.JSONError(w, http.StatusUnauthorized, "unauthorized", "invalid or missing API key")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// RateLimiter provides per-IP rate limiting with sharded maps to avoid
// global mutex contention under high concurrency.
type RateLimiter struct {
	shards   [rateLimiterShards]shard
	limit    int
	stop     chan struct{}
	stopOnce sync.Once
	wg       sync.WaitGroup
}

const rateLimiterShards = 32

type shard struct {
	mu       sync.Mutex
	visitors map[string]*visitor
}

type visitor struct {
	count    int
	lastSeen time.Time
}

func NewRateLimiter(requestsPerMinute int) *RateLimiter {
	rl := &RateLimiter{
		limit: requestsPerMinute,
		stop:  make(chan struct{}),
	}
	for i := range rl.shards {
		rl.shards[i].visitors = make(map[string]*visitor)
	}
	// Cleanup stale entries every 5 minutes — per shard to keep locks short.
	rl.wg.Add(1)
	go func() {
		defer rl.wg.Done()
		ticker := time.NewTicker(5 * time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				now := time.Now()
				for i := range rl.shards {
					sh := &rl.shards[i]
					sh.mu.Lock()
					for ip, v := range sh.visitors {
						if now.Sub(v.lastSeen) > 2*time.Minute {
							delete(sh.visitors, ip)
						}
					}
					sh.mu.Unlock()
				}
			case <-rl.stop:
				return
			}
		}
	}()
	return rl
}

func (rl *RateLimiter) shardFor(ip string) *shard {
	// FNV-1a fast hash, 32 shards = mask 31
	var h uint32 = 2166136261
	for i := 0; i < len(ip); i++ {
		h ^= uint32(ip[i])
		h *= 16777619
	}
	return &rl.shards[h%rateLimiterShards]
}

// Stop terminates the background cleanup goroutine. Safe to call multiple times.
func (rl *RateLimiter) Stop() {
	rl.stopOnce.Do(func() {
		close(rl.stop)
		rl.wg.Wait()
	})
}

func (rl *RateLimiter) allow(ip string) bool {
	sh := rl.shardFor(ip)
	sh.mu.Lock()
	defer sh.mu.Unlock()

	v, exists := sh.visitors[ip]
	if !exists {
		sh.visitors[ip] = &visitor{count: 1, lastSeen: time.Now()}
		return true
	}

	now := time.Now()
	if now.Sub(v.lastSeen) > time.Minute {
		v.count = 1
		v.lastSeen = now
		return true
	}

	if v.count >= rl.limit {
		return false
	}

	v.count++
	v.lastSeen = now
	return true
}

func (rl *RateLimiter) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ip := httputil.ClientIP(r)
		if !rl.allow(ip) {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Retry-After", "60")
			w.WriteHeader(http.StatusTooManyRequests)
			w.Write([]byte(`{"error":{"type":"rate_limit","message":"rate limit exceeded, try again later"}}`))
			slog.Warn("rate limit exceeded", "remote", ip, "path", r.URL.Path, "request_id", r.Header.Get("X-Request-ID"))
			return
		}
		next.ServeHTTP(w, r)
	})
}
