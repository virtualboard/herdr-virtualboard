// Package forge opens pull requests, or explains precisely why it could not.
//
// The design rule here is that a missing token, an unrecognised host, or a
// forge hvb has no client for is a *degraded result*, never a failed run. The
// agent has already done the work and committed it; losing that because nobody
// configured an API token would be absurd. Every path that cannot open a pull
// request returns a Result carrying the compare URL instead, and the caller
// shows it to the user.
package forge

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/virtualboard/herdr-virtualboard/internal/git"
)

// Request is a pull request to open.
type Request struct {
	Remote git.Remote
	Base   string
	Head   string
	Title  string
	Body   string
	Draft  bool
}

// Result is what came of it.
type Result struct {
	// Opened reports whether a pull request now exists.
	Opened bool `json:"opened"`
	// URL is the pull request when Opened, otherwise the compare page the
	// user can open by hand.
	URL string `json:"url,omitempty"`
	// Number is the pull request number, when the forge reported one.
	Number int `json:"number,omitempty"`
	// Reason explains a result that is not Opened, in words a user can act
	// on. It is empty on success.
	Reason string `json:"reason,omitempty"`
}

// Opener creates pull requests on one kind of forge.
type Opener interface {
	// Open attempts the pull request. It returns an error only for a genuine
	// failure — a refused API call, a broken response. "I am not configured
	// for this" is a Result with Opened false and a Reason.
	Open(ctx context.Context, req Request) (*Result, error)
	// Name identifies the opener in diagnostics.
	Name() string
}

// Options configure which openers are available.
type Options struct {
	// HTTPClient optionally supplies the REST transport (for example a custom
	// proxy or an in-memory test handler). Nil uses the bounded default client.
	HTTPClient *http.Client
	// GitHubCLI is the `gh` executable; empty means "gh" on PATH.
	GitHubCLI string
	// Token authenticates the Forgejo and Gitea REST clients.
	Token string
	// BaseURL overrides the API base derived from the remote host, for a
	// forge served under a path or on a non-default scheme.
	BaseURL string
	// Kind overrides forge detection, for a self-hosted instance on a
	// hostname that gives nothing away.
	Kind git.Kind
}

// For returns the opener for a remote, or nil when hvb has no client for it.
func For(remote git.Remote, opts Options) Opener {
	kind := remote.Kind
	if opts.Kind != "" && opts.Kind != git.Unknown {
		kind = opts.Kind
	}
	switch kind {
	case git.GitHub:
		return &githubCLI{bin: opts.GitHubCLI}
	case git.Forgejo, git.Gitea:
		return &giteaAPI{token: opts.Token, baseURL: opts.BaseURL, kind: kind, client: opts.HTTPClient}
	default:
		return nil
	}
}

// Open runs the right opener for a remote, degrading to a compare URL when
// there is no client or it is not configured.
func Open(ctx context.Context, req Request, opts Options) *Result {
	compare := req.Remote.CompareURL(req.Base, req.Head)

	opener := For(req.Remote, opts)
	if opener == nil {
		return &Result{
			URL: compare,
			Reason: fmt.Sprintf("no pull-request client for %s (host %q) — open it from the compare page, "+
				"or set the forge kind in configuration", req.Remote.Kind, req.Remote.Host),
		}
	}

	result, err := opener.Open(ctx, req)
	if err != nil {
		return &Result{
			URL:    compare,
			Reason: fmt.Sprintf("%s could not open the pull request: %v", opener.Name(), err),
		}
	}
	if result.URL == "" {
		result.URL = compare
	}
	return result
}

// TitleFor composes a pull-request title from a feature.
func TitleFor(featureID, featureTitle string) string {
	title := strings.TrimSpace(featureTitle)
	if title == "" {
		return featureID
	}
	return fmt.Sprintf("%s: %s", featureID, title)
}
