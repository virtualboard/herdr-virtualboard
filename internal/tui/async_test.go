package tui

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/virtualboard/herdr-virtualboard/internal/feature"
	"github.com/virtualboard/herdr-virtualboard/internal/runs"
)

type controlledBackend struct {
	*fakeBackend
	loadHook     func(context.Context, int)
	dispatchHook func(context.Context) error
	moveHook     func(context.Context) error
	createHook   func(context.Context) (string, error)
	loads        atomic.Int32
}

func (b *controlledBackend) Load(ctx context.Context) ([]*feature.Spec, []*runs.Run, []error) {
	count := b.loads.Add(1)
	if b.loadHook != nil {
		b.loadHook(ctx, int(count))
	}
	return b.fakeBackend.Load(ctx)
}
func (b *controlledBackend) Dispatch(ctx context.Context, spec *feature.Spec, role, kind string, force bool) (*runs.Run, error) {
	if b.dispatchHook != nil {
		if err := b.dispatchHook(ctx); err != nil {
			return nil, err
		}
	}
	return b.fakeBackend.Dispatch(ctx, spec, role, kind, force)
}
func (b *controlledBackend) Move(ctx context.Context, id string, status feature.Status, owner string) error {
	if b.moveHook != nil {
		if err := b.moveHook(ctx); err != nil {
			return err
		}
	}
	return b.fakeBackend.Move(ctx, id, status, owner)
}

func (b *controlledBackend) Create(ctx context.Context, title string, labels []string, priority string) (string, error) {
	if b.createHook != nil {
		return b.createHook(ctx)
	}
	return b.fakeBackend.Create(ctx, title, labels, priority)
}

type frameSink struct {
	frames        chan string
	width         atomic.Int32
	height        atomic.Int32
	invalidations atomic.Int32
}

func (s *frameSink) Size() (int, int)          { return int(s.width.Load()), int(s.height.Load()) }
func (s *frameSink) Invalidate()               { s.invalidations.Add(1) }
func (s *frameSink) Draw(lines []string) error { s.frames <- strings.Join(lines, "\n"); return nil }

type loopHarness struct {
	keys   chan Key
	resize chan os.Signal
	ticks  chan time.Time
	sink   *frameSink
	done   chan error
	cancel context.CancelFunc
	model  *Model
}

func startLoop(t *testing.T, backend Backend, preload bool) *loopHarness {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	model := NewModel(backend, Palette{})
	model.Resize(140, 35)
	// Preload from the base backend to avoid consuming a controlled I/O gate.
	if preload {
		if b, ok := backend.(*controlledBackend); ok {
			model.applySnapshot(loadSnapshot(ctx, b.fakeBackend))
		} else {
			model.Reload(ctx)
		}
	}
	sink := &frameSink{frames: make(chan string, 256)}
	sink.width.Store(140)
	sink.height.Store(35)
	h := &loopHarness{keys: make(chan Key, 64), resize: make(chan os.Signal, 8), ticks: make(chan time.Time, 1), sink: sink, done: make(chan error, 1), cancel: cancel, model: model}
	go func() { h.done <- runLoop(ctx, model, sink, h.keys, h.resize, h.ticks) }()
	t.Cleanup(cancel)
	return h
}
func (h *loopHarness) frame(t *testing.T, contains string) string {
	t.Helper()
	deadline := time.NewTimer(3 * time.Second)
	defer deadline.Stop()
	last := ""
	for {
		select {
		case last = <-h.sink.frames:
			if strings.Contains(last, contains) {
				return last
			}
		case err := <-h.done:
			t.Fatalf("loop stopped before frame %q: %v", contains, err)
		case <-deadline.C:
			t.Fatalf("no frame %q; last frame:\n%s", contains, last)
		}
	}
}
func (h *loopHarness) stop(t *testing.T) {
	t.Helper()
	h.cancel()
	select {
	case err := <-h.done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("loop did not stop")
	}
}
func signalReceived(t *testing.T, ch <-chan struct{}, name string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(3 * time.Second):
		t.Fatalf("timed out: %s", name)
	}
}

func TestSlowRefreshDoesNotBlockNavigationResizeOrQuit(t *testing.T) {
	started, stopped := make(chan struct{}), make(chan struct{})
	backend := &controlledBackend{fakeBackend: newFakeBackend(spec("FTR-0001", "First feature", feature.Backlog)), loadHook: func(ctx context.Context, _ int) { close(started); <-ctx.Done(); close(stopped) }}
	h := startLoop(t, backend, true)
	signalReceived(t, started, "load started")
	h.keys <- Key{Rune: '?'}
	h.frame(t, "hvb — VirtualBoard on Herdr")
	h.sink.width.Store(80)
	h.resize <- nil
	h.frame(t, "hvb — VirtualBoard on Herdr")
	h.keys <- Key{Rune: 'q'}
	h.frame(t, "BACKLOG")
	h.keys <- Key{Rune: 'q'}
	select {
	case err := <-h.done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("slow load blocked quit")
	}
	signalReceived(t, stopped, "load context cancelled")
	if h.sink.invalidations.Load() != 1 {
		t.Fatal("resize was not handled")
	}
}

func TestRefreshResultKeepsTypedFormAndCurrentSelection(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	base := newFakeBackend(spec("FTR-0001", "First", feature.Backlog), spec("FTR-0002", "Second", feature.Backlog))
	backend := &controlledBackend{fakeBackend: base, loadHook: func(ctx context.Context, n int) {
		if n == 1 {
			close(started)
			select {
			case <-release:
			case <-ctx.Done():
			}
			base.specs[0].Title = "Refreshed first"
		}
	}}
	h := startLoop(t, backend, true)
	signalReceived(t, started, "load started")
	h.keys <- Key{Rune: 'j'}
	h.keys <- Key{Rune: 'n'}
	for _, r := range "Draft café" {
		h.keys <- Key{Rune: r}
	}
	h.frame(t, "Draft café")
	close(release)
	frame := h.frame(t, "Refreshed first")
	if !strings.Contains(frame, "Draft café") || !strings.Contains(frame, "New feature") {
		t.Fatal("refresh closed or reset the active form")
	}
	h.stop(t)
	if h.model.FocusedCard() == nil || h.model.FocusedCard().Spec.ID != "FTR-0002" {
		t.Fatal("delayed snapshot changed the user's selection")
	}
}

func TestQueuedDispatchWaitsForRefreshAndCoalescesRepeatedRequests(t *testing.T) {
	loadStarted, loadRelease, dispatchStarted, dispatchRelease := make(chan struct{}), make(chan struct{}), make(chan struct{}), make(chan struct{})
	backend := &controlledBackend{fakeBackend: newFakeBackend(spec("FTR-0001", "Work", feature.InProgress)),
		loadHook: func(ctx context.Context, n int) {
			if n == 1 {
				close(loadStarted)
				select {
				case <-loadRelease:
				case <-ctx.Done():
				}
			}
		},
		dispatchHook: func(ctx context.Context) error {
			close(dispatchStarted)
			select {
			case <-dispatchRelease:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		},
	}
	h := startLoop(t, backend, true)
	signalReceived(t, loadStarted, "load started")
	h.keys <- Key{Rune: 'd'}
	h.frame(t, "Dispatch FTR-0001")
	h.keys <- Key{Name: KeyEnter}
	h.frame(t, "Dispatching FTR-0001")
	for i := 0; i < 10; i++ {
		h.ticks <- time.Now()
		h.keys <- Key{Rune: 'r'}
	}
	h.keys <- Key{Rune: '?'}
	h.frame(t, "hvb — VirtualBoard on Herdr")
	select {
	case <-dispatchStarted:
		t.Fatal("dispatch ran concurrently with reconciliation")
	default:
	}
	close(loadRelease)
	signalReceived(t, dispatchStarted, "dispatch started")
	h.keys <- Key{Rune: 'q'}
	h.frame(t, "IN PROGRESS")
	h.keys <- Key{Rune: 'd'}
	h.keys <- Key{Rune: '?'}
	h.frame(t, "hvb — VirtualBoard on Herdr")
	close(dispatchRelease)
	h.frame(t, "Dispatched FTR-0001")
	h.stop(t)
	if len(backend.dispatch) != 1 {
		t.Fatalf("duplicate dispatch: %v", backend.dispatch)
	}
	// At most one follow-up refresh can survive keys that were still queued as
	// the action ended; no periodic request accumulates behind a blocked worker.
	if backend.loads.Load() > 3 {
		t.Fatalf("refresh backlog was not coalesced: %d", backend.loads.Load())
	}
}

func TestQuitCancelsSlowDispatch(t *testing.T) {
	started, stopped := make(chan struct{}), make(chan struct{})
	backend := &controlledBackend{fakeBackend: newFakeBackend(spec("FTR-0001", "Work", feature.InProgress)), dispatchHook: func(ctx context.Context) error { close(started); <-ctx.Done(); close(stopped); return ctx.Err() }}
	h := startLoop(t, backend, true)
	h.frame(t, "IN PROGRESS")
	h.keys <- Key{Rune: 'd'}
	h.frame(t, "Dispatch FTR-0001")
	h.keys <- Key{Name: KeyEnter}
	signalReceived(t, started, "dispatch started")
	h.keys <- Key{Rune: 'q'}
	select {
	case err := <-h.done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("dispatch blocked quit")
	}
	signalReceived(t, stopped, "dispatch context cancelled")
}

func TestFailedCreationRestoresEnteredValues(t *testing.T) {
	backend := &controlledBackend{fakeBackend: newFakeBackend(), createHook: func(context.Context) (string, error) { return "", errors.New("creation refused") }}
	h := startLoop(t, backend, true)
	h.frame(t, "BACKLOG")
	h.keys <- Key{Rune: 'n'}
	for _, r := range "Keep this draft" {
		h.keys <- Key{Rune: r}
	}
	h.keys <- Key{Name: KeyEnter}
	frame := h.frame(t, "creation refused")
	if !strings.Contains(frame, "Keep this draft") || !strings.Contains(frame, "New feature") {
		t.Fatal("failed creation lost entered values")
	}
	h.stop(t)
}

func TestPartialCreationDoesNotOfferToCreateDuplicate(t *testing.T) {
	backend := &controlledBackend{fakeBackend: newFakeBackend(), createHook: func(context.Context) (string, error) { return "FTR-9001", errors.New("index failed") }}
	h := startLoop(t, backend, true)
	h.frame(t, "BACKLOG")
	h.keys <- Key{Rune: 'n'}
	h.keys <- Key{Rune: 'x'}
	h.keys <- Key{Name: KeyEnter}
	frame := h.frame(t, "created FTR-9001")
	if strings.Contains(frame, "New feature") {
		t.Fatal("partial success restored a create-again form")
	}
	h.stop(t)
}

func TestMoveCompletionKeepsFilterDraftInsteadOfOpeningDispatch(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	backend := &controlledBackend{fakeBackend: newFakeBackend(spec("FTR-0001", "Work", feature.Backlog)), moveHook: func(ctx context.Context) error {
		close(started)
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}}
	h := startLoop(t, backend, true)
	h.frame(t, "BACKLOG")
	h.keys <- Key{Rune: 'L'}
	signalReceived(t, started, "move started")
	h.keys <- Key{Rune: 'f'}
	for _, r := range "Draft query" {
		h.keys <- Key{Rune: r}
	}
	h.frame(t, "Draft query")
	close(release)
	frame := h.frame(t, "IN PROGRESS 1")
	if !strings.Contains(frame, "Search and filters") || !strings.Contains(frame, "Draft query") {
		t.Fatal("move completion replaced the user's active form")
	}
	h.stop(t)
	if len(backend.moves) != 1 {
		t.Fatal("move did not execute exactly once")
	}
}

func TestSnapshotsDoNotAliasMutableBackendValues(t *testing.T) {
	item := spec("FTR-0001", "Original", feature.InProgress, "backend")
	item.Dependencies = []string{"FTR-0002"}
	run := &runs.Run{ID: "r1", FeatureID: item.ID, Worktree: &runs.Worktree{Path: "original"}, Completion: &runs.CompletionProgress{}, Comments: []runs.Comment{{Body: "original"}}, LastErrors: []string{"original"}, Revision: &runs.Revision{Commit: "original"}}
	backend := newFakeBackend(item)
	backend.runs = []*runs.Run{run}
	snapshot := loadSnapshot(context.Background(), backend)
	item.Title = "Changed"
	item.Labels[0] = "frontend"
	item.Dependencies[0] = "FTR-9999"
	run.Worktree.Path = "changed"
	run.Completion.Done = true
	run.Comments[0].Body = "changed"
	run.LastErrors[0] = "changed"
	run.Revision.Commit = "changed"
	if snapshot.specs[0].Title != "Original" || snapshot.specs[0].Labels[0] != "backend" || snapshot.specs[0].Dependencies[0] != "FTR-0002" {
		t.Fatal("backend writes changed a UI spec snapshot")
	}
	saved := snapshot.runs[0]
	if saved.Worktree.Path != "original" || saved.Completion.Done || saved.Comments[0].Body != "original" || saved.LastErrors[0] != "original" || saved.Revision.Commit != "original" {
		t.Fatal("backend writes changed a UI run snapshot")
	}
}
