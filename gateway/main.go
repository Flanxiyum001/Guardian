// Guardian is a transparent, OpenAI-compatible reverse proxy that detects and
// tokenizes PII and enterprise-dictionary secrets before a request ever leaves
// your network, then rehydrates the real values back into the response. Real
// values live in Redis under opaque tokens; the upstream provider only ever
// sees the tokens.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

const (
	listenAddr      = ":8080"
	minScore        = 0.6
	presidioTimeout = 5 * time.Second
)

var ctx = context.Background()

func main() {
	log.SetFlags(log.LstdFlags | log.Lmsgprefix)
	log.SetPrefix("[guardian] ")

	g := &gateway{
		presidioURL:     strings.TrimRight(envOr("PRESIDIO_URL", "http://localhost:3000"), "/"),
		mockUpstream:    envOr("MOCK_UPSTREAM", "false") == "true",
		upstreamKey:     os.Getenv("OPENAI_API_KEY"),
		jwtSecret:       []byte(os.Getenv("JWT_SECRET")),
		allowRoleHeader: envOr("ALLOW_ROLE_HEADER", "false") == "true",
		tokenTTL:        time.Duration(envInt("TOKEN_TTL_HOURS", 24)) * time.Hour,
		auditSize:       envInt("AUDIT_LOG_SIZE", 500),
		httpClient:      &http.Client{Timeout: presidioTimeout},
	}
	switch strings.ToUpper(envOr("FAILURE_POLICY", "BLOCK")) {
	case "PASS", "FAIL_OPEN", "OPEN":
		g.failurePolicy = policyPass
	default:
		g.failurePolicy = policyBlock
	}

	redisURL := envOr("REDIS_URL", "redis://localhost:6379")
	opt, err := redis.ParseURL(redisURL)
	if err != nil {
		log.Fatalf("invalid REDIS_URL %q: %v", redisURL, err)
	}
	g.rdb = redis.NewClient(opt)
	g.sink = redisSink{rdb: g.rdb, ttl: g.tokenTTL}
	g.resolver = redisResolver{rdb: g.rdb}

	upstream := envOr("UPSTREAM_URL", "https://api.openai.com")
	target, err := url.Parse(upstream)
	if err != nil || target.Scheme == "" || target.Host == "" {
		log.Fatalf("invalid UPSTREAM_URL %q", upstream)
	}
	g.upstreamHost = target.Host

	g.proxy = httputil.NewSingleHostReverseProxy(target)
	g.proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		logf("upstream request failed: %v", err)
		writeJSONError(w, http.StatusBadGateway, "upstream_unreachable", "The upstream AI provider could not be reached.")
	}
	g.proxy.ModifyResponse = g.modifyResponse

	// Hot-reloadable policy: custom dictionary regexes, role masking rules,
	// budgets and pricing.
	g.cfg = newConfigStore(envOr("CONFIG_PATH", "config/custom-rules.json"))
	g.cfg.publish = g.publishConfig
	g.cfg.load()
	g.cfg.watch()

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	mux.Handle("/v1/chat/completions", http.HandlerFunc(g.handleChat))
	mux.Handle("/", g.proxy)

	srv := &http.Server{
		Addr:              listenAddr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}

	logf("🛡️  listening on %s (upstream=%s, presidio=%s, mock=%v, failure_policy=%s)",
		listenAddr, target, g.presidioURL, g.mockUpstream, g.failurePolicy)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
}

// publishConfig mirrors the active config to Redis so the dashboard can render
// budgets without reading the gateway's filesystem.
func (g *gateway) publishConfig(cfg *appConfig) {
	if cfg == nil || g.rdb == nil {
		return
	}
	blob, err := json.Marshal(map[string]any{
		"budgets": cfg.Budgets,
		"pricing": cfg.Pricing,
		"roles":   len(cfg.RolePolicy.Roles),
		"rules":   len(cfg.Rules),
		"updated": time.Now().Unix(),
	})
	if err != nil {
		return
	}
	if err := g.rdb.Set(ctx, "config:active", blob, 0).Err(); err != nil {
		logf("publish config failed: %v", err)
	}
}

func logf(format string, args ...any) { log.Printf(format, args...) }

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func envInt(key string, fallback int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return fallback
}
