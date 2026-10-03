package main

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
)

// ---- on-disk schema (config/custom-rules.json) ----

// ruleConfig is one enterprise dictionary rule. Each pattern is a Go/RE2 regex
// so plain codenames (escaped) and structured patterns both work.
type ruleConfig struct {
	ID          string   `json:"id"`
	EntityType  string   `json:"entity_type"`
	Description string   `json:"description"`
	Score       float64  `json:"score"`
	Patterns    []string `json:"patterns"`
}

// roleRule lists entity types a role/team is allowed to send unmasked.
type roleRule struct {
	AllowEntities []string `json:"allow_entities"`
}

type rolePolicy struct {
	Default roleRule            `json:"default"`
	Roles   map[string]roleRule `json:"roles"`
}

type priceModel struct {
	InputPer1K  float64 `json:"input_per_1k"`
	OutputPer1K float64 `json:"output_per_1k"`
}

type appConfig struct {
	Version    int                   `json:"version"`
	Rules      []ruleConfig          `json:"rules"`
	RolePolicy rolePolicy            `json:"role_policy"`
	Budgets    map[string]float64    `json:"budgets"`
	Pricing    map[string]priceModel `json:"pricing"`
}

// ---- compiled runtime form ----

type compiledRule struct {
	EntityType string
	Score      float64
	Patterns   []*regexp.Regexp
}

type configStore struct {
	path string

	mu      sync.RWMutex
	rules   []compiledRule
	policy  rolePolicy
	budgets map[string]float64
	pricing map[string]priceModel

	// publish is called after every successful (re)load, e.g. to mirror the
	// budgets to Redis for the dashboard.
	publish func(*appConfig)
}

func defaultPricing() map[string]priceModel {
	return map[string]priceModel{
		// OpenAI (USD per 1K tokens)
		"gpt-4o":      {InputPer1K: 0.0025, OutputPer1K: 0.01},
		"gpt-4o-mini": {InputPer1K: 0.00015, OutputPer1K: 0.0006},
		// Anthropic Claude (USD per 1K tokens)
		"claude-3-5-sonnet": {InputPer1K: 0.003, OutputPer1K: 0.015},
		"claude-3-5-haiku":  {InputPer1K: 0.0008, OutputPer1K: 0.004},
		"claude-3-opus":     {InputPer1K: 0.015, OutputPer1K: 0.075},
		"default":           {InputPer1K: 0.0025, OutputPer1K: 0.01},
	}
}

func newConfigStore(path string) *configStore {
	return &configStore{
		path:    path,
		policy:  rolePolicy{Default: roleRule{AllowEntities: []string{}}},
		budgets: map[string]float64{},
		pricing: defaultPricing(),
	}
}

// load reads and compiles the config file. A missing file is not fatal: the
// gateway keeps its safe defaults (mask everything, no budgets).
func (c *configStore) load() {
	raw, err := os.ReadFile(c.path)
	if err != nil {
		if !os.IsNotExist(err) {
			log.Printf("config read failed (%s): %v", c.path, err)
		}
		return
	}

	var cfg appConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		log.Printf("config parse failed (%s): %v", c.path, err)
		return
	}

	rules := make([]compiledRule, 0, len(cfg.Rules))
	for _, r := range cfg.Rules {
		if r.EntityType == "" {
			log.Printf("config rule %q skipped: missing entity_type", r.ID)
			continue
		}
		cr := compiledRule{EntityType: r.EntityType, Score: r.Score}
		if cr.Score <= 0 {
			cr.Score = 0.85
		}
		for _, p := range r.Patterns {
			re, err := regexp.Compile(p)
			if err != nil {
				log.Printf("config rule %q pattern %q invalid: %v", r.ID, p, err)
				continue
			}
			cr.Patterns = append(cr.Patterns, re)
		}
		if len(cr.Patterns) > 0 {
			rules = append(rules, cr)
		}
	}

	pricing := cfg.Pricing
	if len(pricing) == 0 {
		pricing = defaultPricing()
	}

	c.mu.Lock()
	c.rules = rules
	c.policy = cfg.RolePolicy
	if c.policy.Roles == nil {
		c.policy.Roles = map[string]roleRule{}
	}
	c.budgets = cfg.Budgets
	if c.budgets == nil {
		c.budgets = map[string]float64{}
	}
	c.pricing = pricing
	c.mu.Unlock()

	log.Printf("config loaded: %d custom rule(s), %d role(s), %d budget(s)",
		len(rules), len(c.policy.Roles), len(c.budgets))
	if c.publish != nil {
		c.publish(&cfg)
	}
}

func (c *configStore) rulesSnapshot() []compiledRule {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.rules
}

// allowedFor unions the default allow-list with the allow-lists of every policy
// key the identity matches (role, team, groups).
func (c *configStore) allowedFor(keys []string) map[string]bool {
	c.mu.RLock()
	defer c.mu.RUnlock()

	allowed := make(map[string]bool)
	add := func(list []string) {
		for _, e := range list {
			allowed[strings.ToUpper(e)] = true
		}
	}
	add(c.policy.Default.AllowEntities)
	for _, k := range keys {
		if r, ok := c.policy.Roles[strings.ToLower(k)]; ok {
			add(r.AllowEntities)
		}
	}
	return allowed
}

func (c *configStore) budgetFor(team string) (float64, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if team == "" {
		team = "default"
	}
	cap, ok := c.budgets[team]
	if !ok {
		cap, ok = c.budgets["default"]
	}
	return cap, ok
}

func (c *configStore) priceFor(model string) priceModel {
	c.mu.RLock()
	defer c.mu.RUnlock()

	if p, ok := c.pricing[model]; ok {
		return p
	}

	// Real requests use versioned IDs (e.g. "claude-3-5-sonnet-20241022"), so
	// fall back to the longest configured key the model name starts with.
	best := ""
	for key := range c.pricing {
		if key == "default" {
			continue
		}
		if strings.HasPrefix(model, key) && len(key) > len(best) {
			best = key
		}
	}
	if best != "" {
		return c.pricing[best]
	}

	if p, ok := c.pricing["default"]; ok {
		return p
	}
	return priceModel{InputPer1K: 0.0025, OutputPer1K: 0.01}
}

// watch reloads the config whenever the file changes. It watches the containing
// directory (not the file) so editors that replace the file via rename are
// detected too. Works with a bind-mounted config directory in Docker.
func (c *configStore) watch() {
	w, err := fsnotify.NewWatcher()
	if err != nil {
		log.Printf("config watcher unavailable: %v", err)
		return
	}
	dir := filepath.Dir(c.path)
	if err := w.Add(dir); err != nil {
		log.Printf("config watcher cannot watch %s: %v", dir, err)
		_ = w.Close()
		return
	}

	target := filepath.Base(c.path)
	go func() {
		defer w.Close()
		var timer *time.Timer
		for {
			select {
			case ev, ok := <-w.Events:
				if !ok {
					return
				}
				if filepath.Base(ev.Name) != target {
					continue
				}
				if timer != nil {
					timer.Stop()
				}
				// Debounce: editors emit several events per save.
				timer = time.AfterFunc(250*time.Millisecond, c.load)
			case err, ok := <-w.Errors:
				if !ok {
					return
				}
				log.Printf("config watcher error: %v", err)
			}
		}
	}()
	log.Printf("watching %s for live config updates", c.path)
}
