package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"
)

// span is a detected sensitive region of text.
type span struct {
	Start      int
	End        int
	EntityType string
	Score      float64
	Source     string // "presidio" | "custom"
}

type presidioRequest struct {
	Text     string `json:"text"`
	Language string `json:"language"`
}

// presidioFinding mirrors the Presidio /analyze response item.
type presidioFinding struct {
	Index      int     `json:"index"`
	EntityType string  `json:"entity_type"`
	Start      int     `json:"start"`
	End        int     `json:"end"`
	Score      float64 `json:"score"`
}

var errPresidio = errors.New("presidio unavailable")

// detectSpans combines local enterprise-dictionary matches with Presidio's PII
// findings. A Presidio failure is returned separately so the caller can apply
// the configured fail-open / fail-closed policy while still masking local rules.
func (g *gateway) detectSpans(text string, cfg *configStore) ([]span, error) {
	spans := customSpans(text, cfg.rulesSnapshot())

	presidioSpans, err := g.analyzeWithPresidio(text)
	if err != nil {
		return spans, err
	}
	return append(spans, presidioSpans...), nil
}

// customSpans runs the enterprise dictionary regexes locally.
func customSpans(text string, rules []compiledRule) []span {
	var spans []span
	for _, rule := range rules {
		for _, re := range rule.Patterns {
			for _, loc := range re.FindAllStringIndex(text, -1) {
				if loc[0] == loc[1] {
					continue
				}
				spans = append(spans, span{
					Start:      loc[0],
					End:        loc[1],
					EntityType: rule.EntityType,
					Score:      rule.Score,
					Source:     "custom",
				})
			}
		}
	}
	return spans
}

func (g *gateway) analyzeWithPresidio(text string) ([]span, error) {
	payload, err := json.Marshal(presidioRequest{Text: text, Language: "en"})
	if err != nil {
		return nil, fmt.Errorf("encode presidio request: %w", err)
	}

	resp, err := g.httpClient.Post(g.presidioURL+"/analyze", "application/json", bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("%w: %v", errPresidio, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%w: status %d", errPresidio, resp.StatusCode)
	}

	var findings []presidioFinding
	if err := json.NewDecoder(resp.Body).Decode(&findings); err != nil {
		return nil, fmt.Errorf("%w: decode: %v", errPresidio, err)
	}

	spans := make([]span, 0, len(findings))
	for _, f := range findings {
		if f.Start >= 0 && f.End > f.Start {
			spans = append(spans, span{
				Start:      f.Start,
				End:        f.End,
				EntityType: f.EntityType,
				Score:      f.Score,
				Source:     "presidio",
			})
		}
	}
	return spans, nil
}

func (g *gateway) presidioHealthy() bool {
	req, err := http.NewRequest(http.MethodGet, g.presidioURL+"/health", nil)
	if err != nil {
		return false
	}
	c := &http.Client{Timeout: 2 * time.Second}
	resp, err := c.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}
