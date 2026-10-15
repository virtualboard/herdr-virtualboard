package tui

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/virtualboard/herdr-virtualboard/internal/config"
	"github.com/virtualboard/herdr-virtualboard/internal/dispatch"
	"github.com/virtualboard/herdr-virtualboard/internal/feature"
	"github.com/virtualboard/herdr-virtualboard/internal/herdrcli"
	"github.com/virtualboard/herdr-virtualboard/internal/roles"
	"github.com/virtualboard/herdr-virtualboard/internal/runs"
	"github.com/virtualboard/herdr-virtualboard/internal/vb"
	"github.com/virtualboard/herdr-virtualboard/internal/workspace"
)

// Backend is everything the board reads and mutates. It is an interface so the
// model can be driven in tests without a VirtualBoard workspace, a vb binary,
// or a running Herdr.
type Backend interface {
	Load(ctx context.Context) ([]*feature.Spec, []*runs.Run, []error)
	Move(ctx context.Context, id string, target feature.Status, owner string) error
	Create(ctx context.Context, title string, labels []string, priority string) (string, error)
	SetField(ctx context.Context, id, key, value string) error
	Dispatch(ctx context.Context, spec *feature.Spec, role, kind string, force bool) (*runs.Run, error)
	// DispatchWith is Dispatch with explicit worktree and pull-request
	// intent; nil for either inherits configuration.
	DispatchWith(ctx context.Context, spec *feature.Spec, role, kind string, worktree, pr *bool) (*runs.Run, error)
	Cancel(ctx context.Context, runID string) error
	Focus(ctx context.Context, runID string) error
	// Metadata accessors must remain memory-only: rendering calls them on
	// the UI goroutine. Put potentially blocking I/O in Load or an action.
	Roles() []roles.Role
	Config() *config.Config
	Owner() string
	ProjectName() string
}

// Card is one feature as the board shows it: the spec plus the run context that
// lives outside the repository.
type Card struct {
	Spec   *feature.Spec
	Runs   []*runs.Run
	Active *runs.Run
}

// View is which screen the board is showing.
type View int

const (
	ViewBoard View = iota
	ViewDetail
	ViewHelp
	ViewNewFeature
	ViewFilters
	ViewMovePicker
	ViewDispatchPicker
	ViewConfirm
)

// Model is the board's whole state. Every render is a pure function of it.
type Model struct {
	backend Backend
	palette Palette

	width  int
	height int

	view View

	// columns holds the cards per status, in board order.
	columns       map[feature.Status][]*Card
	allColumns    map[feature.Status][]*Card
	matchedCounts map[feature.Status]int
	filters       boardFilters
	hideDone      bool
	// column and card are the focused positions.
	column int
	card   map[feature.Status]int

	problems []error
	status   string
	statusAt time.Time
	isError  bool

	helpScroll    int
	detailScroll  int
	detailSection int

	form    *form
	picker  *picker
	confirm *confirmation

	enqueue      func(task) bool
	loading      bool
	pendingLabel string
	failedForm   *form
	inputContext context.Context
	lastLoad     time.Time
	quitting     bool
}

// NewModel builds a board over a backend.
func NewModel(backend Backend, palette Palette) *Model {
	return &Model{
		backend: backend,
		palette: palette,
		columns: map[feature.Status][]*Card{},
		card:    map[feature.Status]int{},
		view:    ViewBoard,
		width:   80,
		height:  24,
	}
}

// Resize records new terminal geometry.
func (m *Model) Resize(width, height int) {
	m.width, m.height = width, height
}

// Reload refreshes the board from the backend, preserving the focused card by
// identity rather than by index: a refresh that silently moves the selection
// because another agent finished a feature is how a user dispatches the wrong
// thing.
func (m *Model) Reload(ctx context.Context) {
	if m.enqueue != nil {
		m.requestReload()
		return
	}
	m.applySnapshot(loadSnapshot(ctx, m.backend))
}

func (m *Model) applySnapshot(snapshot boardSnapshot) {
	focused := m.FocusedCard()
	var focusedID string
	if focused != nil {
		focusedID = focused.Spec.ID
	}

	specs, allRuns, problems := snapshot.specs, snapshot.runs, snapshot.problems
	m.problems = problems

	runsByFeature := map[string][]*runs.Run{}
	for _, run := range allRuns {
		runsByFeature[run.FeatureID] = append(runsByFeature[run.FeatureID], run)
	}

	columns := map[feature.Status][]*Card{}
	for _, spec := range specs {
		card := &Card{Spec: spec, Runs: runsByFeature[spec.ID]}
		for _, run := range card.Runs {
			if run.Active() {
				card.Active = run
				break
			}
		}
		columns[spec.Status] = append(columns[spec.Status], card)
	}
	m.allColumns = columns
	m.rebuildFiltered()
	first := m.lastLoad.IsZero()
	m.lastLoad = time.Now()

	m.clampSelection()
	switch {
	case focusedID != "":
		m.focusFeature(focusedID)
	case first:
		// Opening on an empty backlog while every feature is in review is
		// a board that makes the user navigate before it tells them
		// anything. Start where the work is.
		m.focusFirstPopulatedColumn()
	}
}

// focusFirstPopulatedColumn moves the selection to the leftmost column that has
// cards, leaving it alone when the board is empty.
func (m *Model) focusFirstPopulatedColumn() {
	for index, status := range feature.Statuses {
		if len(m.columns[status]) > 0 {
			m.column = index
			return
		}
	}
}

// focusFeature moves the selection to a feature wherever it now lives.
func (m *Model) focusFeature(id string) bool {
	for columnIndex, status := range feature.Statuses {
		for cardIndex, card := range m.columns[status] {
			if card.Spec.ID == id {
				m.column = columnIndex
				m.card[status] = cardIndex
				return true
			}
		}
	}
	return false
}

// clampSelection keeps the focused indices inside the board after a reload.
func (m *Model) clampSelection() {
	if m.column < 0 {
		m.column = 0
	}
	if m.column >= len(feature.Statuses) {
		m.column = len(feature.Statuses) - 1
	}
	for _, status := range feature.Statuses {
		count := len(m.columns[status])
		index := m.card[status]
		switch {
		case count == 0:
			m.card[status] = 0
		case index >= count:
			m.card[status] = count - 1
		case index < 0:
			m.card[status] = 0
		}
	}
}

// FocusedStatus is the status of the focused column.
func (m *Model) FocusedStatus() feature.Status {
	if m.column < 0 || m.column >= len(feature.Statuses) {
		return feature.Backlog
	}
	return feature.Statuses[m.column]
}

// FocusedCard is the selected card, or nil when the column is empty.
func (m *Model) FocusedCard() *Card {
	status := m.FocusedStatus()
	cards := m.columns[status]
	index := m.card[status]
	if index < 0 || index >= len(cards) {
		return nil
	}
	return cards[index]
}

// Cards returns a column's cards.
func (m *Model) Cards(status feature.Status) []*Card { return m.columns[status] }

// Counts returns the number of cards per status.
func (m *Model) Counts() map[feature.Status]int {
	out := map[feature.Status]int{}
	for _, status := range feature.Statuses {
		out[status] = m.matchedCounts[status]
	}
	return out
}

// ActiveRunCount is how many runs currently hold a pane.
func (m *Model) ActiveRunCount() int {
	count := 0
	for _, status := range feature.Statuses {
		for _, card := range m.allColumns[status] {
			if card.Active != nil {
				count++
			}
		}
	}
	return count
}

// Quitting reports whether the event loop should stop.
func (m *Model) Quitting() bool { return m.quitting }

// setStatus posts a transient message to the footer.
func (m *Model) setStatus(format string, args ...any) {
	m.status, m.isError, m.statusAt = fmt.Sprintf(format, args...), false, time.Now()
}

func (m *Model) setError(err error) {
	if err == nil {
		return
	}
	m.status, m.isError, m.statusAt = err.Error(), true, time.Now()
}

// statusMessage returns the footer message, expiring it after a while so a
// stale "dispatched" does not sit under a board that has moved on.
func (m *Model) statusMessage() (string, bool) {
	if m.status == "" || time.Since(m.statusAt) > statusTTL {
		return "", false
	}
	return m.status, m.isError
}

const statusTTL = 12 * time.Second

// sortRunsNewestFirst is used by the detail view, which shows history in the
// order a human reads it.
func sortRunsNewestFirst(list []*runs.Run) []*runs.Run {
	out := append([]*runs.Run(nil), list...)
	sort.SliceStable(out, func(i, j int) bool { return out[i].StartedAt.After(out[j].StartedAt) })
	return out
}

// liveBackend is the production Backend, over a real workspace.
type liveBackend struct {
	workspace  *workspace.Workspace
	config     *config.Config
	vb         *vb.Client
	herdr      *herdrcli.Client
	store      *runs.Store
	dispatcher *dispatch.Dispatcher
	roles      []roles.Role
}

// NewBackend builds the production backend.
func NewBackend(
	ws *workspace.Workspace,
	cfg *config.Config,
	vbClient *vb.Client,
	herdrClient *herdrcli.Client,
	store *runs.Store,
	dispatcher *dispatch.Dispatcher,
	loadedRoles []roles.Role,
) Backend {
	return &liveBackend{
		workspace: ws, config: cfg, vb: vbClient, herdr: herdrClient,
		store: store, dispatcher: dispatcher, roles: loadedRoles,
	}
}

func (b *liveBackend) Load(ctx context.Context) ([]*feature.Spec, []*runs.Run, []error) {
	var problems []error
	// Reconciling before reading is what keeps the board honest without a
	// daemon: a run whose pane closed is settled here, not left showing as
	// live until someone notices.
	if _, err := b.dispatcher.Reconcile(ctx); err != nil {
		problems = append(problems, err)
	}
	specs, specProblems := b.workspace.LoadSpecs()
	problems = append(problems, specProblems...)
	allRuns, err := b.store.List()
	if err != nil {
		problems = append(problems, err)
	}
	return specs, allRuns, problems
}

func (b *liveBackend) Move(ctx context.Context, id string, target feature.Status, owner string) error {
	if _, err := b.vb.Move(ctx, id, target, owner); err != nil {
		return err
	}
	if err := b.vb.Index(ctx); err != nil {
		return fmt.Errorf("moved %s, but the index is stale: %w", id, err)
	}
	if _, err := b.dispatcher.AutoStart(ctx, id); err != nil {
		return fmt.Errorf("moved %s, but automatic dispatch failed: %w", id, err)
	}
	return nil
}

func (b *liveBackend) Create(ctx context.Context, title string, labels []string, priority string) (string, error) {
	created, err := b.vb.New(ctx, title, labels...)
	if err != nil {
		return "", err
	}
	if priority != "" {
		if err := b.vb.Update(ctx, created.ID, map[string]string{"priority": priority}, nil); err != nil {
			return created.ID, err
		}
	}
	if err := b.vb.Index(ctx); err != nil {
		return created.ID, fmt.Errorf("created %s, but the index is stale: %w", created.ID, err)
	}
	return created.ID, nil
}

func (b *liveBackend) SetField(ctx context.Context, id, key, value string) error {
	if err := b.vb.Update(ctx, id, map[string]string{key: value}, nil); err != nil {
		return err
	}
	if err := b.vb.Index(ctx); err != nil {
		return fmt.Errorf("updated %s, but the index is stale: %w", id, err)
	}
	return nil
}

func (b *liveBackend) Dispatch(ctx context.Context, spec *feature.Spec, role, kind string, force bool) (*runs.Run, error) {
	return b.dispatcher.Start(ctx, dispatch.Request{Spec: spec, Role: role, Kind: kind, Force: force})
}

func (b *liveBackend) DispatchWith(ctx context.Context, spec *feature.Spec, role, kind string, worktree, pr *bool) (*runs.Run, error) {
	return b.dispatcher.Start(ctx, dispatch.Request{
		Spec: spec, Role: role, Kind: kind, Worktree: worktree, PR: pr,
	})
}

func (b *liveBackend) Cancel(ctx context.Context, runID string) error {
	_, err := b.dispatcher.Cancel(ctx, runID, "cancelled from the board")
	return err
}

func (b *liveBackend) Focus(ctx context.Context, runID string) error {
	return b.dispatcher.Focus(ctx, runID)
}

func (b *liveBackend) Roles() []roles.Role    { return b.roles }
func (b *liveBackend) Config() *config.Config { return b.config }
func (b *liveBackend) Owner() string          { return b.config.ResolveOwner() }
func (b *liveBackend) ProjectName() string    { return b.workspace.Name() }
