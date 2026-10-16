package feature

import (
	"bufio"
	"fmt"
	"os"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Frontmatter is the YAML header of a feature spec. The fields and their
// constraints come from `.virtualboard/schemas/frontmatter.schema.json`;
// `additionalProperties` is false there, so this struct is the whole surface.
type Frontmatter struct {
	ID           string   `yaml:"id" json:"id"`
	Title        string   `yaml:"title" json:"title"`
	Status       Status   `yaml:"status" json:"status"`
	Owner        string   `yaml:"owner,omitempty" json:"owner,omitempty"`
	Priority     string   `yaml:"priority,omitempty" json:"priority,omitempty"`
	Complexity   string   `yaml:"complexity,omitempty" json:"complexity,omitempty"`
	Created      string   `yaml:"created" json:"created"`
	Updated      string   `yaml:"updated" json:"updated"`
	Labels       []string `yaml:"labels,omitempty" json:"labels,omitempty"`
	Dependencies []string `yaml:"dependencies,omitempty" json:"dependencies,omitempty"`
	Epic         string   `yaml:"epic,omitempty" json:"epic,omitempty"`
	RiskNotes    string   `yaml:"risk_notes,omitempty" json:"risk_notes,omitempty"`
	Role         string   `yaml:"role,omitempty" json:"role,omitempty"`
}

// Spec is one feature spec file: its frontmatter, its markdown body, and where
// it lives on disk.
type Spec struct {
	Frontmatter `yaml:",inline" json:",inline"`

	// Path is absolute. Callers that need the workspace-relative form
	// (`features/backlog/FTR-0001-….md`, as vb reports it) derive it.
	Path string `json:"path"`
	// Body is everything after the closing frontmatter delimiter.
	Body string `json:"-"`
}

// Unassigned is the owner value the VirtualBoard feature template writes for a
// feature nobody has claimed. It is distinct from an empty owner only in that
// the template prefers it; both mean "unclaimed".
const Unassigned = "unassigned"

// Claimed reports whether the spec has a real owner.
func (s *Spec) Claimed() bool {
	owner := strings.TrimSpace(s.Owner)
	return owner != "" && owner != Unassigned
}

// HasLabel reports whether the spec carries the given label.
func (s *Spec) HasLabel(label string) bool {
	for _, candidate := range s.Labels {
		if candidate == label {
			return true
		}
	}
	return false
}

// Load reads and parses a feature spec from disk.
func Load(path string) (*Spec, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read feature spec: %w", err)
	}
	spec, err := Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	spec.Path = path
	return spec, nil
}

// Parse splits a feature spec into frontmatter and body. A spec must open with
// a `---` delimiter on its first line; anything else is a malformed spec rather
// than a body-only document, because `vb new` always writes frontmatter.
func Parse(raw []byte) (*Spec, error) {
	text := strings.ReplaceAll(string(raw), "\r\n", "\n")
	if !strings.HasPrefix(text, "---\n") {
		return nil, fmt.Errorf("missing YAML frontmatter delimiter")
	}
	rest := text[len("---\n"):]
	end := strings.Index(rest, "\n---")
	if end < 0 {
		return nil, fmt.Errorf("unterminated YAML frontmatter")
	}
	header := rest[:end+1]
	body := rest[end+len("\n---"):]
	body = strings.TrimPrefix(body, "\n")

	spec := &Spec{Body: body}
	if err := yaml.Unmarshal([]byte(header), &spec.Frontmatter); err != nil {
		return nil, fmt.Errorf("parse frontmatter: %w", err)
	}
	if spec.ID == "" {
		return nil, fmt.Errorf("frontmatter has no id")
	}
	return spec, nil
}

// Section returns the body of a top-level markdown section by heading text,
// case-insensitively and ignoring the leading `#` run. The returned text has
// surrounding blank lines trimmed. The second result reports whether the
// heading exists at all, so an intentionally empty section is distinguishable
// from a missing one.
func (s *Spec) Section(heading string) (string, bool) {
	want := normalizeHeading(heading)
	scanner := bufio.NewScanner(strings.NewReader(s.Body))
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)

	var collected []string
	var depth int
	capturing := false
	for scanner.Scan() {
		line := scanner.Text()
		level, text, isHeading := parseHeading(line)
		switch {
		case isHeading && capturing && level <= depth:
			return strings.TrimSpace(strings.Join(collected, "\n")), true
		case isHeading && normalizeHeading(text) == want && !capturing:
			capturing, depth = true, level
		case capturing:
			collected = append(collected, line)
		}
	}
	if !capturing {
		return "", false
	}
	return strings.TrimSpace(strings.Join(collected, "\n")), true
}

// Sections lists the top-level heading texts of the body in document order.
func (s *Spec) Sections() []string {
	var out []string
	scanner := bufio.NewScanner(strings.NewReader(s.Body))
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		if _, text, ok := parseHeading(scanner.Text()); ok {
			out = append(out, text)
		}
	}
	return out
}

// AcceptanceCriteria extracts the checklist items from the "Acceptance Criteria"
// section, tolerating the "(Testable)" suffix the template ships with. Each
// item reports whether its box is ticked.
func (s *Spec) AcceptanceCriteria() []Criterion {
	body, ok := s.Section("Acceptance Criteria")
	if !ok {
		body, ok = s.Section("Acceptance Criteria (Testable)")
		if !ok {
			return nil
		}
	}
	var out []Criterion
	for _, line := range strings.Split(body, "\n") {
		trimmed := strings.TrimSpace(line)
		for _, prefix := range []string{"- ", "* ", "+ "} {
			if !strings.HasPrefix(trimmed, prefix) {
				continue
			}
			item := strings.TrimSpace(strings.TrimPrefix(trimmed, prefix))
			lowered := strings.ToLower(item)
			switch {
			case strings.HasPrefix(lowered, "[x]"):
				out = append(out, Criterion{Text: strings.TrimSpace(item[3:]), Done: true})
			case strings.HasPrefix(lowered, "[ ]"):
				out = append(out, Criterion{Text: strings.TrimSpace(item[3:]), Done: false})
			}
			break
		}
	}
	return out
}

// Criterion is one acceptance-criteria checklist item.
type Criterion struct {
	Text string `json:"text"`
	Done bool   `json:"done"`
}

// SortSpecs orders specs the way the board and `hvb feature list` present them:
// priority first (P0 before P3, unset last), then feature id.
func SortSpecs(specs []*Spec) {
	sort.SliceStable(specs, func(i, j int) bool {
		left, right := priorityRank(specs[i].Priority), priorityRank(specs[j].Priority)
		if left != right {
			return left < right
		}
		return specs[i].ID < specs[j].ID
	})
}

func priorityRank(priority string) int {
	switch strings.ToUpper(strings.TrimSpace(priority)) {
	case "P0":
		return 0
	case "P1":
		return 1
	case "P2":
		return 2
	case "P3":
		return 3
	default:
		return 4
	}
}

func parseHeading(line string) (level int, text string, ok bool) {
	trimmed := strings.TrimLeft(line, " ")
	hashes := len(trimmed) - len(strings.TrimLeft(trimmed, "#"))
	if hashes == 0 || hashes > 6 {
		return 0, "", false
	}
	rest := trimmed[hashes:]
	if rest != "" && !strings.HasPrefix(rest, " ") {
		return 0, "", false
	}
	return hashes, strings.TrimSpace(rest), true
}

func normalizeHeading(heading string) string {
	return strings.ToLower(strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(heading), "# ")))
}
