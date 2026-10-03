package main

import (
	"strings"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

// pointAtDeadRedis gives the tokenizer a client that always fails, so the tests
// exercise the masking logic without needing a running Redis.
func pointAtDeadRedis() {
	rdb = redis.NewClient(&redis.Options{
		Addr:         "127.0.0.1:0",
		DialTimeout:  50 * time.Millisecond,
		ReadTimeout:  50 * time.Millisecond,
		WriteTimeout: 50 * time.Millisecond,
		MaxRetries:   -1,
	})
}

func TestTokenizeMasksMultipleFindings(t *testing.T) {
	pointAtDeadRedis()

	text := "call 555-0199 or test@company.com now"
	findings := []presidioFinding{
		{EntityType: "PHONE_NUMBER", Start: 5, End: 13, Score: 0.95},
		{EntityType: "EMAIL_ADDRESS", Start: 17, End: 33, Score: 0.90},
	}

	got := tokenize(text, findings)

	if strings.Contains(got, "555-0199") || strings.Contains(got, "test@company.com") {
		t.Fatalf("expected PII to be masked, got %q", got)
	}
	if !strings.Contains(got, "[MASKED_PHONE_NUMBER_") || !strings.Contains(got, "[MASKED_EMAIL_ADDRESS_") {
		t.Fatalf("expected both entity types to be tokenized, got %q", got)
	}
	if !strings.HasPrefix(got, "call ") || !strings.HasSuffix(got, " now") {
		t.Fatalf("expected surrounding text preserved, got %q", got)
	}
}

func TestTokenizeSkipsLowScoreOverlapAndOutOfRange(t *testing.T) {
	pointAtDeadRedis()

	text := "abc123"
	findings := []presidioFinding{
		{EntityType: "LOW", Start: 0, End: 3, Score: 0.30},    // below threshold
		{EntityType: "WIN", Start: 0, End: 6, Score: 0.90},    // masks the whole string
		{EntityType: "OVERLAP", Start: 3, End: 6, Score: 0.9}, // overlaps WIN
		{EntityType: "OOR", Start: 2, End: 99, Score: 0.90},   // out of range
	}

	got := tokenize(text, findings)

	if n := strings.Count(got, "[MASKED_"); n != 1 {
		t.Fatalf("expected exactly one mask, got %d in %q", n, got)
	}
	if !strings.Contains(got, "[MASKED_WIN_") {
		t.Fatalf("expected the widest span to win, got %q", got)
	}
}
