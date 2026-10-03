package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/golang-jwt/jwt/v5"
	"github.com/redis/go-redis/v9"
)

// ---- test doubles ----

type memSink struct{ m map[string]string }

func (s *memSink) Store(token, value string) { s.m[token] = value }

type memResolver struct{ m map[string]string }

func (r memResolver) Resolve(token string) (string, bool) {
	v, ok := r.m[token]
	return v, ok
}

// chunkReader hands out a fixed sequence of chunks, so we can simulate an SSE
// stream that splits a token across reads.
type chunkReader struct {
	parts []string
	i     int
}

func (c *chunkReader) Read(p []byte) (int, error) {
	if c.i >= len(c.parts) {
		return 0, io.EOF
	}
	n := copy(p, c.parts[c.i])
	c.i++
	return n, nil
}

// ---- masking ----

func TestMaskTextMasksMultipleEntities(t *testing.T) {
	sink := &memSink{m: map[string]string{}}
	text := "call 555-0199 or test@company.com now"
	spans := []span{
		{Start: 5, End: 13, EntityType: "PHONE_NUMBER", Score: 0.95},
		{Start: 17, End: 33, EntityType: "EMAIL_ADDRESS", Score: 0.90},
	}

	res := maskText(text, spans, nil, sink)

	if strings.Contains(res.Masked, "555-0199") || strings.Contains(res.Masked, "test@company.com") {
		t.Fatalf("expected PII masked, got %q", res.Masked)
	}
	if len(res.Tokens) != 2 || len(res.Entities) != 2 {
		t.Fatalf("expected 2 tokens/entities, got %d/%d", len(res.Tokens), len(res.Entities))
	}
	if len(sink.m) != 2 {
		t.Fatalf("expected 2 stored token mappings, got %d", len(sink.m))
	}
	if !strings.HasPrefix(res.Masked, "call ") || !strings.HasSuffix(res.Masked, " now") {
		t.Fatalf("expected surrounding text preserved, got %q", res.Masked)
	}
	for _, entity := range res.Entities {
		if !strings.Contains(res.Masked, "[MASKED_"+entity+"_") {
			t.Fatalf("expected token for %s in %q", entity, res.Masked)
		}
	}
}

func TestMaskTextHonoursRoleAllowList(t *testing.T) {
	sink := &memSink{m: map[string]string{}}
	text := "customer zip 94107"
	spans := []span{{Start: 12, End: 17, EntityType: "ZIP_CODE", Score: 0.9}}

	res := maskText(text, spans, map[string]bool{"ZIP_CODE": true}, sink)

	if res.Masked != text {
		t.Fatalf("allowed entity should pass through, got %q", res.Masked)
	}
	if len(res.Tokens) != 0 || len(sink.m) != 0 {
		t.Fatalf("allowed entity should not be tokenized")
	}
}

func TestMaskTextSkipsLowScoreOverlapAndOutOfRange(t *testing.T) {
	sink := &memSink{m: map[string]string{}}
	text := "abc123"
	spans := []span{
		{Start: 0, End: 3, EntityType: "LOW", Score: 0.30},
		{Start: 0, End: 6, EntityType: "WIN", Score: 0.90},
		{Start: 3, End: 6, EntityType: "OVERLAP", Score: 0.90},
		{Start: 2, End: 99, EntityType: "OOR", Score: 0.90},
	}

	res := maskText(text, spans, nil, sink)

	if len(res.Tokens) != 1 {
		t.Fatalf("expected exactly one mask, got %d (%v)", len(res.Tokens), res.Masked)
	}
	if !strings.Contains(res.Masked, "[MASKED_WIN_") {
		t.Fatalf("expected widest span to win, got %q", res.Masked)
	}
}

func TestCustomSpansMatchesEnterpriseDictionary(t *testing.T) {
	rules := []compiledRule{{
		EntityType: "PROJECT_CODENAME",
		Score:      0.9,
		Patterns:   []*regexp.Regexp{regexp.MustCompile(`Project\s+[A-Z][A-Za-z0-9]+`), regexp.MustCompile(`Secret-V\d+`)},
	}}

	spans := customSpans("we ship Project Aurora and Secret-V3 next week", rules)

	if len(spans) != 2 {
		t.Fatalf("expected 2 custom spans, got %d", len(spans))
	}
	for _, s := range spans {
		if s.EntityType != "PROJECT_CODENAME" || s.Source != "custom" {
			t.Fatalf("unexpected span %+v", s)
		}
	}
}

// ---- rehydration ----

func TestIsPartialToken(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"[MAS", true},
		{"[", true},
		{"[MASKED_", true},
		{"[MASKED_EMAIL_ADDRESS_17", true},
		{"[MASKED_X_1]", false},
		{"hello", false},
		{"[MASKED_ with space", false},
	}
	for _, c := range cases {
		if got := isPartialToken(c.in); got != c.want {
			t.Errorf("isPartialToken(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestRehydrateAllReplacesKnownTokens(t *testing.T) {
	res := memResolver{m: map[string]string{"[MASKED_EMAIL_ADDRESS_1]": "jane@acme.com"}}
	got := string(rehydrateAll([]byte("hello [MASKED_EMAIL_ADDRESS_1] bye"), res))
	if got != "hello jane@acme.com bye" {
		t.Fatalf("unexpected rehydration: %q", got)
	}
}

func TestRehydrateReaderHandlesTokenSplitAcrossChunks(t *testing.T) {
	res := memResolver{m: map[string]string{"[MASKED_EMAIL_ADDRESS_1]": "jane@acme.com"}}
	// The token is split mid-way, exactly like a real SSE chunk boundary.
	src := &chunkReader{parts: []string{
		`data: {"delta":"contact [MASKED_EMAI`,
		`L_ADDRESS_1] now"}`,
		"\n\n",
	}}

	var finalized int
	r := newRehydrateReader(io.NopCloser(src), res, func(n int) { finalized = n })

	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	body := string(out)
	if !strings.Contains(body, "jane@acme.com") {
		t.Fatalf("expected rehydrated email, got %q", body)
	}
	if strings.Contains(body, "MASKED") {
		t.Fatalf("expected no tokens left in stream, got %q", body)
	}

	// io.ReadAll does not close the reader; the reverse proxy closes the body,
	// which is what triggers cost finalization.
	if err := r.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if finalized != len(out) {
		t.Fatalf("finalize got %d output bytes, want %d", finalized, len(out))
	}
}

func TestRehydrateReaderLeavesUnknownTokens(t *testing.T) {
	res := memResolver{m: map[string]string{}}
	src := &chunkReader{parts: []string{"[MASKED_EMAIL_ADDRESS_999]"}}
	r := newRehydrateReader(io.NopCloser(src), res, nil)
	out, _ := io.ReadAll(r)
	if string(out) != "[MASKED_EMAIL_ADDRESS_999]" {
		t.Fatalf("unknown token should stay visible, got %q", string(out))
	}
}

// ---- config + policy ----

func newTestConfigStore() *configStore {
	c := newConfigStore("does-not-exist.json")
	c.policy = rolePolicy{
		Default: roleRule{AllowEntities: []string{}},
		Roles: map[string]roleRule{
			"data-science": {AllowEntities: []string{"ZIP_CODE"}},
			"marketing":    {AllowEntities: []string{}},
		},
	}
	c.budgets = map[string]float64{"default": 100, "marketing": 500}
	return c
}

func TestConfigRolePolicyAndBudgets(t *testing.T) {
	c := newTestConfigStore()

	if !c.allowedFor([]string{"data-science"})["ZIP_CODE"] {
		t.Fatal("data-science should be allowed to send ZIP_CODE")
	}
	if c.allowedFor([]string{"marketing"})["ZIP_CODE"] {
		t.Fatal("marketing should NOT be allowed to send ZIP_CODE")
	}
	if got, ok := c.budgetFor("marketing"); !ok || got != 500 {
		t.Fatalf("marketing budget = %v/%v, want 500", got, ok)
	}
	if got, ok := c.budgetFor("unknown-team"); !ok || got != 100 {
		t.Fatalf("unknown team should fall back to default 100, got %v/%v", got, ok)
	}
}

func TestConfigStoreHotReloadsOnFileChange(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "custom-rules.json")

	write := func(body string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatalf("write config: %v", err)
		}
	}

	write(`{"rules":[{"entity_type":"A","patterns":["aaa"]}]}`)

	c := newConfigStore(path)
	c.load()
	if got := len(c.rulesSnapshot()); got != 1 {
		t.Fatalf("initial rules = %d, want 1", got)
	}

	c.watch()

	// A security engineer saves new rules; the gateway must pick them up
	// without a restart.
	write(`{"rules":[{"entity_type":"A","patterns":["aaa"]},{"entity_type":"B","patterns":["bbb"]}]}`)

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if len(c.rulesSnapshot()) == 2 {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("config did not hot-reload, rules = %d", len(c.rulesSnapshot()))
}

func TestPriceForExactPrefixAndFallback(t *testing.T) {
	c := newTestConfigStore()
	c.pricing = map[string]priceModel{
		"gpt-4o-mini":       {InputPer1K: 0.00015, OutputPer1K: 0.0006},
		"claude-3-5-sonnet": {InputPer1K: 0.003, OutputPer1K: 0.015},
		"default":           {InputPer1K: 0.0025, OutputPer1K: 0.01},
	}

	if p := c.priceFor("gpt-4o-mini"); p.InputPer1K != 0.00015 {
		t.Fatalf("exact match failed: %+v", p)
	}
	// Real requests use versioned IDs; the longest prefix should win.
	if p := c.priceFor("claude-3-5-sonnet-20241022"); p.OutputPer1K != 0.015 {
		t.Fatalf("prefix match failed: %+v", p)
	}
	if p := c.priceFor("mystery-model"); p.OutputPer1K != 0.01 {
		t.Fatalf("default fallback failed: %+v", p)
	}
}

func TestIdentityPolicyKeysAndTeam(t *testing.T) {
	id := identity{Subject: "alice", Role: "intern", Team: "Marketing", Groups: []string{"contractors"}}
	keys := id.policyKeys()
	if len(keys) != 3 || keys[0] != "intern" || keys[1] != "Marketing" {
		t.Fatalf("unexpected policy keys: %v", keys)
	}
	if id.budgetTeam() != "marketing" {
		t.Fatalf("budget team = %q, want marketing", id.budgetTeam())
	}
	if empty := (identity{}).policyKeys(); len(empty) != 1 || empty[0] != "anonymous" {
		t.Fatalf("anonymous keys = %v", empty)
	}
}

func TestParseJWTExtractsRoleTeamGroups(t *testing.T) {
	secret := []byte("test-secret")
	signed, err := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"sub":    "alice",
		"role":   "intern",
		"team":   "marketing",
		"groups": []any{"contractors", "eu"},
	}).SignedString(secret)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}

	id, err := parseJWT(signed, secret)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if id.Subject != "alice" || id.Role != "intern" || id.Team != "marketing" {
		t.Fatalf("unexpected identity %+v", id)
	}
	if len(id.Groups) != 2 || id.Groups[0] != "contractors" {
		t.Fatalf("unexpected groups %v", id.Groups)
	}

	if _, err := parseJWT(signed, []byte("wrong-secret")); err == nil {
		t.Fatal("expected signature verification to fail with wrong secret")
	}
}

func TestBearerToken(t *testing.T) {
	if got := bearerToken("Bearer abc.def.ghi"); got != "abc.def.ghi" {
		t.Fatalf("got %q", got)
	}
	if got := bearerToken("Basic abc"); got != "" {
		t.Fatalf("expected empty for non-bearer, got %q", got)
	}
}

// ---- integration tests through the real HTTP handler ----

// deadRedis returns a client that fails fast; the handler degrades gracefully
// (stats/audit calls log and continue) so tests need no running Redis.
func deadRedis() *redis.Client {
	return redis.NewClient(&redis.Options{
		Addr:         "127.0.0.1:0",
		DialTimeout:  25 * time.Millisecond,
		ReadTimeout:  25 * time.Millisecond,
		WriteTimeout: 25 * time.Millisecond,
		MaxRetries:   -1,
	})
}

func newTestGateway() *gateway {
	store := map[string]string{}
	g := &gateway{
		cfg:           newConfigStore("does-not-exist.json"),
		rdb:           deadRedis(),
		mockUpstream:  true,
		failurePolicy: policyPass,
		tokenTTL:      time.Hour,
		auditSize:     100,
		httpClient:    &http.Client{Timeout: time.Second},
	}
	g.sink = &memSink{m: store}
	g.resolver = memResolver{m: store}
	return g
}

func TestHandleChatMasksRequestAndRehydratesEcho(t *testing.T) {
	const pii = "jane@acme.com"
	content := "contact " + pii + " now"
	idx := strings.Index(content, pii)

	presidio := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprintf(w, `[{"entity_type":"EMAIL_ADDRESS","start":%d,"end":%d,"score":0.95}]`, idx, idx+len(pii))
	}))
	defer presidio.Close()

	g := newTestGateway()
	g.presidioURL = presidio.URL
	g.httpClient = presidio.Client()

	body := fmt.Sprintf(`{"model":"gpt-4o-mini","messages":[{"role":"user","content":%q}]}`, content)
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	rec := httptest.NewRecorder()

	g.handleChat(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var payload struct {
		Messages           []message `json:"messages"`
		RehydratedMessages []message `json:"rehydrated_messages"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !strings.Contains(payload.Messages[0].Content, "[MASKED_EMAIL_ADDRESS_") {
		t.Fatalf("upstream payload should be masked, got %q", payload.Messages[0].Content)
	}
	if strings.Contains(payload.Messages[0].Content, pii) {
		t.Fatalf("PII leaked into upstream payload: %q", payload.Messages[0].Content)
	}
	if payload.RehydratedMessages[0].Content != content {
		t.Fatalf("rehydrated echo = %q, want %q", payload.RehydratedMessages[0].Content, content)
	}
}

func TestModifyResponseRehydratesStreamingBody(t *testing.T) {
	g := newTestGateway()
	g.resolver = memResolver{m: map[string]string{"[MASKED_EMAIL_ADDRESS_1]": "jane@acme.com"}}

	pc := &proxyContext{team: "default", model: "gpt-4o-mini"}
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	req = req.WithContext(context.WithValue(req.Context(), proxyCtxKey, pc))

	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       io.NopCloser(strings.NewReader("data: {\"delta\":\"hi [MASKED_EMAIL_ADDRESS_1]\"}\n\n")),
		Request:    req,
	}

	if err := g.modifyResponse(resp); err != nil {
		t.Fatalf("modifyResponse: %v", err)
	}
	out, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	_ = resp.Body.Close()

	if !strings.Contains(string(out), "jane@acme.com") {
		t.Fatalf("expected rehydrated stream, got %q", string(out))
	}
}

func TestHandleChatFailsClosedWhenPresidioDown(t *testing.T) {
	g := newTestGateway()
	g.presidioURL = "http://127.0.0.1:0"
	g.failurePolicy = policyBlock

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"messages":[{"role":"user","content":"hello there"}]}`))
	rec := httptest.NewRecorder()

	g.handleChat(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("FAILURE_POLICY=BLOCK should return 503, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestHandleChatFailsOpenWhenPresidioDown(t *testing.T) {
	g := newTestGateway()
	g.presidioURL = "http://127.0.0.1:0"
	g.failurePolicy = policyPass

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"messages":[{"role":"user","content":"hello there"}]}`))
	rec := httptest.NewRecorder()

	g.handleChat(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("FAILURE_POLICY=PASS should return 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var payload struct {
		Messages []message `json:"messages"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if payload.Messages[0].Content != "hello there" {
		t.Fatalf("content should pass through unmasked, got %q", payload.Messages[0].Content)
	}
}

func TestBlockBudgetReturns429WithRequiredMessage(t *testing.T) {
	g := newTestGateway()
	rec := httptest.NewRecorder()

	g.blockBudget(rec, &proxyContext{team: "marketing"}, budgetState{
		Team: "marketing", Cap: 500, Spent: 500, HasCap: true, Exceeded: true,
	})

	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429", rec.Code)
	}
	var payload struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if payload.Error.Message != "AI Budget Exceeded. Contact your administrator." {
		t.Fatalf("message = %q", payload.Error.Message)
	}
}

// ---- Redis-backed integration (in-process Redis, no Docker) ----

func TestBudgetGateBlocksAtCapWithRedis(t *testing.T) {
	mr := miniredis.RunT(t)
	g := newTestGateway()
	g.rdb = redis.NewClient(&redis.Options{Addr: mr.Addr()})
	g.allowRoleHeader = true
	g.cfg = newTestConfigStore() // marketing cap = 500

	// Pretend marketing already burned through its monthly budget.
	month := time.Now().UTC().Format("2006-01")
	mr.Set("budget:marketing:"+month, "500")

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("X-Guardian-Team", "marketing")
	rec := httptest.NewRecorder()

	g.handleChat(rec, req)

	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("expected 429 at cap, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "AI Budget Exceeded. Contact your administrator.") {
		t.Fatalf("missing budget message: %s", rec.Body.String())
	}
}

func TestRequestStoresTokenAuditAndStatsWithRedis(t *testing.T) {
	mr := miniredis.RunT(t)
	const pii = "jane@acme.com"
	content := "contact " + pii + " now"
	idx := strings.Index(content, pii)

	presidio := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprintf(w, `[{"entity_type":"EMAIL_ADDRESS","start":%d,"end":%d,"score":0.95}]`, idx, idx+len(pii))
	}))
	defer presidio.Close()

	g := newTestGateway()
	g.rdb = redis.NewClient(&redis.Options{Addr: mr.Addr()})
	g.sink = redisSink{rdb: g.rdb, ttl: time.Hour}
	g.resolver = redisResolver{rdb: g.rdb}
	g.presidioURL = presidio.URL
	g.httpClient = presidio.Client()

	body := fmt.Sprintf(`{"model":"gpt-4o-mini","messages":[{"role":"user","content":%q}]}`, content)
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	rec := httptest.NewRecorder()

	g.handleChat(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}

	// The token must be resolvable under the tok: prefix the dashboard uses.
	var tokenKey string
	for _, key := range mr.Keys() {
		if strings.HasPrefix(key, "tok:") {
			tokenKey = key
		}
	}
	if tokenKey == "" {
		t.Fatal("expected a token stored in Redis")
	}
	if got, err := mr.Get(tokenKey); err != nil || got != pii {
		t.Fatalf("token value = %q (%v), want %q", got, err, pii)
	}

	// Stats counters.
	if got, _ := mr.Get("stats:leaks_blocked"); got != "1" {
		t.Fatalf("leaks_blocked = %q, want 1", got)
	}
	if got, _ := mr.Get("stats:total_requests"); got != "1" {
		t.Fatalf("total_requests = %q, want 1", got)
	}

	// Audit log entry references the masked token only.
	entries, err := mr.List("audit:log")
	if err != nil || len(entries) != 1 {
		t.Fatalf("audit:log len = %d (%v), want 1", len(entries), err)
	}
	if strings.Contains(entries[0], pii) {
		t.Fatalf("audit entry must not contain the original PII: %s", entries[0])
	}
	if !strings.Contains(entries[0], "MASKED_EMAIL_ADDRESS_") {
		t.Fatalf("audit entry should reference masked token: %s", entries[0])
	}
}
