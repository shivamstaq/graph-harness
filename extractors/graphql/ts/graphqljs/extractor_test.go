package graphqljs

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	cf "github.com/shivamstaq/graph-harness/internal/code_framework"
	"github.com/shivamstaq/graph-harness/internal/kernel"
)

func TestExtract_FixtureSchemaAndResolvers(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("testdata", "schema.ts"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	ops := Extract("schema.ts", string(src))
	// Expect 4 ops: user, users, createUser, userAdded.
	wantNames := map[string]string{
		"user":       "query",
		"users":      "query",
		"createUser": "mutation",
		"userAdded":  "subscription",
	}
	if len(ops) != len(wantNames) {
		t.Fatalf("ops count = %d, want %d. ops=%+v", len(ops), len(wantNames), ops)
	}
	for _, o := range ops {
		w, ok := wantNames[o.Name]
		if !ok {
			t.Errorf("unexpected op %q", o.Name)
			continue
		}
		if o.OperationType != w {
			t.Errorf("op %q: type=%q want %q", o.Name, o.OperationType, w)
		}
		if len(o.AnchoredTo.Anchors) == 0 {
			t.Errorf("op %q: empty AnchoredTo", o.Name)
		}
		if len(o.ResolverRef.Anchors) == 0 {
			t.Errorf("op %q: empty ResolverRef", o.Name)
		}
	}
}

func TestOnEvent_EmitsGraphQLOperationAndMutation(t *testing.T) {
	wsdir := t.TempDir()
	tspath := filepath.Join(wsdir, "schema.ts")
	src, err := os.ReadFile(filepath.Join("testdata", "schema.ts"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	if err := os.WriteFile(tspath, src, 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	ex, err := New(cf.Deps{Workspace: wsdir, Logf: func(string, ...any) {}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	payload, _ := json.Marshal(map[string]any{"path": "schema.ts"})
	events, err := ex.OnEvent(context.Background(), kernel.Event{
		Layer: "code.core", Kind: "FileChanged", Payload: payload,
	})
	if err != nil {
		t.Fatalf("OnEvent: %v", err)
	}
	// Expect 4 GraphQLOperationAdded + 1 MutationAdded (one mutation field).
	gqlCount, mutCount := 0, 0
	for _, e := range events {
		switch e.Kind {
		case "GraphQLOperationAdded":
			gqlCount++
		case "MutationAdded":
			mutCount++
		}
	}
	if gqlCount != 4 {
		t.Errorf("GraphQLOperationAdded count = %d, want 4", gqlCount)
	}
	if mutCount != 1 {
		t.Errorf("MutationAdded count = %d, want 1", mutCount)
	}
}

func TestOnEvent_NonMatchingFileNoEmit(t *testing.T) {
	wsdir := t.TempDir()
	tspath := filepath.Join(wsdir, "irrelevant.ts")
	if err := os.WriteFile(tspath, []byte("export const x = 1;\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	ex, _ := New(cf.Deps{Workspace: wsdir, Logf: func(string, ...any) {}})
	payload, _ := json.Marshal(map[string]any{"path": "irrelevant.ts"})
	events, err := ex.OnEvent(context.Background(), kernel.Event{
		Layer: "code.core", Kind: "FileChanged", Payload: payload,
	})
	if err != nil {
		t.Fatalf("OnEvent: %v", err)
	}
	if len(events) != 0 {
		t.Errorf("expected no events, got %d: %+v", len(events), events)
	}
}

func TestRegistered(t *testing.T) {
	// init() registers; ensure the registry contains us with the right
	// descriptor shape.
	_, desc, ok := cf.Lookup(extractorName)
	if !ok {
		t.Fatalf("extractor %q not registered", extractorName)
	}
	if desc.Family != "graphql" {
		t.Errorf("family = %q", desc.Family)
	}
}
