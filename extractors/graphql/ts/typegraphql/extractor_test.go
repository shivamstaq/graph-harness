package typegraphql

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	cf "github.com/shivamstaq/graph-harness/internal/code_framework"
	"github.com/shivamstaq/graph-harness/internal/kernel"
)

func TestOnEvent_TypeGraphQLFixture(t *testing.T) {
	wsdir := t.TempDir()
	tspath := filepath.Join(wsdir, "resolver.ts")
	src, err := os.ReadFile(filepath.Join("testdata", "resolver.ts"))
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
	payload, _ := json.Marshal(map[string]any{"path": "resolver.ts"})
	events, err := ex.OnEvent(context.Background(), kernel.Event{
		Layer: "code.core", Kind: "FileChanged", Payload: payload,
	})
	if err != nil {
		t.Fatalf("OnEvent: %v", err)
	}
	names := map[string]string{}
	for _, e := range events {
		if e.Kind != "GraphQLOperationAdded" {
			continue
		}
		var p map[string]any
		if err := json.Unmarshal(e.Payload, &p); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		names[p["name"].(string)] = p["operation_type"].(string)
	}
	want := map[string]string{
		"user":       "query",
		"users":      "query",
		"createUser": "mutation",
		"userAdded":  "subscription",
	}
	for n, op := range want {
		if got := names[n]; got != op {
			t.Errorf("op %q: got %q, want %q", n, got, op)
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
