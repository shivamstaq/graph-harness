package gqlgen

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	cf "github.com/shivamstaq/graph-harness/internal/code_framework"
	"github.com/shivamstaq/graph-harness/internal/kernel"
)

func TestOnEvent_SchemaSDL(t *testing.T) {
	wsdir := t.TempDir()
	dst := filepath.Join(wsdir, "schema.graphql")
	src, err := os.ReadFile(filepath.Join("testdata", "schema.graphql"))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if err := os.WriteFile(dst, src, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	ex, _ := New(cf.Deps{Workspace: wsdir, Logf: func(string, ...any) {}})
	payload, _ := json.Marshal(map[string]any{"path": "schema.graphql"})
	events, err := ex.OnEvent(context.Background(), kernel.Event{
		Layer: "code.core", Kind: "FileChanged", Payload: payload,
	})
	if err != nil {
		t.Fatalf("OnEvent: %v", err)
	}
	gqlCount, mutCount := 0, 0
	got := map[string]string{}
	for _, e := range events {
		switch e.Kind {
		case "GraphQLOperationAdded":
			gqlCount++
			var p map[string]any
			if err := json.Unmarshal(e.Payload, &p); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			got[p["name"].(string)] = p["operation_type"].(string)
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
	want := map[string]string{
		"user":       "query",
		"users":      "query",
		"createUser": "mutation",
		"userAdded":  "subscription",
	}
	for n, op := range want {
		if got[n] != op {
			t.Errorf("op %q: got %q, want %q", n, got[n], op)
		}
	}
}

func TestOnEvent_ResolversGo(t *testing.T) {
	wsdir := t.TempDir()
	dst := filepath.Join(wsdir, "schema.resolvers.go")
	src, err := os.ReadFile(filepath.Join("testdata", "schema.resolvers.go"))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if err := os.WriteFile(dst, src, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	ex, _ := New(cf.Deps{Workspace: wsdir, Logf: func(string, ...any) {}})
	payload, _ := json.Marshal(map[string]any{"path": "schema.resolvers.go"})
	events, err := ex.OnEvent(context.Background(), kernel.Event{
		Layer: "code.core", Kind: "FileChanged", Payload: payload,
	})
	if err != nil {
		t.Fatalf("OnEvent: %v", err)
	}
	gqlCount, mutCount := 0, 0
	got := map[string]string{}
	for _, e := range events {
		switch e.Kind {
		case "GraphQLOperationAdded":
			gqlCount++
			var p map[string]any
			if err := json.Unmarshal(e.Payload, &p); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			got[p["name"].(string)] = p["operation_type"].(string)
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
	want := map[string]string{
		"user":       "query",
		"users":      "query",
		"createUser": "mutation",
		"userAdded":  "subscription",
	}
	for n, op := range want {
		if got[n] != op {
			t.Errorf("op %q: got %q, want %q (all=%v)", n, got[n], op, got)
		}
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
