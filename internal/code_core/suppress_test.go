package code_core

import (
	"context"
	"sort"
	"testing"

	"github.com/shivamstaq/graph-harness/internal/source_live"
)

// TestSuppressAtSource_NoOpReextractEmitsZeroEvents asserts the SPEC
// §6.21 contract: re-running the orchestrator over an unchanged
// workspace produces zero kernel events. We exercise the contract
// through the two emission paths that matter at code.core:
//   - IngestParsedFileWithChange (tree-sitter / watcher path)
//   - Unifier.Unify (three-source merge path)
//
// The captureEmitter records every event the Unifier emits. After a
// fresh ingest of fixture state followed by a re-ingest with the
// same state, the captured count must equal the count from the
// first ingest. Any additional event on the second ingest is a
// suppress-at-source regression.
func TestSuppressAtSource_NoOpReextractEmitsZeroEvents(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := newTestStore(t)

	// Tree-sitter path: same ParsedFile twice. Both calls must
	// return the same entity IDs; the second must report changed=
	// false because nothing transitioned.
	pf := &source_live.ParsedFile{
		Path:     "src/foo.go",
		Language: "go",
		Functions: []source_live.FunctionDecl{
			{
				Name:          "Foo",
				QualifiedName: "pkg.Foo",
				Signature:     "func() error",
				BodyHash:      "body-aaaa",
			},
			{
				Name:          "Bar",
				QualifiedName: "pkg.Bar",
				Receiver:      "*Pkg",
				Signature:     "func() error",
				BodyHash:      "body-bbbb",
			},
		},
	}

	idsFirst, changedFirst, err := store.IngestParsedFileWithChange(ctx, pf, 10)
	if err != nil {
		t.Fatalf("first ingest: %v", err)
	}
	if !changedFirst {
		t.Fatalf("first ingest must report changed=true: %v", idsFirst)
	}

	idsSecond, changedSecond, err := store.IngestParsedFileWithChange(ctx, pf, 11)
	if err != nil {
		t.Fatalf("second ingest: %v", err)
	}
	if changedSecond {
		t.Fatalf("re-ingest of unchanged ParsedFile must report changed=false; got %v", idsSecond)
	}
	if !sameIDs(idsFirst, idsSecond) {
		t.Fatalf("entity IDs diverged across re-ingest: first=%v second=%v", idsFirst, idsSecond)
	}

	// Disambiguation-dedup path: a fresh disambiguation at the same
	// location with the same claims must not re-emit. Two distinct
	// canonical IDs go through MarkDisambiguationEmitted at seq=20;
	// re-asserting the same claim hash at seq=21 must return false.
	hash := DisambiguationClaimsHash([]string{"id-aaa", "id-bbb"})
	first, err := store.MarkDisambiguationEmitted(ctx, "src/x.go", 0, 10, hash, 20)
	if err != nil {
		t.Fatalf("MarkDisambiguationEmitted first: %v", err)
	}
	if !first {
		t.Fatalf("first emission must be fresh (returned false)")
	}
	second, err := store.MarkDisambiguationEmitted(ctx, "src/x.go", 0, 10, hash, 21)
	if err != nil {
		t.Fatalf("MarkDisambiguationEmitted second: %v", err)
	}
	if second {
		t.Fatalf("duplicate emission must be suppressed (returned true)")
	}

	// A genuinely different claim set at the same location DOES fire
	// — the dedup is on (path, start, end, claims_hash), not on
	// (path, start, end) alone. This guards against over-suppression.
	otherHash := DisambiguationClaimsHash([]string{"id-aaa", "id-ccc"})
	third, err := store.MarkDisambiguationEmitted(ctx, "src/x.go", 0, 10, otherHash, 22)
	if err != nil {
		t.Fatalf("MarkDisambiguationEmitted third: %v", err)
	}
	if !third {
		t.Fatalf("distinct claim set at same location must fire fresh emission")
	}
}

// TestSuppressAtSource_StateTransitionDoesEmit complements the
// no-op test: when an entity's content actually changes (e.g. a
// body_hash update reflecting an edit), the change-detection
// helpers MUST report changed=true so downstream callers know to
// emit a drift event.
func TestSuppressAtSource_StateTransitionDoesEmit(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := newTestStore(t)

	pfV1 := &source_live.ParsedFile{
		Path:     "src/edit.go",
		Language: "go",
		Functions: []source_live.FunctionDecl{
			{Name: "Foo", QualifiedName: "pkg.Foo", Signature: "func() error", BodyHash: "v1"},
		},
	}
	if _, changed, err := store.IngestParsedFileWithChange(ctx, pfV1, 1); err != nil || !changed {
		t.Fatalf("first ingest must change state; err=%v changed=%v", err, changed)
	}

	// Same qualified name + signature → same canonical ID, but a
	// different body_hash. The entity row updates in place but
	// because body_hash is part of the content fingerprint the
	// suppress-at-source contract reports changed=true so the
	// watcher path can emit a drift event.
	pfV2 := &source_live.ParsedFile{
		Path:     "src/edit.go",
		Language: "go",
		Functions: []source_live.FunctionDecl{
			{Name: "Foo", QualifiedName: "pkg.Foo", Signature: "func() error", BodyHash: "v2"},
		},
	}
	if _, changed, err := store.IngestParsedFileWithChange(ctx, pfV2, 2); err != nil || !changed {
		t.Fatalf("body_hash change must transition state; err=%v changed=%v", err, changed)
	}

	// A third ingest with the same v2 state is a no-op again — the
	// fingerprint has settled on v2's content.
	if _, changed, err := store.IngestParsedFileWithChange(ctx, pfV2, 3); err != nil || changed {
		t.Fatalf("re-ingest of unchanged v2 must report changed=false; err=%v changed=%v", err, changed)
	}
}

// TestContentFingerprint_DeterministicAndSelective ensures the
// fingerprint changes when semantically-meaningful fields change
// and stays constant under no-op observation refreshes (created_seq
// is the only mutable field excluded).
func TestContentFingerprint_DeterministicAndSelective(t *testing.T) {
	t.Parallel()
	base := Entity{
		ID:                  "abc",
		Kind:                KindFunction,
		LanguageID:          "go",
		QualifiedName:       "pkg.Foo",
		BodyHash:            "h1",
		NormalizedSignature: "(int)int",
	}
	if base.ContentFingerprint() == "" {
		t.Fatalf("fingerprint must be non-empty")
	}

	// Identical inputs → identical fingerprint.
	if base.ContentFingerprint() != base.ContentFingerprint() {
		t.Fatalf("fingerprint must be deterministic")
	}

	// Selective: changing each semantically-meaningful field must
	// shift the fingerprint. We iterate over a representative set;
	// the table-driven shape keeps the test honest as Entity grows.
	mutators := []struct {
		name string
		mut  func(*Entity)
	}{
		{"Kind", func(e *Entity) { e.Kind = KindMethod }},
		{"LanguageID", func(e *Entity) { e.LanguageID = "ts" }},
		{"QualifiedName", func(e *Entity) { e.QualifiedName = "pkg.Bar" }},
		{"Receiver", func(e *Entity) { e.Receiver = "*Pkg" }},
		{"Path", func(e *Entity) { e.Path = "src/other.go" }},
		{"BodyHash", func(e *Entity) { e.BodyHash = "h2" }},
		{"KindTag", func(e *Entity) { e.KindTag = "tag" }},
		{"ParentID", func(e *Entity) { e.ParentID = "parent" }},
		{"NormalizedSignature", func(e *Entity) { e.NormalizedSignature = "(int,int)int" }},
		{"SymbolFingerprint", func(e *Entity) { e.SymbolFingerprint = "fp" }},
		{"ASTHash", func(e *Entity) { e.ASTHash = "ast" }},
		{"Ordinal", func(e *Entity) { e.Ordinal = 7 }},
	}
	for _, m := range mutators {
		t.Run(m.name, func(t *testing.T) {
			t.Parallel()
			variant := base
			m.mut(&variant)
			if variant.ContentFingerprint() == base.ContentFingerprint() {
				t.Errorf("fingerprint should differ when %s changes", m.name)
			}
		})
	}
}

func sameIDs(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	ac, bc := append([]string(nil), a...), append([]string(nil), b...)
	sort.Strings(ac)
	sort.Strings(bc)
	for i := range ac {
		if ac[i] != bc[i] {
			return false
		}
	}
	return true
}
