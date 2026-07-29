// Package registry provides access to the skills.sh skill directory.
package registry

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/sleuth-io/sx/v2/internal/buildinfo"
)

const (
	skillsShAPIBase = "https://skills.sh"
	// browseQuery is a short query that broadly matches most skills, returning
	// top results sorted by installs. This mirrors the approach used by the
	// skills.sh application itself (see catalog_providers.py).
	browseQuery = "sk"
)

// Skill represents a skill from the skills.sh directory.
type Skill struct {
	Source   string // e.g., "anthropics/skills"
	SkillID  string // e.g., "frontend-design"
	Name     string // e.g., "frontend-design"
	Installs int    // e.g., 123897
}

// TreeURL returns the GitHub tree URL for this skill.
func (s Skill) TreeURL(branch string) string {
	return fmt.Sprintf("https://github.com/%s/tree/%s/skills/%s", s.Source, branch, s.SkillID)
}

// FormatInstalls returns a human-readable install count (e.g., "123.9K", "1.2M").
func (s Skill) FormatInstalls() string {
	if s.Installs >= 1_000_000 {
		return fmt.Sprintf("%.1fM", float64(s.Installs)/1_000_000)
	}
	if s.Installs >= 1_000 {
		return fmt.Sprintf("%.1fK", float64(s.Installs)/1_000)
	}
	return strconv.Itoa(s.Installs)
}

// FormatCount returns a human-readable count (e.g., "85.7K", "1.2M").
func FormatCount(n int) string {
	if n >= 1_000_000 {
		return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
	}
	if n >= 1_000 {
		return fmt.Sprintf("%.1fK", float64(n)/1_000)
	}
	return strconv.Itoa(n)
}

// FetchTopSkills fetches the most popular skills from skills.sh via the search API.
func FetchTopSkills(ctx context.Context, limit int) ([]Skill, error) {
	return SearchSkills(ctx, browseQuery, limit)
}

// SearchSkills searches the skills.sh directory via the API.
// The API requires a minimum 2-character query and supports a configurable limit.
func SearchSkills(ctx context.Context, query string, limit int) ([]Skill, error) {
	if len(query) < 2 {
		return nil, errors.New("search query must be at least 2 characters")
	}
	if limit <= 0 {
		limit = 20
	}

	apiURL := fmt.Sprintf("%s/api/search?q=%s&limit=%d",
		skillsShAPIBase, url.QueryEscape(query), limit)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", buildinfo.GetUserAgent())

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to search skills.sh: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("skills.sh search returned HTTP %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read search response: %w", err)
	}

	return ParseSearchResponse(body)
}

// searchResponse is the JSON structure returned by the skills.sh search API.
type searchResponse struct {
	Skills []searchSkill `json:"skills"`
	Error  string        `json:"error,omitempty"`
}

type searchSkill struct {
	SkillID  string `json:"skillId"`
	Name     string `json:"name"`
	Source   string `json:"source"`
	Installs int    `json:"installs"`
}

// ParseSearchResponse parses the JSON response from the skills.sh search API.
func ParseSearchResponse(body []byte) ([]Skill, error) {
	var resp searchResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("failed to parse search response: %w", err)
	}
	if resp.Error != "" {
		return nil, fmt.Errorf("skills.sh search error: %s", resp.Error)
	}

	skills := make([]Skill, 0, len(resp.Skills))
	for _, s := range resp.Skills {
		skills = append(skills, Skill{
			Source:   s.Source,
			SkillID:  s.SkillID,
			Name:     s.Name,
			Installs: s.Installs,
		})
	}
	return skills, nil
}

// Search filters skills locally by a query string, matching against name and source.
// Prefer SearchSkills for server-side search across the full directory.
func Search(skills []Skill, query string) []Skill {
	if query == "" {
		return skills
	}
	query = strings.ToLower(query)
	var results []Skill
	for _, s := range skills {
		if strings.Contains(strings.ToLower(s.Name), query) ||
			strings.Contains(strings.ToLower(s.Source), query) {
			results = append(results, s)
		}
	}
	return results
}
