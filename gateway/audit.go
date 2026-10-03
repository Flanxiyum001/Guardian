package main

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"
)

// auditEntry is one row of the dashboard's leak-audit table. It deliberately
// stores only masked tokens and a masked preview, never the original values.
type auditEntry struct {
	ID       string   `json:"id"`
	Ts       int64    `json:"ts"`
	User     string   `json:"user"`
	Role     string   `json:"role"`
	Team     string   `json:"team"`
	IP       string   `json:"ip"`
	Service  string   `json:"service"`
	Model    string   `json:"model"`
	Status   string   `json:"status"`
	Entities []string `json:"entities"`
	Tokens   []string `json:"tokens"`
	Preview  string   `json:"preview"`
}

type alertEntry struct {
	Ts      int64  `json:"ts"`
	Level   string `json:"level"`
	Message string `json:"message"`
}

func (g *gateway) pushAudit(pc *proxyContext, status, preview string) {
	entry := auditEntry{
		ID:       fmt.Sprintf("%d", time.Now().UnixNano()),
		Ts:       time.Now().Unix(),
		User:     pc.user,
		Role:     pc.identity.Role,
		Team:     pc.team,
		IP:       pc.ip,
		Service:  pc.service,
		Model:    pc.model,
		Status:   status,
		Entities: pc.entities,
		Tokens:   pc.tokens,
		Preview:  preview,
	}
	blob, err := json.Marshal(entry)
	if err != nil {
		return
	}
	pipe := g.rdb.Pipeline()
	pipe.LPush(ctx, "audit:log", blob)
	pipe.LTrim(ctx, "audit:log", 0, int64(g.auditSize-1))
	if _, err := pipe.Exec(ctx); err != nil {
		logf("redis audit push failed: %v", err)
	}
}

// pushAlert surfaces operational events (Presidio outages, budget breaches) to
// the dashboard.
func (g *gateway) pushAlert(level, message string) {
	blob, err := json.Marshal(alertEntry{Ts: time.Now().Unix(), Level: level, Message: message})
	if err != nil {
		return
	}
	pipe := g.rdb.Pipeline()
	pipe.LPush(ctx, "alerts", blob)
	pipe.LTrim(ctx, "alerts", 0, 199)
	if _, err := pipe.Exec(ctx); err != nil {
		logf("redis alert push failed: %v", err)
	}
}

func clientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		return strings.TrimSpace(strings.Split(xff, ",")[0])
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "…"
}
