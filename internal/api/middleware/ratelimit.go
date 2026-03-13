package middleware

import (
	"math"
	"net/http"
	"strconv"
	"sync"
	"time"
)

// --------------------------------------------------------------------------
// RateLimitStore is the interface a backing store must implement. The default
// in-memory implementation lives below; swap in a Redis-backed one later by
// satisfying this interface.
// --------------------------------------------------------------------------

// RateLimitStore checks whether a request identified by key should be allowed
// under the given limit configuration. It returns (allowed, retryAfterSec).
type RateLimitStore interface {
	// Allow returns true if the request is permitted. When denied,
	// retryAfter indicates how many seconds until a token becomes available.
	Allow(key string, capacity float64, refillPerSecond float64) (allowed bool, retryAfter int)
}

// --------------------------------------------------------------------------
// In-memory token bucket store
// --------------------------------------------------------------------------

type tokenBucket struct {
	mu       sync.Mutex
	tokens   float64
	maxToken float64
	refillPS float64 // tokens added per second
	lastTime time.Time
}

func newTokenBucket(capacity, refillPerSecond float64) *tokenBucket {
	return &tokenBucket{
		tokens:   capacity,
		maxToken: capacity,
		refillPS: refillPerSecond,
		lastTime: time.Now(),
	}
}

// allow attempts to consume one token. Returns (allowed, retryAfterSec).
func (b *tokenBucket) allow() (bool, int) {
	b.mu.Lock()
	defer b.mu.Unlock()

	now := time.Now()
	elapsed := now.Sub(b.lastTime).Seconds()
	b.lastTime = now

	b.tokens += elapsed * b.refillPS
	if b.tokens > b.maxToken {
		b.tokens = b.maxToken
	}

	if b.tokens < 1 {
		// How long until the next token?
		deficit := 1 - b.tokens
		secs := deficit / b.refillPS
		return false, int(math.Ceil(secs))
	}
	b.tokens--
	return true, 0
}

// MemoryStore is an in-memory RateLimitStore backed by per-key token buckets.
type MemoryStore struct {
	mu      sync.Mutex
	buckets map[string]*tokenBucket
}

// NewMemoryStore creates a new in-memory rate limit store.
func NewMemoryStore() *MemoryStore {
	s := &MemoryStore{
		buckets: make(map[string]*tokenBucket),
	}
	go s.cleanupLoop()
	return s
}

func (s *MemoryStore) get(key string, capacity, refillPS float64) *tokenBucket {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, ok := s.buckets[key]
	if !ok {
		b = newTokenBucket(capacity, refillPS)
		s.buckets[key] = b
	}
	return b
}

// Allow implements RateLimitStore.
func (s *MemoryStore) Allow(key string, capacity, refillPerSecond float64) (bool, int) {
	b := s.get(key, capacity, refillPerSecond)
	return b.allow()
}

// cleanupLoop removes idle buckets every 5 minutes to prevent unbounded growth.
func (s *MemoryStore) cleanupLoop() {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	for range ticker.C {
		s.mu.Lock()
		now := time.Now()
		for key, b := range s.buckets {
			b.mu.Lock()
			idle := now.Sub(b.lastTime)
			full := b.tokens + idle.Seconds()*b.refillPS >= b.maxToken
			b.mu.Unlock()
			// If bucket has been idle long enough to be fully refilled,
			// it's equivalent to a fresh bucket — remove it.
			if full && idle > 10*time.Minute {
				delete(s.buckets, key)
			}
		}
		s.mu.Unlock()
	}
}

// --------------------------------------------------------------------------
// Limit configuration
// --------------------------------------------------------------------------

// LimitConfig describes a rate limit tier.
type LimitConfig struct {
	Capacity       float64 // burst size
	RefillPerSec   float64 // sustained rate (tokens/sec)
	KeyFunc        func(r *http.Request) string
}

// Pre-defined limit tiers.
var (
	rlStore = NewMemoryStore()

	// Auth endpoints: 20 requests/minute per IP (stricter to prevent brute force).
	AuthLimit = LimitConfig{
		Capacity:     20,
		RefillPerSec: 20.0 / 60,
		KeyFunc:      ipKey,
	}

	// Webhook endpoints: 100 requests/minute per source IP.
	WebhookLimit = LimitConfig{
		Capacity:     100,
		RefillPerSec: 100.0 / 60,
		KeyFunc:      ipKey,
	}

	// Generation/AI endpoints: 10 requests/hour per org.
	GenerationLimit = LimitConfig{
		Capacity:     10,
		RefillPerSec: 10.0 / 3600,
		KeyFunc:      orgKey,
	}

	// Default CRUD endpoints: 1000 requests/minute per org.
	DefaultLimit = LimitConfig{
		Capacity:     1000,
		RefillPerSec: 1000.0 / 60,
		KeyFunc:      orgKey,
	}
)

// --------------------------------------------------------------------------
// Key functions
// --------------------------------------------------------------------------

// orgKey uses the authenticated org ID. Falls back to IP for unauthenticated.
func orgKey(r *http.Request) string {
	orgID := OrgIDFromCtx(r.Context())
	if orgID.String() != "00000000-0000-0000-0000-000000000000" {
		return "org:" + orgID.String()
	}
	return "ip:" + r.RemoteAddr
}

// ipKey uses the client IP address (after RealIP middleware).
func ipKey(r *http.Request) string {
	return "ip:" + r.RemoteAddr
}

// --------------------------------------------------------------------------
// Middleware constructors
// --------------------------------------------------------------------------

func rateLimitMiddleware(cfg LimitConfig) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			key := cfg.KeyFunc(r)
			allowed, retryAfter := rlStore.Allow(key, cfg.Capacity, cfg.RefillPerSec)
			if !allowed {
				if retryAfter < 1 {
					retryAfter = 1
				}
				w.Header().Set("Retry-After", strconv.Itoa(retryAfter))
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusTooManyRequests)
				_, _ = w.Write([]byte(`{"error":"rate limit exceeded"}`))
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// AuthRateLimit enforces 20 requests/minute per IP for auth endpoints.
func AuthRateLimit() func(http.Handler) http.Handler {
	return rateLimitMiddleware(AuthLimit)
}

// GenerationRateLimit enforces 10 requests/hour per org for generation endpoints.
func GenerationRateLimit() func(http.Handler) http.Handler {
	return rateLimitMiddleware(GenerationLimit)
}

// WebhookRateLimit enforces 100 requests/minute per source IP for webhook endpoints.
func WebhookRateLimit() func(http.Handler) http.Handler {
	return rateLimitMiddleware(WebhookLimit)
}

// DefaultRateLimit enforces 1000 requests/minute per org for all other endpoints.
func DefaultRateLimit() func(http.Handler) http.Handler {
	return rateLimitMiddleware(DefaultLimit)
}
