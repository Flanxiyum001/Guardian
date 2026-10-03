package main

import (
	"encoding/json"
	"sync"
	"time"
)

type usageInfo struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
}

type usageEnvelope struct {
	Usage *usageInfo `json:"usage"`
}

// estimateTokens approximates a token count from a byte length (~4 bytes/token).
func estimateTokens(n int) int {
	if n <= 0 {
		return 0
	}
	return (n + 3) / 4
}

type budgetState struct {
	Team     string
	Cap      float64
	Spent    float64
	HasCap   bool
	Exceeded bool
}

func (g *gateway) budgetKey(team string) string {
	return "budget:" + team + ":" + time.Now().UTC().Format("2006-01")
}

func (g *gateway) budgetStatus(team string) budgetState {
	st := budgetState{Team: team}
	cap, hasCap := g.cfg.budgetFor(team)
	st.Cap = cap
	st.HasCap = hasCap && cap > 0
	if v, err := g.rdb.Get(ctx, g.budgetKey(team)).Float64(); err == nil {
		st.Spent = v
	}
	st.Exceeded = st.HasCap && st.Spent >= st.Cap
	return st
}

// recordCost converts usage into dollars and adds it to the team's monthly
// spend counter. It returns the cost of this request.
func (g *gateway) recordCost(team, model string, promptTokens, completionTokens int) float64 {
	price := g.cfg.priceFor(model)
	cost := float64(promptTokens)/1000*price.InputPer1K + float64(completionTokens)/1000*price.OutputPer1K

	if err := g.rdb.IncrByFloat(ctx, g.budgetKey(team), cost).Err(); err != nil {
		logf("redis incr budget failed: %v", err)
	}
	if err := g.rdb.IncrByFloat(ctx, "stats:cost_usd_total", cost).Err(); err != nil {
		logf("redis incr total cost failed: %v", err)
	}
	return cost
}

func parseUsage(raw []byte, pc *proxyContext) {
	var env usageEnvelope
	if err := json.Unmarshal(raw, &env); err == nil && env.Usage != nil {
		pc.usage = env.Usage
	}
}

// proxyContext carries per-request state from the handler into ModifyResponse
// (which runs on the shared reverse proxy) via the request context.
type proxyContext struct {
	identity    identity
	model       string
	team        string
	service     string
	ip          string
	user        string
	promptChars int
	entities    []string
	tokens      []string
	usage       *usageInfo
	startedAt   time.Time

	once sync.Once
}

// finalize records cost exactly once, preferring real usage data when the
// upstream returned it and falling back to an estimate from the body size.
func (pc *proxyContext) finalize(g *gateway, outputBytes int) {
	pc.once.Do(func() {
		promptTokens := estimateTokens(pc.promptChars)
		completionTokens := 0
		if pc.usage != nil {
			if pc.usage.PromptTokens > 0 {
				promptTokens = pc.usage.PromptTokens
			}
			completionTokens = pc.usage.CompletionTokens
		}
		if completionTokens == 0 {
			completionTokens = estimateTokens(outputBytes)
		}
		cost := g.recordCost(pc.team, pc.model, promptTokens, completionTokens)
		logf("request by %s (%s) model=%s entities=%d cost=$%.5f",
			pc.user, pc.team, pc.model, len(pc.entities), cost)
	})
}
