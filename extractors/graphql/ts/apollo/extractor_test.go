package apollo

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	cf "github.com/shivamstaq/graph-harness/internal/code_framework"
	"github.com/shivamstaq/graph-harness/internal/kernel"
)

func TestOnEvent_ApolloFixture(t *testing.T) {
	wsdir := t.TempDir()
	tspath := filepath.Join(wsdir, "server.ts")
	src, err := os.ReadFile(filepath.Join("testdata", "server.ts"))
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
	payload, _ := json.Marshal(map[string]any{"path": "server.ts"})
	events, err := ex.OnEvent(context.Background(), kernel.Event{
		Layer: "code.core", Kind: "FileChanged", Payload: payload,
	})
	if err != nil {
		t.Fatalf("OnEvent: %v", err)
	}
	gqlCount, mutCount := 0, 0
	for _, e := range events {
		switch e.Kind {
		case "GraphQLOperationAdded":
			gqlCount++
		case "MutationAdded":
			mutCount++
		}
	}
	if gqlCount != 3 {
		t.Errorf("GraphQLOperationAdded count = %d, want 3", gqlCount)
	}
	if mutCount != 1 {
		t.Errorf("MutationAdded count = %d, want 1", mutCount)
	}
}

func TestOnEvent_NoApolloImportSuppresses(t *testing.T) {
	wsdir := t.TempDir()
	tspath := filepath.Join(wsdir, "schema.ts")
	src := []byte("import { gql } from 'graphql-tag';\nconst t = gql`type Query { ping: String }`;\n")
	if err := os.WriteFile(tspath, src, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	ex, _ := New(cf.Deps{Workspace: wsdir, Logf: func(string, ...any) {}})
	payload, _ := json.Marshal(map[string]any{"path": "schema.ts"})
	events, err := ex.OnEvent(context.Background(), kernel.Event{
		Layer: "code.core", Kind: "FileChanged", Payload: payload,
	})
	if err != nil {
		t.Fatalf("OnEvent: %v", err)
	}
	if len(events) != 0 {
		t.Errorf("expected zero events for non-apollo file, got %d", len(events))
	}
}

func TestRegistered(t *testing.T) {
	_, desc, ok := cf.Lookup(extractorName)
	if !ok {
		t.Fatalf("extractor %q not registered", extractorName)
	}
	if desc.Family != "graphql" {
		t.Errorf("family = %q", desc.Family)
	}
}
