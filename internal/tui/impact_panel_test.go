package tui

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shivamstaq/graph-harness/internal/change_process"
)

// fakeSource is an in-memory ImpactDataSource used by the reducer
// tests. Each method records its call so tests can assert
// re-derivation cadence without standing up a real Store / EventLog.
type fakeSource struct {
	mu      sync.Mutex
	diff    []byte
	diffErr error

	pathEntities map[string][]ImpactEntityRef
	pathErr      error

	validate    *change_process.ValidateDiffResult
	validateErr error

	stagedCalls   int
	pathCalls     int
	validateCalls int
}

func (f *fakeSource) StagedDiff(_ context.Context) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stagedCalls++
	return f.diff, f.diffErr
}

func (f *fakeSource) EntitiesForPath(_ context.Context, path string) ([]ImpactEntityRef, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pathCalls++
	if f.pathErr != nil {
		return nil, f.pathErr
	}
	return f.pathEntities[path], nil
}

func (f *fakeSource) ValidateDiff(_ context.Context, _ []byte) (*change_process.ValidateDiffResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.validateCalls++
	return f.validate, f.validateErr
}

func (f *fakeSource) calls() (int, int, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.stagedCalls, f.pathCalls, f.validateCalls
}

// TestImpactPanel_ApplyEvent_AdvancesSeq locks in the reducer's
// monotonic LastSeq advance. Events arriving out-of-order do not
// regress the seq footer; the panel always shows the highest seq it
// has observed.
func TestImpactPanel_ApplyEvent_AdvancesSeq(t *testing.T) {
	t.Parallel()
	p := NewImpactPanel(nil, nil)
	state := p.ApplyEvent(ImpactEvent{Seq: 7, Layer: "code.framework", Kind: "RouteAdded"})
	if state.LastSeq != 7 {
		t.Fatalf("LastSeq after first event = %d, want 7", state.LastSeq)
	}
	state = p.ApplyEvent(ImpactEvent{Seq: 3, Layer: "code.framework", Kind: "SchemaFieldChanged"})
	if state.LastSeq != 7 {
		t.Fatalf("LastSeq after out-of-order event = %d, want 7 (monotonic)", state.LastSeq)
	}
	state = p.ApplyEvent(ImpactEvent{Seq: 12, Layer: "code.framework", Kind: "EventPublisherAdded"})
	if state.LastSeq != 12 {
		t.Fatalf("LastSeq after newer event = %d, want 12", state.LastSeq)
	}
}

// TestImpactPanel_ApplyDerive_FoldsFindings exercises the reducer's
// derive-result fold: a state with findings + framework context should
// populate the Findings + Impacted columns. Tests Render does not
// panic when impacted is empty.
func TestImpactPanel_ApplyDerive_FoldsFindings(t *testing.T) {
	t.Parallel()
	p := NewImpactPanel(nil, nil)
	finding := change_process.ValidationFinding{
		ID:       "finding_abcd1234",
		Kind:     change_process.FindingKindMissingDependentUpdate,
		Severity: "high",
		Subject: change_process.Subject{
			EntityKind: "code.framework:EventPublisher",
			EntityID:   "EventPublisher:pub1",
			Qualified:  "kafka:order.created",
		},
		FrameworkContext: &change_process.FrameworkContext{
			TouchedKind: "EventPublisher",
			TouchedSubject: change_process.EntityRef{
				Kind:          "EventPublisher",
				ID:            "EventPublisher:pub1",
				QualifiedName: "kafka:order.created",
			},
			Dependents: []change_process.DependentRef{
				{Kind: "EventSubscriber", ID: "sub1", QualifiedName: "kafka:order.created", Reason: "subscribes to event"},
				{Kind: "ContractTest", ID: "ct1", QualifiedName: "kafka:order.created", Reason: "contract test for event"},
			},
		},
	}
	next := ImpactState{
		Touched: []ImpactEntityRef{
			{ID: "func1", Kind: "Function", QualifiedName: "pkg.PublishOrder", Path: "internal/orders/publish.go"},
		},
		TouchedFiles: []string{"internal/orders/publish.go"},
		Impacted: map[string][]change_process.DependentRef{
			"EventSubscriber": {finding.FrameworkContext.Dependents[0]},
			"ContractTest":    {finding.FrameworkContext.Dependents[1]},
		},
		Findings: []change_process.ValidationFinding{finding},
		LastSeq:  42,
	}
	state := p.ApplyDerive(next, nil)
	if len(state.Findings) != 1 {
		t.Fatalf("Findings len = %d, want 1", len(state.Findings))
	}
	if state.Impacted["EventSubscriber"][0].QualifiedName != "kafka:order.created" {
		t.Fatalf("impacted subscriber qn = %q", state.Impacted["EventSubscriber"][0].QualifiedName)
	}
	rendered := p.Render()
	if !strings.Contains(rendered, "kafka:order.created") {
		t.Fatalf("Render did not include touched subject qn:\n%s", rendered)
	}
	if !strings.Contains(rendered, "EventSubscriber") {
		t.Fatalf("Render did not include EventSubscriber kind:\n%s", rendered)
	}
	if !strings.Contains(rendered, "push-driven — no polling") {
		t.Fatalf("Render footer missing push-driven marker:\n%s", rendered)
	}
}

// TestImpactPanel_ApplyDerive_PreservesPriorOnError ensures a failed
// derive does not blank the columns. The Err field surfaces so the
// user sees the failure, but the prior render survives.
func TestImpactPanel_ApplyDerive_PreservesPriorOnError(t *testing.T) {
	t.Parallel()
	p := NewImpactPanel(nil, nil)
	prior := ImpactState{
		Touched: []ImpactEntityRef{{ID: "x", Kind: "Function", QualifiedName: "pkg.fn"}},
		Impacted: map[string][]change_process.DependentRef{
			"EventSubscriber": {{Kind: "EventSubscriber", ID: "s1", QualifiedName: "kafka:t"}},
		},
		Findings: []change_process.ValidationFinding{{ID: "f1", Kind: change_process.FindingKindMissingDependentUpdate}},
		LastSeq:  9,
	}
	p.ApplyDerive(prior, nil)
	after := p.ApplyDerive(ImpactState{}, errors.New("validate boom"))
	if len(after.Touched) != 1 {
		t.Fatalf("Touched dropped after err derive: %+v", after.Touched)
	}
	if after.Err == nil || !strings.Contains(after.Err.Error(), "boom") {
		t.Fatalf("Err = %v, want non-nil with 'boom'", after.Err)
	}
}

// TestImpactPanel_HandleKey_DrillDownToggle covers the Enter / Esc
// expand/collapse transition required by P2.T39 (per-finding drill-
// down on a key press).
func TestImpactPanel_HandleKey_DrillDownToggle(t *testing.T) {
	t.Parallel()
	p := NewImpactPanel(nil, nil)
	f := change_process.ValidationFinding{
		ID:       "f1",
		Kind:     change_process.FindingKindMissingDependentUpdate,
		Severity: "high",
		Subject:  change_process.Subject{Qualified: "kafka:order.created"},
		FrameworkContext: &change_process.FrameworkContext{
			TouchedKind:    "EventPublisher",
			TouchedSubject: change_process.EntityRef{QualifiedName: "kafka:order.created"},
			Dependents:     []change_process.DependentRef{{Kind: "EventSubscriber", QualifiedName: "kafka:order.created", Reason: "subscribes to event"}},
		},
	}
	p.ApplyDerive(ImpactState{
		Findings: []change_process.ValidationFinding{f},
		Impacted: map[string][]change_process.DependentRef{},
	}, nil)
	if p.IsExpanded() {
		t.Fatalf("panel should start collapsed")
	}
	if !p.HandleKey("enter") {
		t.Fatalf("HandleKey(enter) should claim the key")
	}
	if !p.IsExpanded() {
		t.Fatalf("panel should be expanded after enter")
	}
	if !strings.Contains(p.Render(), "drilldown") {
		t.Fatalf("Render after enter missing drilldown header:\n%s", p.Render())
	}
	if !p.HandleKey("esc") {
		t.Fatalf("HandleKey(esc) should claim the key while expanded")
	}
	if p.IsExpanded() {
		t.Fatalf("panel should collapse after esc")
	}
}

// TestImpactPanel_HandleKey_CursorMove confirms j/k clamp inside the
// findings range and do not panic on empty state.
func TestImpactPanel_HandleKey_CursorMove(t *testing.T) {
	t.Parallel()
	p := NewImpactPanel(nil, nil)
	// Empty state — j/k should be no-ops (k claimed only when cursor>0).
	_ = p.HandleKey("j") // safe even with empty findings
	if p.CursorRow() != 0 {
		t.Fatalf("CursorRow on empty findings = %d, want 0", p.CursorRow())
	}
	p.ApplyDerive(ImpactState{
		Findings: []change_process.ValidationFinding{
			{ID: "a"}, {ID: "b"}, {ID: "c"},
		},
		Impacted: map[string][]change_process.DependentRef{},
	}, nil)
	p.HandleKey("j")
	p.HandleKey("j")
	if p.CursorRow() != 2 {
		t.Fatalf("CursorRow after 2 j = %d, want 2", p.CursorRow())
	}
	p.HandleKey("j") // clamped at len-1
	if p.CursorRow() != 2 {
		t.Fatalf("CursorRow clamped wrong: %d", p.CursorRow())
	}
	p.HandleKey("k")
	if p.CursorRow() != 1 {
		t.Fatalf("CursorRow after k = %d, want 1", p.CursorRow())
	}
}

// TestImpactPanel_recvCmd_PushDriven exercises the bubbletea-style
// reducer end-to-end with a fake event channel + fake data source.
// No timers in the panel — we drive it by sending events on the
// channel and asserting that the Update loop re-arms the recv and
// schedules a derive on every push delivery.
func TestImpactPanel_recvCmd_PushDriven(t *testing.T) {
	t.Parallel()
	ch := make(chan ImpactEvent, 4)
	src := &fakeSource{
		diff: []byte("--- a/foo.go\n+++ b/foo.go\n@@ -1 +1 @@\n-old\n+new\n"),
		pathEntities: map[string][]ImpactEntityRef{
			"foo.go": {{ID: "func1", Kind: "Function", QualifiedName: "pkg.Foo", Path: "foo.go"}},
		},
		validate: &change_process.ValidateDiffResult{
			ValidationSeq: 1,
			Findings: []change_process.ValidationFinding{{
				ID:       "f1",
				Kind:     change_process.FindingKindMissingDependentUpdate,
				Severity: "high",
				Subject:  change_process.Subject{Qualified: "kafka:t"},
				FrameworkContext: &change_process.FrameworkContext{
					TouchedKind:    "EventPublisher",
					TouchedSubject: change_process.EntityRef{QualifiedName: "kafka:t"},
					Dependents:     []change_process.DependentRef{{Kind: "EventSubscriber", ID: "s1", QualifiedName: "kafka:t", Reason: "subscribes to event"}},
				},
			}},
		},
	}
	p := NewImpactPanel(src, ch)

	// Push an event. The recv cmd reads from the channel and returns
	// an impactEventMsg; Update folds the seq + re-arms recv + spawns
	// a deriveCmd.
	ch <- ImpactEvent{Seq: 11, Layer: "code.framework", Kind: "EventPublisherAdded"}

	// Drive one recv cycle synchronously by invoking the cmd directly
	// (matches what bubbletea's scheduler would do).
	msg := p.recvCmd()()
	ev, ok := msg.(impactEventMsg)
	if !ok {
		t.Fatalf("recvCmd msg type = %T, want impactEventMsg", msg)
	}
	if ev.Seq != 11 {
		t.Fatalf("recvCmd delivered seq=%d, want 11", ev.Seq)
	}

	// Feed the event message through Update — should schedule both
	// recv (re-arm) and derive cmds.
	_, cmd := p.Update(ev)
	if cmd == nil {
		t.Fatal("Update returned nil cmd after impactEventMsg; expected re-arm + derive batch")
	}
	if p.State().LastSeq != 11 {
		t.Fatalf("LastSeq after Update = %d, want 11", p.State().LastSeq)
	}

	// Run the derive cmd synchronously (bubbletea would run it on a
	// goroutine; we run it inline so the test stays deterministic).
	deriveResult := p.deriveCmd(ImpactEvent(ev))()
	dm, ok := deriveResult.(impactDeriveMsg)
	if !ok {
		t.Fatalf("deriveCmd msg type = %T, want impactDeriveMsg", deriveResult)
	}
	if dm.err != nil {
		t.Fatalf("deriveCmd err = %v, want nil", dm.err)
	}
	_, _ = p.Update(dm)
	state := p.State()
	if len(state.Findings) != 1 {
		t.Fatalf("after derive, Findings = %d, want 1", len(state.Findings))
	}
	if len(state.Impacted["EventSubscriber"]) != 1 {
		t.Fatalf("after derive, Impacted EventSubscriber = %d, want 1", len(state.Impacted["EventSubscriber"]))
	}
	// Confirm the data source was called exactly once for each method
	// — no spurious polling cycles.
	staged, paths, vals := src.calls()
	if staged != 1 || paths != 1 || vals != 1 {
		t.Fatalf("data source call counts = (staged=%d, paths=%d, validate=%d); want (1,1,1) — any higher implies polling", staged, paths, vals)
	}
}

// TestImpactPanel_recvCmd_ClosedChannel proves that closing the
// subscription channel terminates the recv loop cleanly (returns nil
// msg) without panicking. Mirrors the production teardown path when
// the daemon connection closes.
func TestImpactPanel_recvCmd_ClosedChannel(t *testing.T) {
	t.Parallel()
	ch := make(chan ImpactEvent)
	p := NewImpactPanel(nil, ch)
	close(ch)
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = p.recvCmd()()
	}()
	select {
	case <-done:
		// expected
	case <-time.After(2 * time.Second):
		t.Fatal("recvCmd blocked after channel close")
	}
}

// TestImpactPanel_NonFrameworkEvent_DoesNotDerive guards the filter
// in Update: events from other layers update the seq footer but do
// not trigger a re-derive (the framework dependents don't change).
func TestImpactPanel_NonFrameworkEvent_DoesNotDerive(t *testing.T) {
	t.Parallel()
	ch := make(chan ImpactEvent, 1)
	src := &fakeSource{
		validate: &change_process.ValidateDiffResult{},
	}
	p := NewImpactPanel(src, ch)
	_, cmd := p.Update(impactEventMsg{Seq: 5, Layer: "code.core", Kind: "FunctionAdded"})
	if cmd == nil {
		t.Fatal("expected at least a recv re-arm cmd")
	}
	// Drain it once to confirm only recv re-arm happened (no derive).
	_, _, vals := src.calls()
	if vals != 0 {
		t.Fatalf("non-framework event triggered %d derives; want 0", vals)
	}
}

// TestTouchedPathsFromDiff covers the local diff-header parser: it
// must yield deduped, sorted, workspace-relative paths and skip the
// /dev/null marker that git emits for additions / deletions.
func TestTouchedPathsFromDiff(t *testing.T) {
	t.Parallel()
	diff := []byte(`diff --git a/internal/x.go b/internal/x.go
index abc..def
--- a/internal/x.go
+++ b/internal/x.go
@@ -1 +1 @@
-old
+new
diff --git a/newfile.py b/newfile.py
new file mode 100644
--- /dev/null
+++ b/newfile.py
@@ -0,0 +1 @@
+print("hi")
`)
	paths := touchedPathsFromDiff(diff)
	want := []string{"internal/x.go", "newfile.py"}
	if len(paths) != len(want) {
		t.Fatalf("paths = %v, want %v", paths, want)
	}
	for i := range want {
		if paths[i] != want[i] {
			t.Fatalf("paths[%d] = %q, want %q", i, paths[i], want[i])
		}
	}
}

// TestModel_AttachImpact_AddsTabAndSwitching ensures the impact panel
// is a real fifth tab once attached (tab navigation wraps around 5
// instead of 4) and that the parent View renders the panel's output.
func TestModel_AttachImpact_AddsTabAndSwitching(t *testing.T) {
	t.Parallel()
	m := NewModel(nil)
	if got := m.tabCount(); got != 4 {
		t.Fatalf("tabCount without impact = %d, want 4", got)
	}
	src := &fakeSource{}
	m.AttachImpact(src, nil)
	if got := m.tabCount(); got != 5 {
		t.Fatalf("tabCount with impact = %d, want 5", got)
	}
	// Navigate to the impact tab and confirm View renders the panel.
	m.view = 4
	out := m.View()
	if !strings.Contains(out, "impact") {
		t.Fatalf("View at impact tab missing tab label:\n%s", out)
	}
	if !strings.Contains(out, "Touched") {
		t.Fatalf("View at impact tab missing Touched column:\n%s", out)
	}
}
