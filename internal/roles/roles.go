// Package roles reads the VirtualBoard agent charters from
// `.virtualboard/agents/` and picks the right one for a feature.
//
// A role is what herdr-board calls a "system prompt": the charter file is
// prepended to the dispatch prompt so the agent adopts the role VirtualBoard
// expects it to announce.
package roles

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/virtualboard/herdr-virtualboard/internal/feature"
	"gopkg.in/yaml.v3"
)

// Role is one VirtualBoard agent charter.
type Role struct {
	// Key is the charter's file stem (`backend_dev`). It is what the user
	// types and what `HVB_ROLE` carries.
	Key string `json:"key"`
	// Name is the `name:` frontmatter field (`backend-dev`), which is the
	// spelling the Claude Code subagent of the same charter registers under.
	Name string `json:"name"`
	// Description is the `description:` frontmatter field.
	Description string `json:"description"`
	// Path is the absolute charter path.
	Path string `json:"path"`
}

// notRoles are the files in `agents/` that describe the system rather than a
// role an agent can adopt.
var notRoles = map[string]bool{"AGENTS": true, "RULES": true, "README": true}

// Load reads every charter in the workspace's agents directory, sorted by key.
// A workspace with no agents directory yields no roles and no error: dispatch
// then falls back to a role-less prompt rather than refusing to run.
func Load(agentsDir string) ([]Role, error) {
	entries, err := os.ReadDir(agentsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read agents directory: %w", err)
	}
	var out []Role
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".md") {
			continue
		}
		key := strings.TrimSuffix(name, ".md")
		if notRoles[key] {
			continue
		}
		path := filepath.Join(agentsDir, name)
		role := Role{Key: key, Name: key, Path: path}
		if header, err := readFrontmatter(path); err == nil {
			if header.Name != "" {
				role.Name = header.Name
			}
			role.Description = header.Description
		}
		out = append(out, role)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out, nil
}

// Charter returns the full markdown of the role's charter file.
func (r Role) Charter() (string, error) {
	raw, err := os.ReadFile(r.Path)
	if err != nil {
		return "", fmt.Errorf("read charter %s: %w", r.Path, err)
	}
	return string(raw), nil
}

// Find resolves a role by key or by frontmatter name, case-insensitively and
// tolerating the `-`/`_` spelling difference between the two.
func Find(all []Role, want string) (Role, bool) {
	normalized := normalize(want)
	for _, role := range all {
		if normalize(role.Key) == normalized || normalize(role.Name) == normalized {
			return role, true
		}
	}
	return Role{}, false
}

// labelHints maps a feature label to the role key it suggests. The mapping
// follows the "Role Selection Guidelines" table in `.virtualboard/agents/AGENTS.md`;
// it is a default, and any per-project config or explicit `--role` overrides it.
var labelHints = []struct {
	labels []string
	role   string
}{
	{[]string{"frontend", "ui", "ux-implementation", "component", "css"}, "frontend_dev"},
	{[]string{"backend", "api", "database", "auth", "server"}, "backend_dev"},
	{[]string{"fullstack", "feature", "end-to-end"}, "fullstack_dev"},
	{[]string{"infra", "infrastructure", "devops", "ci", "cd", "cicd", "deploy", "deployment", "observability"}, "devops_engineer"},
	{[]string{"security", "compliance", "privacy", "threat-model"}, "security_compliance_engineer"},
	{[]string{"data", "analytics", "metrics", "telemetry", "reporting", "etl"}, "data_analytics_engineer"},
	{[]string{"design", "ux", "wireframe", "prototype", "design-system"}, "ux_product_designer"},
	{[]string{"architecture", "adr", "tech-debt", "standards"}, "architect"},
	{[]string{"qa", "test", "testing", "regression", "e2e"}, "qa"},
	{[]string{"planning", "coordination", "sprint", "roadmap"}, "pm"},
}

// Suggest picks the role that best fits a feature, given the roles the
// workspace actually ships.
//
// Precedence: the explicit role field, then a legacy `role:` label, then QA for
// review, then the first matching label hint, then fallback. The result's
// second value is false when no role could be resolved at all.
func Suggest(all []Role, spec *feature.Spec, fallback string) (Role, bool) {
	if len(all) == 0 {
		return Role{}, false
	}
	if spec != nil {
		if spec.Role != "" {
			if role, found := Find(all, spec.Role); found {
				return role, true
			}
		}
		for _, label := range spec.Labels {
			if rest, ok := strings.CutPrefix(label, "role:"); ok {
				if role, found := Find(all, rest); found {
					return role, true
				}
			}
		}
		if spec.Status == feature.Review {
			if role, found := Find(all, "qa"); found {
				return role, true
			}
		}
		for _, label := range spec.Labels {
			for _, hint := range labelHints {
				for _, candidate := range hint.labels {
					if normalize(label) != normalize(candidate) {
						continue
					}
					if role, found := Find(all, hint.role); found {
						return role, true
					}
				}
			}
		}
	}
	if fallback != "" {
		if role, found := Find(all, fallback); found {
			return role, true
		}
	}
	if role, found := Find(all, "fullstack_dev"); found {
		return role, true
	}
	return all[0], true
}

type frontmatter struct {
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
}

func readFrontmatter(path string) (frontmatter, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return frontmatter{}, err
	}
	text := strings.ReplaceAll(string(raw), "\r\n", "\n")
	if !strings.HasPrefix(text, "---\n") {
		return frontmatter{}, fmt.Errorf("%s: no frontmatter", path)
	}
	rest := text[len("---\n"):]
	end := strings.Index(rest, "\n---")
	if end < 0 {
		return frontmatter{}, fmt.Errorf("%s: unterminated frontmatter", path)
	}
	var header frontmatter
	if err := yaml.Unmarshal([]byte(rest[:end+1]), &header); err != nil {
		return frontmatter{}, err
	}
	return header, nil
}

func normalize(s string) string {
	return strings.ReplaceAll(strings.ToLower(strings.TrimSpace(s)), "-", "_")
}
