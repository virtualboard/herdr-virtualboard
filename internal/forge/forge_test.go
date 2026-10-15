package forge

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/virtualboard/herdr-virtualboard/internal/git"
)

func remote(t *testing.T, raw string) git.Remote {
	t.Helper()
	parsed, err := git.ParseRemote(raw)
	if err != nil {
		t.Fatal(err)
	}
	return parsed
}

// The governing rule of this package: nothing here may fail a run. Every path
// that cannot open a pull request must still hand the user something to click.
func TestUnsupportedForgeDegradesToACompareURL(t *testing.T) {
	result := Open(context.Background(), Request{
		Remote: remote(t, "git@code.internal:team/thing.git"),
		Base:   "main", Head: "feature/FTR-0001/x",
	}, Options{})

	if result.Opened {
		t.Fatal("an unknown forge cannot have opened anything")
	}
	if result.URL == "" {
		t.Fatal("the user must still get a compare URL")
	}
	if !strings.Contains(result.Reason, "no pull-request client") {
		t.Errorf("Reason = %q", result.Reason)
	}
}

// A missing token is the common case for a self-hosted forge reached over SSH.
// It is a degraded result, never an error.
func TestForgejoWithoutATokenDegrades(t *testing.T) {
	result := Open(context.Background(), Request{
		Remote: remote(t, "ssh://git@forgejo.example:2222/org/repo.git"),
		Base:   "main", Head: "topic",
	}, Options{})

	if result.Opened {
		t.Fatal("no token means no pull request")
	}
	if !strings.Contains(result.Reason, "token") {
		t.Errorf("the reason should name the missing token: %q", result.Reason)
	}
	if result.URL != "https://forgejo.example/org/repo/compare/main...topic" {
		t.Errorf("URL = %q", result.URL)
	}
}

func TestForgejoOpensAPullRequest(t *testing.T) {
	var gotPath, gotAuth string
	var gotBody map[string]any

	client := handlerClient(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s", r.Method)
		}
		if r.URL.Scheme != "https" || r.URL.Host != "forgejo.example" {
			t.Errorf("endpoint = %s", r.URL)
		}
		if r.Header.Get("Content-Type") != "application/json" || r.Header.Get("Accept") != "application/json" {
			t.Errorf("headers = %v", r.Header)
		}
		gotPath, gotAuth = r.URL.Path, r.Header.Get("Authorization")
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"number":42,"html_url":"https://forgejo.example/org/repo/pulls/42"}`))
	}))

	result := Open(context.Background(), Request{
		Remote: remote(t, "ssh://git@forgejo.example:2222/org/repo.git"),
		Base:   "main", Head: "feature/FTR-0001/x",
		Title: "FTR-0001: Add retry", Body: "body text",
	}, Options{Token: "secret", HTTPClient: client})

	if !result.Opened {
		t.Fatalf("Opened = false, reason %q", result.Reason)
	}
	if result.Number != 42 || result.URL != "https://forgejo.example/org/repo/pulls/42" {
		t.Errorf("result = %+v", result)
	}
	if gotPath != "/api/v1/repos/org/repo/pulls" {
		t.Errorf("path = %q", gotPath)
	}
	if gotAuth != "token secret" {
		t.Errorf("auth header = %q", gotAuth)
	}
	if gotBody["base"] != "main" || gotBody["head"] != "feature/FTR-0001/x" {
		t.Errorf("body = %v", gotBody)
	}
}

// Forgejo and Gitea have no `draft` field on creation; their own UI expresses a
// draft as a WIP title prefix.
func TestForgejoDraftBecomesAWIPTitle(t *testing.T) {
	var gotBody map[string]any
	client := handlerClient(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"number":1,"html_url":"u"}`))
	}))

	Open(context.Background(), Request{
		Remote: remote(t, "https://gitea.example/org/repo.git"),
		Base:   "main", Head: "topic", Title: "FTR-0001: thing", Draft: true,
	}, Options{Token: "t", HTTPClient: client})

	if title, _ := gotBody["title"].(string); !strings.HasPrefix(title, "WIP: ") {
		t.Fatalf("draft title = %q, want a WIP prefix", title)
	}
	if _, present := gotBody["draft"]; present {
		t.Error("the API has no draft field; sending one risks a 422")
	}
}

func TestForgejoRejectedTokenDegrades(t *testing.T) {
	client := handlerClient(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))

	result := Open(context.Background(), Request{
		Remote: remote(t, "https://forgejo.example/org/repo.git"),
		Base:   "main", Head: "topic",
	}, Options{Token: "bad", HTTPClient: client})

	if result.Opened {
		t.Fatal("a rejected token cannot have opened anything")
	}
	if !strings.Contains(result.Reason, "rejected") {
		t.Errorf("Reason = %q", result.Reason)
	}
	if result.URL == "" {
		t.Error("the compare URL should still be offered")
	}
}

// An existing pull request is a success for hvb's purposes, not a failure.
func TestForgejoConflictCountsAsOpened(t *testing.T) {
	client := handlerClient(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusConflict)
	}))

	result := Open(context.Background(), Request{
		Remote: remote(t, "https://forgejo.example/org/repo.git"),
		Base:   "main", Head: "topic",
	}, Options{Token: "t", HTTPClient: client})

	if !result.Opened {
		t.Fatalf("a conflict means one already exists: %+v", result)
	}
}

// fakeGH writes a stub `gh` that records argv and replies with a script.
func fakeGH(t *testing.T, script string) (string, func() []string) {
	t.Helper()
	dir := t.TempDir()
	logPath := filepath.Join(dir, "argv.log")
	path := filepath.Join(dir, "gh")
	body := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> " + logPath + "\n" + script + "\n"
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return path, func() []string {
		raw, err := os.ReadFile(logPath)
		if err != nil {
			return nil
		}
		var out []string
		for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
			if line != "" {
				out = append(out, line)
			}
		}
		return out
	}
}

func TestGitHubOpensThroughTheCLI(t *testing.T) {
	bin, argv := fakeGH(t, `case "$*" in
  *"auth status"*) exit 0 ;;
  *"pr create"*) echo "https://github.com/o/r/pull/7" ;;
esac`)

	result := Open(context.Background(), Request{
		Remote: remote(t, "git@github.com:o/r.git"),
		Base:   "main", Head: "feature/FTR-0001/x",
		Title: "FTR-0001: thing", Body: "body", Draft: true,
	}, Options{GitHubCLI: bin})

	if !result.Opened {
		t.Fatalf("Opened = false, reason %q", result.Reason)
	}
	if result.URL != "https://github.com/o/r/pull/7" || result.Number != 7 {
		t.Errorf("result = %+v", result)
	}
	create := ""
	for _, line := range argv() {
		if strings.Contains(line, "pr create") {
			create = line
		}
	}
	for _, want := range []string{"--repo o/r", "--base main", "--head feature/FTR-0001/x", "--draft"} {
		if !strings.Contains(create, want) {
			t.Errorf("gh argv %q is missing %q", create, want)
		}
	}
}

func TestGitHubUnauthenticatedDegrades(t *testing.T) {
	bin, _ := fakeGH(t, `case "$*" in
  *"auth status"*) echo "not logged in" >&2; exit 1 ;;
esac`)
	result := Open(context.Background(), Request{
		Remote: remote(t, "git@github.com:o/r.git"), Base: "main", Head: "topic",
	}, Options{GitHubCLI: bin})

	if result.Opened {
		t.Fatal("an unauthenticated gh cannot open anything")
	}
	if !strings.Contains(result.Reason, "gh auth login") {
		t.Errorf("the reason should say how to fix it: %q", result.Reason)
	}
}

// gh reports an already-open pull request as a failure; recovering its URL is
// better than telling the user something went wrong.
func TestGitHubRecoversAnExistingPullRequest(t *testing.T) {
	bin, _ := fakeGH(t, `case "$*" in
  *"auth status"*) exit 0 ;;
  *"pr create"*) echo "a pull request already exists" >&2; exit 1 ;;
  *"pr list"*) echo '[{"number":5,"url":"https://github.com/o/r/pull/5"}]' ;;
esac`)
	result := Open(context.Background(), Request{
		Remote: remote(t, "git@github.com:o/r.git"), Base: "main", Head: "topic",
	}, Options{GitHubCLI: bin})

	if !result.Opened || result.Number != 5 {
		t.Fatalf("result = %+v", result)
	}
}

func TestKindOverrideBeatsDetection(t *testing.T) {
	// A self-hosted Forgejo on a neutral hostname: detection says unknown,
	// configuration says otherwise, and configuration wins.
	opener := For(remote(t, "git@code.internal:o/r.git"), Options{Kind: git.Forgejo})
	if opener == nil || opener.Name() != "forgejo" {
		t.Fatalf("opener = %v", opener)
	}
}

func TestTitleFor(t *testing.T) {
	if got := TitleFor("FTR-0007", "Add retry"); got != "FTR-0007: Add retry" {
		t.Errorf("TitleFor = %q", got)
	}
	if got := TitleFor("FTR-0007", "  "); got != "FTR-0007" {
		t.Errorf("TitleFor with no title = %q", got)
	}
}

func TestBodyCarriesTheReviewersContext(t *testing.T) {
	body := Body(BodyInput{
		FeatureID: "FTR-0007", Title: "Add retry",
		Summary:  "Retry failed PUTs with backoff.",
		SpecPath: ".virtualboard/features/in-progress/FTR-0007.md",
		Criteria: []Criterion{{Text: "retries three times", Done: true}, {Text: "backoff is exponential"}},
		Commits:  []string{"FTR-0007: add retry helper"},
		Role:     "backend_dev", Harness: "claude",
	})
	for _, want := range []string{
		"FTR-0007", "Add retry", "Retry failed PUTs",
		"- [x] retries three times", "- [ ] backoff is exponential",
		"FTR-0007: add retry helper", "backend_dev",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("body is missing %q:\n%s", want, body)
		}
	}
}

// Exercise the real HTTP client, request creation, handler and response decoding
// without opening sockets or changing http.DefaultTransport. These tests cover
// application HTTP behavior; they do not claim to test TCP or TLS negotiation.
type handlerTransport struct{ handler http.Handler }

func (h handlerTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if err := request.Context().Err(); err != nil {
		return nil, err
	}
	recorder := httptest.NewRecorder()
	h.handler.ServeHTTP(recorder, request)
	response := recorder.Result()
	response.Request = request
	return response, nil
}

func handlerClient(handler http.Handler) *http.Client {
	return &http.Client{Transport: handlerTransport{handler: handler}, Timeout: time.Second}
}
