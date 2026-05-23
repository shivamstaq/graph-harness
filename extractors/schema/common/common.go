// Package common provides shared helpers for the schema-family
// extractors (Prisma, Drizzle, SQLAlchemy, GORM, raw SQL, and
// migrations). Each extractor depends on this package to avoid
// duplicating the same emission-shaping code.
//
// The helpers here intentionally do NOT depend on any single
// framework's parsing primitives — they operate on already-parsed
// (table, field, type) triples and turn them into
// kernel.Event payloads conforming to the code.framework contract.
package common

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"

	cf "github.com/shivamstaq/graph-harness/internal/code_framework"
	"github.com/shivamstaq/graph-harness/internal/kernel"
)

// FileFromEvent extracts the file path from a code.core.FileChanged
// payload. Returns the empty string + ErrNoPath if the payload does
// not contain a `path` field. Symmetrical with the
// internal/code_core/ingest.go emitter.
func FileFromEvent(ev kernel.Event) (string, error) {
	if len(ev.Payload) == 0 {
		return "", ErrNoPath
	}
	var p struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(ev.Payload, &p); err != nil {
		return "", err
	}
	if p.Path == "" {
		return "", ErrNoPath
	}
	return p.Path, nil
}

// ErrNoPath is returned by FileFromEvent when the input does not carry
// a file path. Extractors typically treat this as a no-op (return nil
// emissions).
var ErrNoPath = errors.New("event payload has no path")

// ReadFile reads `path` relative to `workspace`. Absolute paths are
// returned as-is. Returns os.ErrNotExist when the file does not exist
// (callers should treat that as "nothing to extract" rather than an
// error).
func ReadFile(workspace, path string) ([]byte, error) {
	full := path
	if !filepath.IsAbs(path) {
		full = filepath.Join(workspace, path)
	}
	return os.ReadFile(full)
}

// PathAnchor returns a SelectorRef anchored to a single path glob.
// Used when an extractor cannot resolve to a specific code.core
// entity (the typical case for schema files which are DSL not source).
func PathAnchor(glob string) cf.SelectorRef {
	return cf.SelectorRef{
		Anchors: []cf.Anchor{{Kind: "path_glob", Value: glob}},
	}
}

// QualifiedAnchor returns a SelectorRef anchored to a qualified name
// plus an optional path-glob disambiguator. The qualified_name anchor
// is the strongest cross-layer reference per SPEC §2.2.
func QualifiedAnchor(qn, glob string) cf.SelectorRef {
	anchors := []cf.Anchor{{Kind: "qualified_name", Value: qn}}
	if glob != "" {
		anchors = append(anchors, cf.Anchor{Kind: "path_glob", Value: glob})
	}
	return cf.SelectorRef{Anchors: anchors, Unique: true}
}

// LangAnchor returns a SelectorRef anchored to a qualified name plus
// language_id, used by language-specific extractors (GORM struct =
// Go, SQLAlchemy = Python, Drizzle = TS).
func LangAnchor(qn, lang, glob string) cf.SelectorRef {
	anchors := []cf.Anchor{
		{Kind: "qualified_name", Value: qn},
		{Kind: "language_id", Value: lang},
	}
	if glob != "" {
		anchors = append(anchors, cf.Anchor{Kind: "path_glob", Value: glob})
	}
	return cf.SelectorRef{Anchors: anchors, Unique: true}
}

// EmitSchema builds a Schema entity payload + the matching kernel.Event
// (SchemaAdded). Returns the Schema (with its ContentID populated) so
// callers can pass it to EmitField.
func EmitSchema(producedBy string, table, dialect string, anchor cf.SelectorRef) (cf.Schema, kernel.Event) {
	id := cf.MakeContentID(cf.KindSchema, anchor, map[string]any{
		"table":   table,
		"dialect": dialect,
	})
	s := cf.Schema{
		ID:         id,
		Kind:       cf.KindSchema,
		Table:      table,
		Dialect:    dialect,
		AnchoredTo: anchor,
		Provenance: cf.Provenance{
			Confidence:  0.95,
			Freshness:   kernel.FreshnessCurrent,
			SourceClass: []kernel.SourceClass{cf.SourceExtractorFramework},
			ProducedBy:  producedBy,
		},
	}
	pl, _ := json.Marshal(s)
	return s, kernel.Event{
		Layer:   "code.framework",
		Kind:    "SchemaAdded",
		Payload: pl,
		Subject: &kernel.EntityRef{Layer: "code.framework", Kind: string(cf.KindSchema), ID: id},
	}
}

// EmitField builds a SchemaField entity payload + the matching event
// (SchemaFieldAdded). The parent must have been emitted first so its
// ContentID is available.
func EmitField(producedBy string, parent cf.Schema, name, dataType string, nullable bool, anchor cf.SelectorRef) (cf.SchemaField, kernel.Event) {
	id := cf.MakeContentID(cf.KindSchemaField, anchor, map[string]any{
		"schema_id": parent.ID,
		"name":      name,
		"data_type": dataType,
		"nullable":  nullable,
	})
	sf := cf.SchemaField{
		ID:         id,
		Kind:       cf.KindSchemaField,
		SchemaID:   parent.ID,
		Name:       name,
		DataType:   dataType,
		Nullable:   nullable,
		AnchoredTo: anchor,
		Provenance: cf.Provenance{
			Confidence:  0.95,
			Freshness:   kernel.FreshnessCurrent,
			SourceClass: []kernel.SourceClass{cf.SourceExtractorFramework},
			ProducedBy:  producedBy,
		},
	}
	pl, _ := json.Marshal(sf)
	return sf, kernel.Event{
		Layer:   "code.framework",
		Kind:    "SchemaFieldAdded",
		Payload: pl,
		Subject: &kernel.EntityRef{Layer: "code.framework", Kind: string(cf.KindSchemaField), ID: id},
	}
}

// EmitRead builds a SchemaRead entity payload + the matching event
// (SchemaReadAdded). field is the FieldID this read site touches;
// callers that detect reads on unknown fields (the rawsql case) pass
// the column name encoded as "<table>.<column>" and the suppress-at-
// source layer will eventually fold these against any later-emitted
// SchemaField.
func EmitRead(producedBy string, fieldID string, anchor cf.SelectorRef) kernel.Event {
	id := cf.MakeContentID(cf.KindSchemaRead, anchor, map[string]any{
		"field_id": fieldID,
	})
	r := cf.SchemaRead{
		ID:         id,
		Kind:       cf.KindSchemaRead,
		FieldID:    fieldID,
		AnchoredTo: anchor,
		Provenance: cf.Provenance{
			Confidence:  0.80,
			Freshness:   kernel.FreshnessCurrent,
			SourceClass: []kernel.SourceClass{cf.SourceExtractorFramework},
			ProducedBy:  producedBy,
		},
	}
	pl, _ := json.Marshal(r)
	return kernel.Event{
		Layer:   "code.framework",
		Kind:    "SchemaReadAdded",
		Payload: pl,
		Subject: &kernel.EntityRef{Layer: "code.framework", Kind: string(cf.KindSchemaRead), ID: id},
	}
}

// EmitWrite is the SchemaWriteAdded twin of EmitRead.
func EmitWrite(producedBy string, fieldID string, anchor cf.SelectorRef) kernel.Event {
	id := cf.MakeContentID(cf.KindSchemaWrite, anchor, map[string]any{
		"field_id": fieldID,
	})
	w := cf.SchemaWrite{
		ID:         id,
		Kind:       cf.KindSchemaWrite,
		FieldID:    fieldID,
		AnchoredTo: anchor,
		Provenance: cf.Provenance{
			Confidence:  0.80,
			Freshness:   kernel.FreshnessCurrent,
			SourceClass: []kernel.SourceClass{cf.SourceExtractorFramework},
			ProducedBy:  producedBy,
		},
	}
	pl, _ := json.Marshal(w)
	return kernel.Event{
		Layer:   "code.framework",
		Kind:    "SchemaWriteAdded",
		Payload: pl,
		Subject: &kernel.EntityRef{Layer: "code.framework", Kind: string(cf.KindSchemaWrite), ID: id},
	}
}

// EmitMigration builds a Migration entity payload + the matching event
// (MigrationAdded).
func EmitMigration(producedBy string, tool, version string, anchor cf.SelectorRef) kernel.Event {
	id := cf.MakeContentID(cf.KindMigration, anchor, map[string]any{
		"tool":    tool,
		"version": version,
	})
	m := cf.Migration{
		ID:         id,
		Kind:       cf.KindMigration,
		Tool:       tool,
		Version:    version,
		AnchoredTo: anchor,
		Provenance: cf.Provenance{
			Confidence:  0.99,
			Freshness:   kernel.FreshnessCurrent,
			SourceClass: []kernel.SourceClass{cf.SourceExtractorFramework},
			ProducedBy:  producedBy,
		},
	}
	pl, _ := json.Marshal(m)
	return kernel.Event{
		Layer:   "code.framework",
		Kind:    "MigrationAdded",
		Payload: pl,
		Subject: &kernel.EntityRef{Layer: "code.framework", Kind: string(cf.KindMigration), ID: id},
	}
}

// HasExt returns true if path ends with any of the given (lower-case)
// extensions. Comparison is case-insensitive against the path.
func HasExt(path string, exts ...string) bool {
	lower := strings.ToLower(path)
	for _, ext := range exts {
		if strings.HasSuffix(lower, ext) {
			return true
		}
	}
	return false
}

// ProducedBy returns the ProducedBy SourceClass string for an
// extractor name (e.g. "extractor:framework:schema.prisma").
func ProducedBy(name string) string {
	return string(cf.SourceExtractorFramework) + ":" + name
}

// NoopOnPathMiss is a tiny convenience: if FileFromEvent errors with
// ErrNoPath or the file doesn't have one of the supported extensions,
// the caller returns nil/nil so the dispatcher records "nothing
// changed for this file" — the suppress-at-source path.
func NoopOnPathMiss(ctx context.Context, ev kernel.Event, accept func(string) bool) (string, bool) {
	_ = ctx
	path, err := FileFromEvent(ev)
	if err != nil || !accept(path) {
		return "", false
	}
	return path, true
}
