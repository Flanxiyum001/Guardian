// Guardian is a transparent, OpenAI-compatible reverse proxy that detects and
// tokenizes PII with Microsoft Presidio before the request ever leaves your
// network. The real values are stored in Redis under opaque tokens; the upstream
// provider only ever sees the tokens.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

const (
	listenAddr      = ":8080"
	tokenTTL        = time.Hour
	minScore        = 0.6
	presidioTimeout = 5 * time.Second
)

var (
	ctx          = context.Background()
	rdb          *redis.Client
	presidioURL  string
	mockUpstream bool
	upstreamKey  string
)

// openAIRequest is the subset of the chat-completions payload we rewrite.
type openAIRequest struct {
	Messages []message `json:"messages"`
}

type message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type presidioRequest struct {
	Text     string `json:"text"`
	Language string `json:"language"`
}

// presidioFinding mirrors Presidio's /analyze response item.
type presidioFinding struct {
	Index      int     `json:"index"`
	EntityType string  `json:"entity_type"`
	Start      int     `json:"start"`
	End        int     `json:"end"`
	Score      float64 `json:"score"`
}

func main() {
	log.SetFlags(log.LstdFlags | log.Lmsgprefix)
	log.SetPrefix("[guardian] ")

	redisURL := envOr("REDIS_URL", "redis://localhost:6379")
	opt, err := redis.ParseURL(redisURL)
	if err != nil {
		log.Fatalf("invalid REDIS_URL %q: %v", redisURL, err)
	}
	rdb = redis.NewClient(opt)

	presidioURL = strings.TrimRight(envOr("PRESIDIO_URL", "http://localhost:3000"), "/")
	mockUpstream = envOr("MOCK_UPSTREAM", "false") == "true"
	upstreamKey = os.Getenv("OPENAI_API_KEY")

	upstream := envOr("UPSTREAM_URL", "https://api.openai.com")
	target, err := url.Parse(upstream)
	if err != nil || target.Scheme == "" || target.Host == "" {
		log.Fatalf("invalid UPSTREAM_URL %q", upstream)
	}

	proxy := httputil.NewSingleHostReverseProxy(target)
	proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		log.Printf("upstream request failed: %v", err)
		http.Error(w, `{"error":"upstream_unreachable"}`, http.StatusBadGateway)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	// Everything under /v1/chat/completions is scanned before forwarding.
	mux.Handle("/v1/chat/completions", sanitize(proxy))
	mux.Handle("/", proxy)

	srv := &http.Server{
		Addr:              listenAddr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}

	log.Printf("🛡️  listening on %s (upstream=%s, presidio=%s, mock=%v)",
		listenAddr, target, presidioURL, mockUpstream)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
}

// sanitize returns the handler that tokenizes PII in the last chat message,
// records stats in Redis, then either forwards upstream or echoes the result.
func sanitize(proxy *httputil.ReverseProxy) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, `{"error":"cannot_read_body"}`, http.StatusBadRequest)
			return
		}
		_ = r.Body.Close()

		var req openAIRequest
		if err := json.Unmarshal(body, &req); err != nil || len(req.Messages) == 0 {
			// Something we can't safely rewrite (streaming frames, multimodal
			// content arrays, …). Forward the original bytes untouched.
			forward(proxy, w, r, body)
			return
		}

		last := len(req.Messages) - 1
		req.Messages[last].Content = sanitizeText(req.Messages[last].Content)

		if err := rdb.Incr(ctx, "stats:total_requests").Err(); err != nil {
			log.Printf("redis incr total_requests failed: %v", err)
		}

		if mockUpstream {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"guardian": "sanitized",
				"upstream": "mock",
				"messages": req.Messages,
			})
			return
		}

		clean, err := json.Marshal(req)
		if err != nil {
			http.Error(w, `{"error":"cannot_encode_body"}`, http.StatusInternalServerError)
			return
		}
		forward(proxy, w, r, clean)
	})
}

// forward replaces the request body and hands the request to the reverse proxy.
func forward(proxy *httputil.ReverseProxy, w http.ResponseWriter, r *http.Request, body []byte) {
	r.Body = io.NopCloser(bytes.NewReader(body))
	r.ContentLength = int64(len(body))
	r.Header.Set("Content-Length", strconv.Itoa(len(body)))
	if upstreamKey != "" && r.Header.Get("Authorization") == "" {
		r.Header.Set("Authorization", "Bearer "+upstreamKey)
	}
	proxy.ServeHTTP(w, r)
}

// sanitizeText asks Presidio for PII spans and masks each confident finding.
// On any Presidio failure it fails open by returning the original text rather
// than breaking the client request.
func sanitizeText(text string) string {
	if strings.TrimSpace(text) == "" {
		return text
	}

	payload, err := json.Marshal(presidioRequest{Text: text, Language: "en"})
	if err != nil {
		return text
	}

	client := &http.Client{Timeout: presidioTimeout}
	resp, err := client.Post(presidioURL+"/analyze", "application/json", bytes.NewReader(payload))
	if err != nil {
		log.Printf("presidio analyze failed, forwarding raw text: %v", err)
		return text
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		log.Printf("presidio returned status %d, forwarding raw text", resp.StatusCode)
		return text
	}

	var findings []presidioFinding
	if err := json.NewDecoder(resp.Body).Decode(&findings); err != nil {
		log.Printf("presidio decode failed, forwarding raw text: %v", err)
		return text
	}

	return tokenize(text, findings)
}

// tokenize rebuilds the string from the original text using Presidio's byte
// offsets, walking left-to-right so each replacement cannot shift later spans.
func tokenize(text string, findings []presidioFinding) string {
	valid := make([]presidioFinding, 0, len(findings))
	for _, f := range findings {
		if f.Score >= minScore && f.Start >= 0 && f.End <= len(text) && f.Start < f.End {
			valid = append(valid, f)
		}
	}
	sort.Slice(valid, func(i, j int) bool {
		if valid[i].Start == valid[j].Start {
			return valid[i].End > valid[j].End
		}
		return valid[i].Start < valid[j].Start
	})

	var out strings.Builder
	out.Grow(len(text))
	cursor := 0
	for _, f := range valid {
		if f.Start < cursor {
			continue // overlaps a span we already masked
		}
		out.WriteString(text[cursor:f.Start])

		value := text[f.Start:f.End]
		token := fmt.Sprintf("[MASKED_%s_%d]", f.EntityType, time.Now().UnixNano())
		if err := rdb.Set(ctx, token, value, tokenTTL).Err(); err != nil {
			log.Printf("redis set token failed: %v", err)
		} else if err := rdb.Incr(ctx, "stats:leaks_blocked").Err(); err != nil {
			log.Printf("redis incr leaks_blocked failed: %v", err)
		}

		out.WriteString(token)
		cursor = f.End
	}
	out.WriteString(text[cursor:])
	return out.String()
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
