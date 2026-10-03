package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httputil"
	"strconv"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

type failurePolicy string

const (
	policyBlock failurePolicy = "BLOCK"
	policyPass  failurePolicy = "PASS"
)

type ctxKey string

const proxyCtxKey ctxKey = "guardian.proxyctx"

// openAIRequest is the subset of the chat-completions payload we rewrite.
type openAIRequest struct {
	Model    string    `json:"model"`
	Stream   bool      `json:"stream"`
	Messages []message `json:"messages"`
}

type message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type gateway struct {
	rdb         *redis.Client
	cfg         *configStore
	proxy       *httputil.ReverseProxy
	httpClient  *http.Client
	presidioURL string

	// sink/resolver are the token store; Redis in production, in-memory in tests.
	sink     tokenSink
	resolver tokenResolver

	mockUpstream    bool
	upstreamKey     string
	upstreamHost    string
	failurePolicy   failurePolicy
	tokenTTL        time.Duration
	jwtSecret       []byte
	allowRoleHeader bool
	auditSize       int
}

func (g *gateway) incr(key string) {
	if err := g.rdb.Incr(ctx, key).Err(); err != nil {
		logf("redis incr %s failed: %v", key, err)
	}
}

func (g *gateway) serviceName() string {
	if g.upstreamHost != "" {
		return g.upstreamHost
	}
	return "llm-upstream"
}

// handleChat is the sanitizing front door for /v1/chat/completions.
func (g *gateway) handleChat(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "cannot_read_body", "Guardian could not read the request body.")
		return
	}
	_ = r.Body.Close()

	var req openAIRequest
	if err := json.Unmarshal(body, &req); err != nil || len(req.Messages) == 0 {
		// A shape we can't rewrite (multimodal arrays, streaming frames…):
		// pass it through untouched rather than break the client.
		g.forward(w, r, body, nil)
		return
	}

	id := g.identityFromRequest(r)
	pc := &proxyContext{
		identity:  id,
		model:     orDefault(req.Model, "default"),
		team:      id.budgetTeam(),
		service:   g.serviceName(),
		ip:        clientIP(r),
		user:      id.displayName(),
		startedAt: time.Now(),
	}

	// 1. Budget gate first — cheapest possible rejection.
	if st := g.budgetStatus(pc.team); st.HasCap && st.Exceeded {
		g.blockBudget(w, pc, st)
		return
	}

	// 2. Detect and mask, honouring the caller's role policy.
	allowed := g.cfg.allowedFor(id.policyKeys())
	sink := g.sink

	var promptChars int
	for i := range req.Messages {
		msg := &req.Messages[i]
		promptChars += len(msg.Content)
		if strings.EqualFold(msg.Role, "assistant") || strings.TrimSpace(msg.Content) == "" {
			continue
		}

		spans, perr := g.detectSpans(msg.Content, g.cfg)
		if perr != nil {
			g.incr("stats:presidio_failures")
			if g.failurePolicy == policyBlock {
				g.pushAlert("presidio", "Presidio unavailable — request blocked (fail-closed): "+perr.Error())
				g.pushAudit(pc, "blocked_presidio", "")
				g.blockPresidio(w)
				return
			}
			g.pushAlert("presidio", "Presidio unavailable — failing open to protect workflows: "+perr.Error())
		}

		res := maskText(msg.Content, spans, allowed, sink)
		msg.Content = res.Masked
		pc.tokens = append(pc.tokens, res.Tokens...)
		pc.entities = append(pc.entities, res.Entities...)
	}
	pc.promptChars = promptChars

	// 3. Record metrics + the audit entry (masked tokens only).
	g.incr("stats:total_requests")
	status := "clean"
	if len(pc.tokens) > 0 {
		status = "masked"
	}
	g.pushAudit(pc, status, truncate(req.Messages[len(req.Messages)-1].Content, 200))

	if g.mockUpstream {
		g.writeMock(w, req, pc)
		return
	}

	clean, err := json.Marshal(req)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "cannot_encode_body", "Guardian could not encode the sanitized body.")
		return
	}
	g.forward(w, r, clean, pc)
}

// writeMock echoes the sanitized payload (and what the client would get back
// after rehydration) so the smoke test needs no API key.
func (g *gateway) writeMock(w http.ResponseWriter, req openAIRequest, pc *proxyContext) {
	payload := map[string]any{
		"guardian":            "sanitized",
		"upstream":            "mock",
		"model":               pc.model,
		"messages":            req.Messages,
		"rehydrated_messages": g.rehydrateMessages(req.Messages),
	}
	out, err := json.Marshal(payload)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "cannot_encode_body", "Guardian could not encode the mock response.")
		return
	}
	pc.finalize(g, len(out))
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(out)
}

func (g *gateway) rehydrateMessages(msgs []message) []message {
	out := make([]message, len(msgs))
	for i, m := range msgs {
		out[i] = message{Role: m.Role, Content: string(rehydrateAll([]byte(m.Content), g.resolver))}
	}
	return out
}

func (g *gateway) forward(w http.ResponseWriter, r *http.Request, body []byte, pc *proxyContext) {
	r.Body = io.NopCloser(bytes.NewReader(body))
	r.ContentLength = int64(len(body))
	r.Header.Set("Content-Length", strconv.Itoa(len(body)))
	if g.upstreamKey != "" && r.Header.Get("Authorization") == "" {
		r.Header.Set("Authorization", "Bearer "+g.upstreamKey)
	}
	if pc != nil {
		r = r.WithContext(context.WithValue(r.Context(), proxyCtxKey, pc))
	}
	g.proxy.ServeHTTP(w, r)
}

// modifyResponse rehydrates the upstream response: streaming bodies are
// transformed chunk-by-chunk, everything else is buffered and rewritten.
func (g *gateway) modifyResponse(resp *http.Response) error {
	pc, _ := resp.Request.Context().Value(proxyCtxKey).(*proxyContext)
	if pc == nil {
		return nil
	}
	resolver := g.resolver

	if strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		resp.Body = newRehydrateReader(resp.Body, resolver, func(n int) { pc.finalize(g, n) })
		resp.Header.Del("Content-Length")
		resp.ContentLength = -1
		return nil
	}

	raw, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil {
		return err
	}
	parseUsage(raw, pc)

	out := rehydrateAll(raw, resolver)
	pc.finalize(g, len(out))

	resp.Body = io.NopCloser(bytes.NewReader(out))
	resp.ContentLength = int64(len(out))
	resp.Header.Set("Content-Length", strconv.Itoa(len(out)))
	return nil
}

func (g *gateway) blockBudget(w http.ResponseWriter, pc *proxyContext, st budgetState) {
	g.incr("stats:budget_blocks")
	g.pushAudit(pc, "blocked_budget", "")
	g.pushAlert("budget", "Monthly AI budget exceeded for '"+st.Team+"' ($"+
		strconv.FormatFloat(st.Spent, 'f', 2, 64)+" / $"+strconv.FormatFloat(st.Cap, 'f', 2, 64)+")")
	writeJSONError(w, http.StatusTooManyRequests, "guardian_budget_exceeded",
		"AI Budget Exceeded. Contact your administrator.")
}

func (g *gateway) blockPresidio(w http.ResponseWriter) {
	writeJSONError(w, http.StatusServiceUnavailable, "guardian_failure_policy",
		"Guardian could not scan this request and FAILURE_POLICY=BLOCK is set. Try again shortly.")
}

func writeJSONError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"error": map[string]any{"type": code, "message": message},
	})
}

func orDefault(v, fallback string) string {
	if strings.TrimSpace(v) == "" {
		return fallback
	}
	return v
}
