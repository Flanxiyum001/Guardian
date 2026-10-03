package main

import (
	"log"
	"net/http"
	"strings"

	"github.com/golang-jwt/jwt/v5"
)

// identity describes who sent the request, used for role-based masking and
// per-team budgets.
type identity struct {
	Subject string
	Role    string
	Team    string
	Groups  []string
}

// policyKeys returns the config keys to look up, most specific first.
func (id identity) policyKeys() []string {
	var keys []string
	if id.Role != "" {
		keys = append(keys, id.Role)
	}
	if id.Team != "" {
		keys = append(keys, id.Team)
	}
	keys = append(keys, id.Groups...)
	if len(keys) == 0 {
		keys = append(keys, "anonymous")
	}
	return keys
}

// budgetTeam picks the team whose monthly cap applies.
func (id identity) budgetTeam() string {
	if id.Team != "" {
		return strings.ToLower(id.Team)
	}
	if id.Role != "" {
		return strings.ToLower(id.Role)
	}
	return "default"
}

func (id identity) displayName() string {
	switch {
	case id.Subject != "":
		return id.Subject
	case id.Role != "":
		return id.Role
	case id.Team != "":
		return id.Team
	default:
		return "anonymous"
	}
}

// identityFromRequest extracts the caller identity from a signed JWT in the
// Authorization header. An optional, explicitly-enabled role header is honoured
// for local testing (never enable it in production).
func (g *gateway) identityFromRequest(r *http.Request) identity {
	if token := bearerToken(r.Header.Get("Authorization")); token != "" && len(g.jwtSecret) > 0 {
		id, err := parseJWT(token, g.jwtSecret)
		if err != nil {
			log.Printf("jwt rejected: %v", err)
		} else {
			return id
		}
	}

	if g.allowRoleHeader {
		return identity{
			Role: r.Header.Get("X-Guardian-Role"),
			Team: r.Header.Get("X-Guardian-Team"),
		}
	}
	return identity{}
}

func bearerToken(header string) string {
	const prefix = "bearer "
	if len(header) > len(prefix) && strings.EqualFold(header[:len(prefix)], prefix) {
		return strings.TrimSpace(header[len(prefix):])
	}
	return ""
}

func parseJWT(tokenString string, secret []byte) (identity, error) {
	claims := jwt.MapClaims{}
	_, err := jwt.ParseWithClaims(tokenString, claims, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, jwt.ErrTokenSignatureInvalid
		}
		return secret, nil
	}, jwt.WithValidMethods([]string{"HS256", "HS384", "HS512"}))
	if err != nil {
		return identity{}, err
	}

	id := identity{}
	if v, ok := claims["sub"].(string); ok {
		id.Subject = v
	}
	if v, ok := claims["role"].(string); ok {
		id.Role = v
	}
	if v, ok := claims["team"].(string); ok {
		id.Team = v
	}
	switch groups := claims["groups"].(type) {
	case []any:
		for _, gr := range groups {
			if s, ok := gr.(string); ok && s != "" {
				id.Groups = append(id.Groups, s)
			}
		}
	case string:
		if groups != "" {
			id.Groups = strings.Split(groups, ",")
		}
	}
	return id, nil
}
