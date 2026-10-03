package main

import (
	"fmt"
	"log"
	"sort"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

// tokenSink persists the mapping from an opaque token back to the real value.
type tokenSink interface {
	Store(token, value string)
}

// tokenResolver looks a token up again while rehydrating a response.
type tokenResolver interface {
	Resolve(token string) (string, bool)
}

// redisSink is the production token store.
type redisSink struct {
	rdb *redis.Client
	ttl time.Duration
}

func (s redisSink) Store(token, value string) {
	if err := s.rdb.Set(ctx, tokenKey(token), value, s.ttl).Err(); err != nil {
		log.Printf("redis set token failed: %v", err)
		return
	}
	if err := s.rdb.Incr(ctx, "stats:leaks_blocked").Err(); err != nil {
		log.Printf("redis incr leaks_blocked failed: %v", err)
	}
}

type redisResolver struct{ rdb *redis.Client }

func (r redisResolver) Resolve(token string) (string, bool) {
	value, err := r.rdb.Get(ctx, tokenKey(token)).Result()
	if err != nil {
		return "", false
	}
	return value, true
}

func tokenKey(token string) string { return "tok:" + token }

// maskResult reports what a single text field was masked into.
type maskResult struct {
	Masked   string
	Tokens   []string
	Entities []string
}

// maskText replaces every detection with a fresh token, skipping entity types
// the caller is allowed to send. Tokens are persisted through the sink.
func maskText(text string, spans []span, allowed map[string]bool, sink tokenSink) maskResult {
	if text == "" || len(spans) == 0 {
		return maskResult{Masked: text}
	}

	valid := make([]span, 0, len(spans))
	for _, s := range spans {
		if s.Score < minScore || s.Start < 0 || s.End > len(text) || s.Start >= s.End {
			continue
		}
		if allowed[strings.ToUpper(s.EntityType)] {
			continue // this role/team is permitted to send this entity
		}
		valid = append(valid, s)
	}
	if len(valid) == 0 {
		return maskResult{Masked: text}
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
	res := maskResult{}

	for _, s := range valid {
		if s.Start < cursor {
			continue // overlaps a span already masked
		}
		out.WriteString(text[cursor:s.Start])

		value := text[s.Start:s.End]
		token := fmt.Sprintf("[MASKED_%s_%d]", s.EntityType, time.Now().UnixNano())
		sink.Store(token, value)

		res.Tokens = append(res.Tokens, token)
		res.Entities = append(res.Entities, s.EntityType)
		out.WriteString(token)
		cursor = s.End
	}
	out.WriteString(text[cursor:])
	res.Masked = out.String()
	return res
}
