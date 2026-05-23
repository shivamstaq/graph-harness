package strawberry

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	cf "github.com/shivamstaq/graph-harness/internal/code_framework"
	"github.com/shivamstaq/graph-harness/internal/kernel"
)

func TestOnEvent_StrawberryFixture(t *testing.T) {
	wsdir := t.TempDir()
	pypath := filepath.Join(wsdir, "schema.py")
	src, err := os.ReadFile(filepath.Join("testdata", "schema.py"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	if err := os.WriteFile(pypath, src, 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	ex, err := New(cf.Deps{Workspace: wsdir, Logf: func(string, ...any) {}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	payload, _ := json.Marshal(map[string]any{"path": "schema.py"})
	events, err := ex.OnEvent(context.Background(), kernel.Event{
		Layer: "code.core", Kind: "FileChanged", Payload: payload,
	})
	if err != nil {
		t.Fatalf("OnEvent: %v", err)
	}
	got := map[string]string{}
	for _, e := range events {
		if e.Kind != "GraphQLOperationAdded" {
			continue
		}
		var p map[string]any
		if err := json.Unmarshal(e.Payload, &p); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		got[p["name"].(string)] = p["operation_type"].(string)
	}
	want := map[string]string{
		"user":        "query",
		"users":       "query",
		"create_user": "mutation",
		"user_added":  "subscription",
	}
	for n, op := range want {
		if got[n] != op {
			t.Errorf("op %q: got %q, want %q", n, got[n], op)
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
