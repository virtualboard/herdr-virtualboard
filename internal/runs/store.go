// Package runs records agent dispatches.
//
// This is the only state hvb owns. The board itself is the repository: `vb` and
// the markdown specs under `.virtualboard/features/` hold every fact about a
// feature, and hvb never shadows one. What the repository cannot hold is which
// Herdr pane is currently running which feature — that is machine-local,
// short-lived, and would be noise in a commit — so it lives here instead, in one
// JSON file per project under the user's data directory.
package runs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/virtualboard/herdr-virtualboard/internal/feature"
)

// State is a run's lifecycle state.
type State string

const (
	// Queued: the run record exists but no pane has been created yet.
	Queued State = "queued"
	// Running: an agent is live in its pane.
	Running State = "running"
	// Awaiting: the harness finished its turn without reporting an outcome,
	// so a human should look. Reached when Herdr reports `done` but the
	// agent never called `hvb run done`.
	Awaiting State = "awaiting"
	// Succeeded / Failed: the agent reported an explicit outcome.
	Succeeded State = "succeeded"
	Failed    State = "failed"
	// Cancelled: a human stopped the run.
	Cancelled State = "cancelled"
)

// Terminal reports whether no further transition is expected.
func (s State) Terminal() bool {
	switch s {
	case Succeeded, Failed, Cancelled:
		return true
	default:
		return false
	}
}

// Run is one dispatch of one feature to one agent.
type Run struct {
	ID        string `json:"id"`
	FeatureID string `json:"feature_id"`
	// Title is copied at dispatch so a run for a deleted feature still reads
	// sensibly in the history.
	Title string `json:"title"`
	// Status is the feature's lifecycle status when the run started, which
	// is what selected the column policy that dispatched it.
	Status feature.Status `json:"status"`
	Role   string         `json:"role"`
	Kind   string         `json:"kind"`
	State  State          `json:"state"`

	WorkspaceID string `json:"workspace_id,omitempty"`
	TabID       string `json:"tab_id,omitempty"`
	PaneID      string `json:"pane_id,omitempty"`
	AnchorPane  string `json:"anchor_pane,omitempty"`
	AgentName   string `json:"agent_name,omitempty"`
	LockToken   string `json:"lock_token,omitempty"`
	LockOwner   string `json:"lock_owner,omitempty"`
	// LeaseReleased records confirmed absence of this run's lease, including
	// awaiting runs that remain available for a later human outcome.
	LeaseReleased bool                `json:"lease_released,omitempty"`
	Completion    *CompletionProgress `json:"completion,omitempty"`
	Revision      *Revision           `json:"revision,omitempty"`
	ReviewOf      *Revision           `json:"review_of,omitempty"`
	AutoParentID  string              `json:"auto_parent_id,omitempty"`
	AutoChildID   string              `json:"auto_child_id,omitempty"`
	AutoDepth     int                 `json:"auto_depth,omitempty"`

	// PendingPrompt is the prompt hvb has not been able to submit yet,
	// because the harness was blocked on its own startup UI. Reconcile
	// submits it once the agent settles, then clears this.
	PendingPrompt string `json:"pending_prompt,omitempty"`

	// Worktree describes the isolated checkout a run was dispatched into.
	// Nil for a run that worked directly in the project directory.
	Worktree *Worktree `json:"worktree,omitempty"`
	// PullRequest records what came of opening one. Nil when none was asked
	// for; present but not Opened when it was asked for and could not be.
	PullRequest *PullRequest `json:"pull_request,omitempty"`

	StartedAt  time.Time  `json:"started_at"`
	EndedAt    *time.Time `json:"ended_at,omitempty"`
	Outcome    string     `json:"outcome,omitempty"`
	Note       string     `json:"note,omitempty"`
	MovedTo    string     `json:"moved_to,omitempty"`
	Comments   []Comment  `json:"comments,omitempty"`
	LastErrors []string   `json:"last_errors,omitempty"`
}

// Worktree is the isolated checkout a run was dispatched into.
type Worktree struct {
	// Path is the checkout directory, which is also the agent's cwd.
	Path   string `json:"path"`
	Branch string `json:"branch"`
	Base   string `json:"base"`
	Remote string `json:"remote"`
	// WorkspaceID is the linked Herdr workspace Herdr opened for it.
	WorkspaceID string `json:"workspace_id"`
	// RepoRoot is the parent repository the worktree belongs to.
	RepoRoot string `json:"repo_root"`
	// Removed records that the checkout has been cleaned up, so the board
	// stops offering to do it again.
	Removed bool `json:"removed,omitempty"`
}

// PullRequest is the outcome of trying to open one.
type PullRequest struct {
	// Opened distinguishes a pull request that exists from a compare URL
	// the user still has to click.
	Opened bool   `json:"opened"`
	URL    string `json:"url,omitempty"`
	Number int    `json:"number,omitempty"`
	// Reason explains anything other than a clean success.
	Reason string `json:"reason,omitempty"`
	// Pushed reports whether the branch reached the remote. A pull request
	// cannot exist without it, but a push can succeed on its own.
	Pushed bool `json:"pushed"`
}

// Comment is a note attached to a run, by an agent or a human.
type Comment struct {
	At     time.Time `json:"at"`
	Author string    `json:"author"`
	Body   string    `json:"body"`
}

// Active reports whether the run still occupies a pane hvb should watch.
func (r *Run) Active() bool {
	return !r.State.Terminal() || (r.Completion != nil && !r.Completion.Done)
}

// CompletionProgress records which effects of an accepted outcome reached the
// outside world. The outcome survives failures; unfinished effects can resume.
type CompletionProgress struct {
	Cancelled   bool   `json:"cancelled,omitempty"`
	AutoChecked bool   `json:"auto_checked"`
	Captured    bool   `json:"captured"`
	Target      string `json:"target,omitempty"`
	Published   bool   `json:"published"`
	Linked      bool   `json:"linked"`
	Routed      bool   `json:"routed"`
	Indexed     bool   `json:"indexed"`
	Released    bool   `json:"released"`
	Done        bool   `json:"done"`
	LastError   string `json:"last_error,omitempty"`
}

// Revision is an immutable Git handoff, not a cached feature fact.
type Revision struct {
	Commit     string `json:"commit"`
	Branch     string `json:"branch"`
	Repository string `json:"repository"`
}

// Duration is how long the run has been going, or how long it took.
func (r *Run) Duration() time.Duration {
	if r.EndedAt != nil {
		return r.EndedAt.Sub(r.StartedAt)
	}
	return time.Since(r.StartedAt)
}

// document is the on-disk file shape.
type document struct {
	Version int    `json:"version"`
	Project string `json:"project"`
	Runs    []*Run `json:"runs"`
}

// version is the run-file schema version. Bump it when the shape changes in a
// way an older hvb would misread; reads preserve and reject unsupported formats.
const version = 2

// MaxRunsPerFeature caps retained history per feature. Older terminal runs are
// pruned on save so a long-lived board does not grow without bound.
const MaxRunsPerFeature = 20

// Store is the per-project run file. It is safe for concurrent use within one
// process and uses atomic replacement plus a lock file across processes.
type Store struct {
	path string
	mu   sync.Mutex
}

// ErrNotFound is returned when a run id does not exist.
var ErrNotFound = errors.New("run not found")

// DataDir is the directory holding run files: $HVB_DATA_DIR, else
// $XDG_DATA_HOME/herdr-virtualboard, else ~/.local/share/herdr-virtualboard.
func DataDir() (string, error) {
	if dir := os.Getenv("HVB_DATA_DIR"); dir != "" {
		return dir, nil
	}
	if dir := os.Getenv("XDG_DATA_HOME"); dir != "" {
		return filepath.Join(dir, "herdr-virtualboard"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	return filepath.Join(home, ".local", "share", "herdr-virtualboard"), nil
}

// Open returns the run store for a project, keyed by the workspace identity so
// two checkouts of the same repository keep separate histories. Opening and
// reading an absent store do not create directories; only mutations do.
func Open(projectID string) (*Store, error) {
	dir, err := DataDir()
	if err != nil {
		return nil, err
	}
	runsDir := filepath.Join(dir, "runs")
	return &Store{path: filepath.Join(runsDir, projectID+".json")}, nil
}

// Path is the run file's location, for diagnostics.
func (s *Store) Path() string { return s.path }

// List returns every run, newest first.
func (s *Store) List() ([]*Run, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	doc, err := s.load()
	if err != nil {
		return nil, err
	}
	sort.SliceStable(doc.Runs, func(i, j int) bool { return doc.Runs[i].StartedAt.After(doc.Runs[j].StartedAt) })
	return doc.Runs, nil
}

// ForFeature returns a feature's runs, newest first.
func (s *Store) ForFeature(featureID string) ([]*Run, error) {
	all, err := s.List()
	if err != nil {
		return nil, err
	}
	var out []*Run
	for _, run := range all {
		if strings.EqualFold(run.FeatureID, featureID) {
			out = append(out, run)
		}
	}
	return out, nil
}

// Active returns the runs still holding a pane, newest first.
func (s *Store) Active() ([]*Run, error) {
	all, err := s.List()
	if err != nil {
		return nil, err
	}
	var out []*Run
	for _, run := range all {
		if run.Active() {
			out = append(out, run)
		}
	}
	return out, nil
}

// Get returns one run by id, or by the `latest` alias for a feature.
func (s *Store) Get(id string) (*Run, error) {
	all, err := s.List()
	if err != nil {
		return nil, err
	}
	for _, run := range all {
		if run.ID == id {
			return run, nil
		}
	}
	return nil, fmt.Errorf("%w: %s", ErrNotFound, id)
}

// Latest returns a feature's most recent run.
func (s *Store) Latest(featureID string) (*Run, error) {
	all, err := s.ForFeature(featureID)
	if err != nil {
		return nil, err
	}
	if len(all) == 0 {
		return nil, fmt.Errorf("%w: no runs for %s", ErrNotFound, featureID)
	}
	return all[0], nil
}

// Append adds a run.
func (s *Store) Append(run *Run) error {
	return s.mutate(func(doc *document) error {
		doc.Runs = append(doc.Runs, run)
		return nil
	})
}

// Update applies a mutation to one run under the file lock, so a concurrent
// `hvb run done` from an agent pane and a TUI refresh cannot lose each other's
// write.
func (s *Store) Update(id string, apply func(*Run) error) (*Run, error) {
	var updated *Run
	err := s.mutate(func(doc *document) error {
		for _, run := range doc.Runs {
			if run.ID != id {
				continue
			}
			if err := apply(run); err != nil {
				return err
			}
			updated = run
			return nil
		}
		return fmt.Errorf("%w: %s", ErrNotFound, id)
	})
	if err != nil {
		return nil, err
	}
	return updated, nil
}

// Finish marks a run terminal. It is idempotent: a run that already ended keeps
// its first outcome, because the agent's own report should not be overwritten
// by a later reconciliation noticing the pane closed.
func (s *Store) Finish(id string, state State, outcome, note string) (*Run, error) {
	return s.Update(id, func(run *Run) error {
		if run.State.Terminal() {
			return nil
		}
		now := time.Now().UTC()
		run.State = state
		run.EndedAt = &now
		run.Outcome = outcome
		if note != "" {
			run.Note = note
		}
		return nil
	})
}

// AddComment appends a comment to a run.
func (s *Store) AddComment(id, author, body string) (*Run, error) {
	return s.Update(id, func(run *Run) error {
		run.Comments = append(run.Comments, Comment{At: time.Now().UTC(), Author: author, Body: body})
		return nil
	})
}

func (s *Store) mutate(apply func(*document) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	return withOSLock(context.Background(), s.path+".mutex", func() error {
		doc, err := s.load()
		if err != nil {
			return err
		}
		if err := apply(doc); err != nil {
			return err
		}
		if err := validateDocument(doc); err != nil {
			return fmt.Errorf("refuse invalid run store mutation: %w", err)
		}
		prune(doc)
		return s.save(doc)
	})
}

func (s *Store) load() (*document, error) {
	raw, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return &document{Version: version}, nil
		}
		return nil, fmt.Errorf("read run store: %w", err)
	}
	doc, err := decodeDocument(raw)
	if err != nil {
		return nil, fmt.Errorf("run store %s cannot be read; preserve it and inspect hvb doctor before explicit recovery: %w", s.path, err)
	}
	return doc, nil
}

func (s *Store) save(doc *document) error {
	raw, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return fmt.Errorf("encode run store: %w", err)
	}
	raw = append(raw, '\n')
	temp, err := os.CreateTemp(filepath.Dir(s.path), ".runs-*.json")
	if err != nil {
		return fmt.Errorf("stage run store: %w", err)
	}
	name := temp.Name()
	defer os.Remove(name)
	if _, err := temp.Write(raw); err != nil {
		temp.Close()
		return fmt.Errorf("write run store: %w", err)
	}
	if err := temp.Chmod(0o600); err != nil {
		temp.Close()
		return fmt.Errorf("chmod run store: %w", err)
	}
	if err := temp.Close(); err != nil {
		return fmt.Errorf("close run store: %w", err)
	}
	if err := os.Rename(name, s.path); err != nil {
		return fmt.Errorf("replace run store: %w", err)
	}
	return nil
}

// prune caps per-feature history, dropping the oldest terminal runs first and
// never dropping an active one.
func prune(doc *document) {
	byFeature := map[string][]*Run{}
	for _, run := range doc.Runs {
		byFeature[run.FeatureID] = append(byFeature[run.FeatureID], run)
	}
	drop := map[string]bool{}
	for _, group := range byFeature {
		if len(group) <= MaxRunsPerFeature {
			continue
		}
		sort.SliceStable(group, func(i, j int) bool { return group[i].StartedAt.Before(group[j].StartedAt) })
		excess := len(group) - MaxRunsPerFeature
		for _, run := range group {
			if excess == 0 {
				break
			}
			if run.Active() || (run.Worktree != nil && !run.Worktree.Removed) {
				continue
			}
			drop[run.ID] = true
			excess--
		}
	}
	if len(drop) == 0 {
		return
	}
	kept := doc.Runs[:0]
	for _, run := range doc.Runs {
		if !drop[run.ID] {
			kept = append(kept, run)
		}
	}
	doc.Runs = kept
}
