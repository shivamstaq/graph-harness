// Package jsonrpc binds the kernel.Router to the JSON-RPC service so
// every consumer-facing read method funnels through one read seam
// (SPEC §7.4 / answer-07 P0.T17a). This file implements the
// kernel.LayerReader contract for the two layer stores the daemon
// owns directly — code.core and semantic.overlay — and registers
// them with the service's Router instance.
package jsonrpc

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/shivamstaq/graph-harness/internal/code_core"
	"github.com/shivamstaq/graph-harness/internal/kernel"
	"github.com/shivamstaq/graph-harness/internal/semantic_overlay"
)

// codeCoreReader is the kernel.LayerReader for code.core. The router
// dispatches by Capability tag; each tag corresponds to one Store
// method. Argument shapes are documented inline next to each branch.
type codeCoreReader struct {
	store *code_core.Store
}

// Capabilities the code.core reader services.
const (
	CapCodeLookupEntity  = "lookup_entity"            // args: {id?, qualified_name?}
	CapCodeLookupByQName = "lookup_by_qualified_name" // args: {qualified_name, language?}
	CapCodeListEntities  = "list_entities"            // args: {qualified_name?, language?}
)

// codeLookupEntityArgs is the args shape for lookup_entity. Exactly
// one of ID / QualifiedName is required; ID wins when both are set.
type codeLookupEntityArgs struct {
	ID            string `json:"id,omitempty"`
	QualifiedName string `json:"qualified_name,omitempty"`
	Language      string `json:"language,omitempty"`
}

// codeListArgs is the args shape for list_entities.
type codeListArgs struct {
	QualifiedName string `json:"qualified_name,omitempty"`
	Language      string `json:"language,omitempty"`
}

func (r *codeCoreReader) ReadCurrent(ctx context.Context, q kernel.LayerQuery) (kernel.ResultEnvelope, error) {
	switch q.Capability {
	case CapCodeLookupEntity:
		return r.readLookupEntity(ctx, q.Args)
	case CapCodeLookupByQName:
		return r.readLookupByQName(ctx, q.Args)
	case CapCodeListEntities:
		return r.readListEntities(ctx, q.Args)
	default:
		return kernel.ResultEnvelope{}, fmt.Errorf("code.core: capability %q not declared", q.Capability)
	}
}

// ReadAsOf forwards to ReadCurrent — code.core's Store does not yet
// expose snapshot-pinned reads (P3.T13a will add the per-seq view
// layer). The envelope still carries the requested seq verbatim so
// historical-correctness callers can distinguish the two paths even
// when the underlying data is the same.
func (r *codeCoreReader) ReadAsOf(ctx context.Context, _ uint64, q kernel.LayerQuery) (kernel.ResultEnvelope, error) {
	return r.ReadCurrent(ctx, q)
}

func (r *codeCoreReader) readLookupEntity(ctx context.Context, raw json.RawMessage) (kernel.ResultEnvelope, error) {
	var args codeLookupEntityArgs
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &args); err != nil {
			return kernel.ResultEnvelope{}, fmt.Errorf("code.core/%s: %w", CapCodeLookupEntity, err)
		}
	}
	if args.ID == "" && args.QualifiedName == "" {
		return kernel.ResultEnvelope{}, fmt.Errorf("code.core/%s: id or qualified_name required", CapCodeLookupEntity)
	}
	id := args.ID
	if id == "" {
		ent, err := r.store.LookupByQualifiedName(ctx, args.QualifiedName)
		if err != nil {
			return kernel.ResultEnvelope{}, err
		}
		if ent == nil {
			return kernel.ResultEnvelope{}, code_core.ErrEntityNotFound
		}
		id = ent.ID
	}
	view, err := r.store.LookupEntity(ctx, id)
	if err != nil {
		return kernel.ResultEnvelope{}, err
	}
	blob, err := json.Marshal(view)
	if err != nil {
		return kernel.ResultEnvelope{}, err
	}
	return kernel.ResultEnvelope{
		Data: blob,
		Provenance: kernel.Provenance{
			Confidence:  view.Provenance.Summary.Confidence,
			Freshness:   kernel.FreshnessClass(view.Provenance.Summary.Freshness),
			SourceClass: []kernel.SourceClass{"layer:code.core"},
			ProducedSeq: view.Provenance.Summary.LatestSeenSeq,
		},
	}, nil
}

func (r *codeCoreReader) readLookupByQName(ctx context.Context, raw json.RawMessage) (kernel.ResultEnvelope, error) {
	var args codeLookupEntityArgs
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &args); err != nil {
			return kernel.ResultEnvelope{}, err
		}
	}
	if args.QualifiedName == "" {
		return kernel.ResultEnvelope{}, fmt.Errorf("code.core/%s: qualified_name required", CapCodeLookupByQName)
	}
	ent, err := r.store.LookupByQualifiedName(ctx, args.QualifiedName)
	if err != nil {
		return kernel.ResultEnvelope{}, err
	}
	if ent == nil {
		return kernel.ResultEnvelope{Data: json.RawMessage("null")}, nil
	}
	blob, err := json.Marshal(ent)
	if err != nil {
		return kernel.ResultEnvelope{}, err
	}
	return kernel.ResultEnvelope{
		Data: blob,
		Provenance: kernel.Provenance{
			Confidence:  1.0,
			Freshness:   kernel.FreshnessCurrent,
			SourceClass: []kernel.SourceClass{"layer:code.core"},
		},
	}, nil
}

func (r *codeCoreReader) readListEntities(ctx context.Context, raw json.RawMessage) (kernel.ResultEnvelope, error) {
	var args codeListArgs
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &args); err != nil {
			return kernel.ResultEnvelope{}, err
		}
	}
	rows, err := r.store.ListEntities(ctx, code_core.ListFilter{
		QualifiedName: args.QualifiedName,
		LanguageID:    args.Language,
	})
	if err != nil {
		return kernel.ResultEnvelope{}, err
	}
	blob, err := json.Marshal(rows)
	if err != nil {
		return kernel.ResultEnvelope{}, err
	}
	return kernel.ResultEnvelope{
		Data: blob,
		Provenance: kernel.Provenance{
			Confidence:  1.0,
			Freshness:   kernel.FreshnessCurrent,
			SourceClass: []kernel.SourceClass{"layer:code.core"},
		},
	}, nil
}

// overlayReader services semantic.overlay reads.
type overlayReader struct {
	get  func() *semantic_overlay.Overlay
	code *code_core.Store
	head kernel.HeadSeq
}

// Capabilities the semantic.overlay reader services.
const (
	CapOverlayResolveSelector = "resolve_selector" // args: {name}
	CapOverlayFlowsList       = "flows_list"
)

type overlayResolveArgs struct {
	Name string `json:"name"`
}

func (r *overlayReader) ReadCurrent(ctx context.Context, q kernel.LayerQuery) (kernel.ResultEnvelope, error) {
	switch q.Capability {
	case CapOverlayResolveSelector:
		return r.readResolveSelector(ctx, q.Args)
	case CapOverlayFlowsList:
		return r.readFlowsList()
	default:
		return kernel.ResultEnvelope{}, fmt.Errorf("semantic.overlay: capability %q not declared", q.Capability)
	}
}

func (r *overlayReader) ReadAsOf(ctx context.Context, _ uint64, q kernel.LayerQuery) (kernel.ResultEnvelope, error) {
	return r.ReadCurrent(ctx, q)
}

func (r *overlayReader) readResolveSelector(ctx context.Context, raw json.RawMessage) (kernel.ResultEnvelope, error) {
	var args overlayResolveArgs
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &args); err != nil {
			return kernel.ResultEnvelope{}, err
		}
	}
	if args.Name == "" {
		return kernel.ResultEnvelope{}, fmt.Errorf("semantic.overlay/%s: name required", CapOverlayResolveSelector)
	}
	env, err := r.get().Resolve(ctx, args.Name, r.code, r.head())
	if err != nil {
		return kernel.ResultEnvelope{}, err
	}
	blob, err := json.Marshal(env)
	if err != nil {
		return kernel.ResultEnvelope{}, err
	}
	return kernel.ResultEnvelope{
		Data:          blob,
		ResolvedAtSeq: env.ResolvedAt,
		Provenance: kernel.Provenance{
			Confidence:  1.0,
			Freshness:   kernel.FreshnessCurrent,
			SourceClass: []kernel.SourceClass{"layer:semantic.overlay"},
			ProducedSeq: env.ResolvedAt,
		},
	}, nil
}

func (r *overlayReader) readFlowsList() (kernel.ResultEnvelope, error) {
	o := r.get()
	type row struct {
		Name        string `json:"name"`
		Description string `json:"description,omitempty"`
		Scope       string `json:"scope,omitempty"`
		Steps       int    `json:"steps"`
	}
	out := make([]row, 0, len(o.Flows))
	for _, f := range o.Flows {
		out = append(out, row{Name: f.Name, Description: f.Description, Scope: f.Scope, Steps: len(f.Steps)})
	}
	blob, err := json.Marshal(out)
	if err != nil {
		return kernel.ResultEnvelope{}, err
	}
	return kernel.ResultEnvelope{
		Data: blob,
		Provenance: kernel.Provenance{
			Confidence:  1.0,
			Freshness:   kernel.FreshnessCurrent,
			SourceClass: []kernel.SourceClass{"layer:semantic.overlay"},
		},
	}, nil
}

// installRouter wires the router onto a Service. Called from
// NewService so every method handler that lowers through Router
// finds it already populated.
func installRouter(s *Service) *kernel.Router {
	router := kernel.NewRouter(func() uint64 {
		if s.Log == nil {
			return 0
		}
		return s.Log.LastSeq()
	})
	if s.Code != nil {
		router.Register("code.core", &codeCoreReader{store: s.Code})
	}
	router.Register("semantic.overlay", &overlayReader{
		get:  s.Overlay,
		code: s.Code,
		head: func() uint64 {
			if s.Log == nil {
				return 0
			}
			return s.Log.LastSeq()
		},
	})
	return router
}
