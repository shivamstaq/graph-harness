// Package common holds helpers shared by every GraphQL extractor
// (graphql.ts.*, graphql.py.*, graphql.go.gqlgen). Per the Pass 1
// scope, GraphQL extractors all emit GraphQLOperation (+ optional
// Mutation alias) entities anchored at the schema declaration site
// with a resolver SelectorRef pointing at the implementing function.
//
// This package does NOT register any extractor itself; it only
// exposes glue:
//   - ReadFile resolves a FileChanged payload path against the
//     workspace root.
//   - BuildAnchor / BuildResolverAnchor construct the SelectorRef
//     shapes the GraphQL family uses.
//   - BuildOperation packages a (kind, attrs) tuple into a
//     code_framework.GraphQLOperation + matching kernel.Event.
//   - BuildMutationEvent emits the alias Mutation event for the
//     callers that filter on the Mutation kind.
package common

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	cf "github.com/shivamstaq/graph-harness/internal/code_framework"
	"github.com/shivamstaq/graph-harness/internal/kernel"
)

// FileChangedPayload is the minimal shape extractors decode from a
// code.core.FileChanged event. Mirrors internal/daemon.FileChangedPayload
// but kept local to avoid importing daemon types into extractor leaves.
type FileChangedPayload struct {
	Path     string `json:"path"`
	Language string `json:"language,omitempty"`
}

// DecodeFileChanged extracts (path, language) from a FileChanged
// event payload. Returns ok=false when the payload has no `path` or
// fails to decode.
func DecodeFileChanged(ev kernel.Event) (path, language string, ok bool) {
	var p FileChangedPayload
	if err := json.Unmarshal(ev.Payload, &p); err != nil {
		return "", "", false
	}
	if p.Path == "" {
		return "", "", false
	}
	return p.Path, p.Language, true
}

// ReadFile resolves path against workspace (if relative) and returns
// the bytes. Missing files yield (nil, nil) so a removed file is
// indistinguishable from an empty file from the extractor's POV —
// callers should drop empty contents.
func ReadFile(workspace, path string) ([]byte, error) {
	abs := path
	if !filepath.IsAbs(abs) {
		abs = filepath.Join(workspace, abs)
	}
	b, err := os.ReadFile(abs)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read %s: %w", abs, err)
	}
	return b, nil
}

// BuildAnchor constructs the SelectorRef the extractor uses to point
// at the schema declaration site. kind/value pairs are emitted in the
// order the caller passes — the SelectorRef.Hash() canonicalizes.
func BuildAnchor(kvs ...string) cf.SelectorRef {
	if len(kvs)%2 != 0 {
		panic("common.BuildAnchor: kvs must be even (kind, value, kind, value, ...)")
	}
	anchors := make([]cf.Anchor, 0, len(kvs)/2)
	for i := 0; i < len(kvs); i += 2 {
		anchors = append(anchors, cf.Anchor{Kind: kvs[i], Value: kvs[i+1]})
	}
	return cf.SelectorRef{Anchors: anchors}
}

// BuildResolverAnchor is the conventional SelectorRef shape for a
// resolver target: qualified_name + path_glob + entity_kind=Function.
// path is optional ("" means anchor by qualified_name alone).
func BuildResolverAnchor(qualifiedName, path string) cf.SelectorRef {
	anchors := []cf.Anchor{
		{Kind: "qualified_name", Value: qualifiedName},
		{Kind: "entity_kind", Value: "Function"},
	}
	if path != "" {
		anchors = append(anchors, cf.Anchor{Kind: "path_glob", Value: path})
	}
	return cf.SelectorRef{Anchors: anchors}
}

// BuildSchemaAnchor is the conventional SelectorRef shape for a
// schema-site target: qualified_name (operation name) + path_glob +
// entity_kind="GraphQLOperation".
func BuildSchemaAnchor(operationName, path string) cf.SelectorRef {
	anchors := []cf.Anchor{
		{Kind: "qualified_name", Value: operationName},
		{Kind: "entity_kind", Value: "GraphQLOperation"},
	}
	if path != "" {
		anchors = append(anchors, cf.Anchor{Kind: "path_glob", Value: path})
	}
	return cf.SelectorRef{Anchors: anchors}
}

// Operation is the canonical (extractor-internal) row used to build
// both the GraphQLOperation entity and the optional Mutation alias.
// Callers fill all fields; common.BuildOperationEvent + Maybe-
// MutationEvent translate into kernel.Event records.
type Operation struct {
	// OperationType is one of "query" | "mutation" | "subscription".
	OperationType string
	// Name is the operation field name (e.g. "createOrder").
	Name string
	// Arguments is the list of argument names (best-effort, may be
	// empty when extraction couldn't read them).
	Arguments []string
	// ReturnType is the string form of the GraphQL return type.
	ReturnType string
	// AnchoredTo is the SelectorRef at the schema declaration site.
	AnchoredTo cf.SelectorRef
	// ResolverRef is the SelectorRef at the implementing function.
	// May be empty when the resolver is not recoverable from this
	// file alone (e.g. SDL-only); in that case Hash falls back to
	// the schema anchor.
	ResolverRef cf.SelectorRef
}

// attrs builds the canonical attrs map fed into MakeContentID. The
// keys are sorted so the resulting ContentID is deterministic
// across runs and across Go map iteration order.
func (o Operation) attrs() map[string]any {
	args := append([]string(nil), o.Arguments...)
	sort.Strings(args)
	return map[string]any{
		"operation_type": o.OperationType,
		"name":           o.Name,
		"arguments":      args,
		"return_type":    o.ReturnType,
	}
}

// BuildOperationEvent constructs the kernel.Event for a
// GraphQLOperation emission. The event Kind is "GraphQLOperationAdded"
// (the Dispatcher overrides with -Changed/-Removed when it diffs the
// previous emission); the producedBy + Layer + Seq + Tx fields are
// left to the Dispatcher per the Extractor contract.
func BuildOperationEvent(o Operation) (kernel.Event, cf.GraphQLOperation, error) {
	id := cf.MakeContentID(cf.KindGraphQLOperation, o.AnchoredTo, o.attrs())
	op := cf.GraphQLOperation{
		ID:            id,
		Kind:          cf.KindGraphQLOperation,
		OperationType: o.OperationType,
		Name:          o.Name,
		Arguments:     o.Arguments,
		ReturnType:    o.ReturnType,
		ResolverRef:   o.ResolverRef,
		AnchoredTo:    o.AnchoredTo,
	}
	payload, err := json.Marshal(op)
	if err != nil {
		return kernel.Event{}, cf.GraphQLOperation{}, fmt.Errorf("marshal GraphQLOperation: %w", err)
	}
	return kernel.Event{
		Kind: "GraphQLOperationAdded",
		Subject: &kernel.EntityRef{
			Layer: "code.framework",
			Kind:  string(cf.KindGraphQLOperation),
			ID:    id,
		},
		Payload: payload,
	}, op, nil
}

// BuildMutationEvent constructs the alias Mutation event for an
// operation whose OperationType=="mutation". Returns ok=false when
// the operation is not a mutation.
func BuildMutationEvent(o Operation) (kernel.Event, bool, error) {
	if o.OperationType != "mutation" {
		return kernel.Event{}, false, nil
	}
	id := cf.MakeContentID(cf.KindMutation, o.AnchoredTo, map[string]any{
		"name": o.Name,
	})
	m := cf.Mutation{
		ID:          id,
		Kind:        cf.KindMutation,
		Name:        o.Name,
		ResolverRef: o.ResolverRef,
		AnchoredTo:  o.AnchoredTo,
	}
	payload, err := json.Marshal(m)
	if err != nil {
		return kernel.Event{}, false, fmt.Errorf("marshal Mutation: %w", err)
	}
	return kernel.Event{
		Kind: "MutationAdded",
		Subject: &kernel.EntityRef{
			Layer: "code.framework",
			Kind:  string(cf.KindMutation),
			ID:    id,
		},
		Payload: payload,
	}, true, nil
}

// EmitOperations is the convenience wrapper every per-library
// extractor uses to translate a slice of Operations into the
// kernel.Event slice OnEvent returns. Order is preserved.
func EmitOperations(ops []Operation) ([]kernel.Event, error) {
	if len(ops) == 0 {
		return nil, nil
	}
	out := make([]kernel.Event, 0, len(ops)*2)
	for _, o := range ops {
		ev, _, err := BuildOperationEvent(o)
		if err != nil {
			return nil, err
		}
		out = append(out, ev)
		if mev, ok, err := BuildMutationEvent(o); err != nil {
			return nil, err
		} else if ok {
			out = append(out, mev)
		}
	}
	return out, nil
}
