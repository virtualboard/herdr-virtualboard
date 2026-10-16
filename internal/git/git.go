// Package git is everything hvb needs to know about the repository behind a
// VirtualBoard workspace: where it is, what it pushes to, and what state a
// feature branch is in.
//
// It shells out to `git` rather than linking a library. The plugin already
// depends on `vb` and `herdr` being on PATH, git is on every machine that has
// those, and the porcelain this uses is the stable, scriptable subset.
package git

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// Binary is the executable looked up on PATH. HVB_GIT_BIN overrides it, which
// is what the tests use to substitute a recording stub.
const Binary = "git"

// DefaultTimeout bounds a local git command. Push carries its own, longer bound
// because it talks to a network.
const (
	DefaultTimeout = 30 * time.Second
	PushTimeout    = 5 * time.Minute
)

// Repo is a git repository hvb operates on.
type Repo struct {
	// Root is the top-level working directory.
	Root string
	// Bin is the git executable; empty means Binary.
	Bin string
}

// Error is a failed git invocation.
type Error struct {
	Args    []string
	Code    int
	Message string
}

func (e *Error) Error() string {
	return fmt.Sprintf("git %s: %s", strings.Join(e.Args, " "), e.Message)
}

// ErrNotARepo is returned when a directory is not inside a git repository.
var ErrNotARepo = errors.New("not a git repository")

// Open resolves the repository containing dir.
func Open(ctx context.Context, dir string) (*Repo, error) {
	repo := &Repo{Root: dir}
	out, err := repo.run(ctx, DefaultTimeout, "rev-parse", "--show-toplevel")
	if err != nil {
		return nil, fmt.Errorf("%w: %s", ErrNotARepo, dir)
	}
	repo.Root = strings.TrimSpace(out)
	return repo, nil
}

// DefaultBranch reports the branch a feature should be cut from.
//
// It prefers the remote's published HEAD, because that is what the forge will
// use as a pull-request base. Only when there is no remote does it fall back to
// the local checkout's current branch, which is the best guess left.
func (r *Repo) DefaultBranch(ctx context.Context, remote string) (string, error) {
	if remote == "" {
		remote = "origin"
	}
	// The symbolic ref is only present once someone has run `git remote set-head`
	// or cloned; it is authoritative when it exists.
	if out, err := r.run(ctx, DefaultTimeout, "symbolic-ref", "--short", "refs/remotes/"+remote+"/HEAD"); err == nil {
		if _, branch, found := strings.Cut(strings.TrimSpace(out), "/"); found {
			return branch, nil
		}
	}
	// Ask the remote directly. This is a network call, so it is not the first
	// thing tried, but it is right when the local ref was never set up.
	if out, err := r.run(ctx, DefaultTimeout, "ls-remote", "--symref", remote, "HEAD"); err == nil {
		for _, line := range strings.Split(out, "\n") {
			if rest, ok := strings.CutPrefix(line, "ref: refs/heads/"); ok {
				if name, _, _ := strings.Cut(rest, "\t"); name != "" {
					return strings.TrimSpace(name), nil
				}
			}
		}
	}
	for _, candidate := range []string{"main", "master"} {
		if r.HasBranch(ctx, candidate) {
			return candidate, nil
		}
	}
	out, err := r.run(ctx, DefaultTimeout, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return "", fmt.Errorf("resolve default branch: %w", err)
	}
	branch := strings.TrimSpace(out)
	if branch == "" || branch == "HEAD" {
		return "", fmt.Errorf("resolve default branch: repository has no branches yet")
	}
	return branch, nil
}

// HasBranch reports whether a local branch exists.
func (r *Repo) HasBranch(ctx context.Context, branch string) bool {
	_, err := r.run(ctx, DefaultTimeout, "show-ref", "--verify", "--quiet", "refs/heads/"+branch)
	return err == nil
}

// RemoteURL returns a remote's push URL.
func (r *Repo) RemoteURL(ctx context.Context, remote string) (string, error) {
	if remote == "" {
		remote = "origin"
	}
	out, err := r.run(ctx, DefaultTimeout, "remote", "get-url", remote)
	if err != nil {
		return "", fmt.Errorf("no remote %q", remote)
	}
	return strings.TrimSpace(out), nil
}

// Clean reports whether the working tree has no uncommitted changes. Untracked
// files count as changes: an agent that wrote a new file and did not add it has
// not finished, and a pull request built from that branch would be missing it.
func (r *Repo) Clean(ctx context.Context) (bool, error) {
	out, err := r.run(ctx, DefaultTimeout, "status", "--porcelain")
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(out) == "", nil
}

// CommitsAhead counts commits on branch that base does not have. Zero means
// there is nothing to open a pull request for.
func (r *Repo) CommitsAhead(ctx context.Context, branch, base string) (int, error) {
	out, err := r.run(ctx, DefaultTimeout, "rev-list", "--count", base+".."+branch)
	if err != nil {
		return 0, err
	}
	count := 0
	for _, r := range strings.TrimSpace(out) {
		if r < '0' || r > '9' {
			return 0, fmt.Errorf("unexpected rev-list output %q", out)
		}
		count = count*10 + int(r-'0')
	}
	return count, nil
}

// Push publishes a branch and sets its upstream.
func (r *Repo) Push(ctx context.Context, remote, branch string) error {
	if remote == "" {
		remote = "origin"
	}
	// --force-with-lease is deliberately absent: hvb only ever pushes a branch
	// it created for one feature, and a plain push failing on a non-fast-forward
	// is the correct outcome — somebody else has been working on that branch.
	_, err := r.run(ctx, PushTimeout, "push", "--set-upstream", remote, branch)
	return err
}

// Log returns the subject lines of commits on branch not in base, newest first.
// The pull-request body is built from them.
func (r *Repo) Log(ctx context.Context, branch, base string, limit int) ([]string, error) {
	args := []string{"log", "--format=%s", base + ".." + branch}
	if limit > 0 {
		args = append(args, fmt.Sprintf("--max-count=%d", limit))
	}
	out, err := r.run(ctx, DefaultTimeout, args...)
	if err != nil {
		return nil, err
	}
	var subjects []string
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			subjects = append(subjects, line)
		}
	}
	return subjects, nil
}

// CurrentBranch returns the checked-out branch.
func (r *Repo) CurrentBranch(ctx context.Context) (string, error) {
	out, err := r.run(ctx, DefaultTimeout, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

func (r *Repo) Head(ctx context.Context) (string, error) {
	out, err := r.run(ctx, DefaultTimeout, "rev-parse", "--verify", "HEAD")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// At returns a Repo rooted at another directory — a worktree checkout — keeping
// the same git binary.
func (r *Repo) At(dir string) *Repo { return &Repo{Root: dir, Bin: r.Bin} }

func (r *Repo) bin() string {
	if r.Bin != "" {
		return r.Bin
	}
	if bin := os.Getenv("HVB_GIT_BIN"); bin != "" {
		return bin
	}
	return Binary
}

func (r *Repo) run(ctx context.Context, timeout time.Duration, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, r.bin(), args...)
	cmd.Dir = r.Root
	// Never let git stop for credentials: a dispatch running in the background
	// must fail with a message, not hang on an invisible prompt.
	cmd.Env = append(cmd.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_OPTIONAL_LOCKS=0")
	cmd.Stdin = nil

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		message := strings.TrimSpace(stderr.String())
		if message == "" {
			message = err.Error()
		}
		code := -1
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			code = exitErr.ExitCode()
		}
		return "", &Error{Args: args, Code: code, Message: message}
	}
	return stdout.String(), nil
}
