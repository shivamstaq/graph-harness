package jsonrpc

import (
	"context"
	"strings"

	"github.com/shivamstaq/graph-harness/internal/code_core"
)

// Framework-entity browser surface (P2.T38).
//
// These read-only RPCs back Studio's per-framework entity tables
// (Routes, Events, Schemas, Tests). Per the Pass-0.5-A convention,
// every framework extractor materializes its entities into the shared
// code.core store under a Kind discriminator ("Route", "Event",
// "Schema", "SchemaField", "EventPublisher", "EventSubscriber",
// "Test", "ContractTest"). The handlers below list those rows with an
// optional framework / transport / dialect filter and stable
// pagination, returning the canonical code_core.Entity wire shape so
// the SPA can index off Entity.ID and route the "show flow steps that
// touch this entity" drill-down through the reverse
// entity_selector_index (P2.M03).
//
// All handlers are read-only and route directly through Service.Code
// (no RequireWriter, no Router lowering — selector lookups are not
// involved). The capability tag is "framework_browser" so Studio /
// MCP can gate visibility on installation.
//
// Naming: the JSON-RPC methods live under the `framework.*` namespace
// to keep them adjacent to extractors.list / status and to avoid
// colliding with the `code.*` lookup surface, which addresses
// individual entities by ID.

// FrameworkListParams is the shared param shape for every list handler
// in this file. Framework is an optional filter against
// code_entities.path / qualified_name semantics — interpreted per
// handler so route-vs-event-vs-schema knows which column carries the
// framework discriminator (see per-handler doc).
//
// Limit/Offset are applied as a trailing LIMIT/OFFSET on the underlying
// SQL query. Limit<=0 means "default page" (200 rows); the absolute
// ceiling is 1000 rows so the SPA can never request a runaway result
// set through a hand-crafted URL.
type FrameworkListParams struct {
	Framework string `json:"framework,omitempty"`
	Limit     int    `json:"limit,omitempty"`
	Offset    int    `json:"offset,omitempty"`
}

const (
	defaultFrameworkListLimit = 200
	maxFrameworkListLimit     = 1000
)

// applyLimit clamps the requested page size to the surface's
// defaults / hard ceiling.
func (p FrameworkListParams) applyLimit() (limit, offset int) {
	limit = p.Limit
	if limit <= 0 {
		limit = defaultFrameworkListLimit
	}
	if limit > maxFrameworkListLimit {
		limit = maxFrameworkListLimit
	}
	offset = p.Offset
	if offset < 0 {
		offset = 0
	}
	return limit, offset
}

// FrameworkEntitiesResult is the response envelope shared by every
// list method below. Items is always non-nil (empty slice on no
// matches) so JSON consumers never need to special-case null.
type FrameworkEntitiesResult struct {
	Items []code_core.Entity `json:"items"`
	Total int                `json:"total"`
}

// FrameworkRoutes returns Route entities. The Framework filter narrows
// by the route's framework discriminator — Pass-1 extractors stamp it
// onto KindTag in addition to (or instead of) embedding it in the
// route id; the in-store representation uses Kind="Route" with the
// path pattern in QualifiedName and the HTTP method in KindTag
// (route_method anchor convention). To keep the filter portable across
// the multiple ways frameworks have been encoded, we match against
// the qualified_name + path columns for a substring containing the
// framework name. Framework="" returns every Route.
func (s *Service) FrameworkRoutes(ctx context.Context, p FrameworkListParams) (FrameworkEntitiesResult, error) {
	return s.frameworkListFiltered(ctx, code_core.EntityKind("Route"), "", p, filterByFramework)
}

// FrameworkEvents returns Event entities. The Framework filter narrows
// by transport name (kafka / nats / amqp / sns / sqs / redis); the
// transport is encoded as KindTag with a "topic:<transport>" prefix
// (topic_name anchor convention). Framework="" returns every Event.
//
// Note: the param is named "framework" for SPA-side symmetry with the
// Routes handler; "transport" would be more accurate at the framework
// layer but matching field names across the family keeps the SPA's
// shared filter widget simple.
func (s *Service) FrameworkEvents(ctx context.Context, p FrameworkListParams) (FrameworkEntitiesResult, error) {
	return s.frameworkListFiltered(ctx, code_core.EntityKind("Event"), "", p, filterByEventTransport)
}

// FrameworkEventPublishers returns EventPublisher entities. The
// Framework filter narrows by transport name (same convention as
// FrameworkEvents).
func (s *Service) FrameworkEventPublishers(ctx context.Context, p FrameworkListParams) (FrameworkEntitiesResult, error) {
	return s.frameworkListFiltered(ctx, code_core.EntityKind("EventPublisher"), "", p, filterByEventTransport)
}

// FrameworkEventSubscribers returns EventSubscriber entities.
func (s *Service) FrameworkEventSubscribers(ctx context.Context, p FrameworkListParams) (FrameworkEntitiesResult, error) {
	return s.frameworkListFiltered(ctx, code_core.EntityKind("EventSubscriber"), "", p, filterByEventTransport)
}

// FrameworkSchemas returns Schema entities. The Framework filter
// narrows by dialect (prisma / drizzle / sqlalchemy / gorm / sql).
// Dialect is stamped on KindTag by the Pass-1 schema extractors.
func (s *Service) FrameworkSchemas(ctx context.Context, p FrameworkListParams) (FrameworkEntitiesResult, error) {
	return s.frameworkListFiltered(ctx, code_core.EntityKind("Schema"), "", p, filterByKindTag)
}

// FrameworkSchemaFields returns SchemaField entities. The Framework
// filter is interpreted as a table name prefix (SchemaField
// QualifiedName has form "<table>.<field>").
func (s *Service) FrameworkSchemaFields(ctx context.Context, p FrameworkListParams) (FrameworkEntitiesResult, error) {
	return s.frameworkListFiltered(ctx, code_core.EntityKind("SchemaField"), "", p, filterByTablePrefix)
}

// FrameworkTests returns Test entities. The Framework filter narrows
// by test framework name (jest / vitest / pytest / go-test) stored on
// KindTag.
func (s *Service) FrameworkTests(ctx context.Context, p FrameworkListParams) (FrameworkEntitiesResult, error) {
	return s.frameworkListFiltered(ctx, code_core.EntityKind("Test"), "", p, filterByKindTag)
}

// FrameworkContractTests returns ContractTest entities.
func (s *Service) FrameworkContractTests(ctx context.Context, p FrameworkListParams) (FrameworkEntitiesResult, error) {
	return s.frameworkListFiltered(ctx, code_core.EntityKind("ContractTest"), "", p, filterByKindTag)
}

// entityFilter is the per-handler post-fetch filter signature. Returns
// true to keep the entity, false to drop. The functional shape keeps
// the SQL side simple (single Kind index probe) while letting each
// framework-family handler express the precise meaning of its
// "framework" param.
type entityFilter func(framework string, e code_core.Entity) bool

// frameworkListFiltered is the shared body for every framework.*
// list handler. It runs Store.ListEntitiesByKind to grab a candidate
// page, applies the per-handler filter, and re-packs into the
// FrameworkEntitiesResult envelope.
//
// Pagination semantics: limit/offset are applied at the SQL level
// when the filter is "match everything" (framework=="" or the filter
// returns true for every row); when the filter rejects rows the
// returned page may be shorter than limit even though more matches
// exist further on. This matches "best-effort streaming" UX for v1
// browsers — clients that need exact-count pagination can drop the
// framework filter and post-filter client-side. Total counts the
// pre-filter total so the SPA can show "showing N of M" guidance.
func (s *Service) frameworkListFiltered(
	ctx context.Context,
	kind code_core.EntityKind,
	kindTagPrefix string,
	p FrameworkListParams,
	filter entityFilter,
) (FrameworkEntitiesResult, error) {
	out := FrameworkEntitiesResult{Items: []code_core.Entity{}}
	if s.Code == nil {
		return out, nil
	}
	limit, offset := p.applyLimit()
	ents, err := s.Code.ListEntitiesByKind(ctx, kind, kindTagPrefix, limit, offset)
	if err != nil {
		return out, err
	}
	total, err := s.Code.CountByKind(ctx, kind)
	if err != nil {
		return out, err
	}
	out.Total = total
	for _, e := range ents {
		if p.Framework != "" && filter != nil && !filter(p.Framework, e) {
			continue
		}
		out.Items = append(out.Items, e)
	}
	return out, nil
}

// filterByFramework matches a Route entity's framework discriminator.
// Per the route extractor family the framework name (chi / express /
// fastapi / …) is stamped onto the entity's path column when it's
// part of a generated artifact path and otherwise lives in the
// kind_tag column as a comma-joined `method,framework` slot — we
// accept either spelling so SPA queries don't need to know which
// extractor authored the row.
func filterByFramework(framework string, e code_core.Entity) bool {
	if framework == "" {
		return true
	}
	if strings.Contains(strings.ToLower(e.KindTag), strings.ToLower(framework)) {
		return true
	}
	if strings.Contains(strings.ToLower(e.Path), strings.ToLower(framework)) {
		return true
	}
	return false
}

// filterByEventTransport matches an Event-family entity by transport
// name. Transport is encoded as "topic:<transport>" on KindTag for
// topic-bearing rows; non-topic event rows carry the transport in
// KindTag directly (e.g. "kafka", "nats") by the Pass-1 convention.
func filterByEventTransport(framework string, e code_core.Entity) bool {
	if framework == "" {
		return true
	}
	want := strings.ToLower(framework)
	tag := strings.ToLower(e.KindTag)
	if tag == want {
		return true
	}
	if strings.HasPrefix(tag, "topic:") && strings.TrimPrefix(tag, "topic:") == want {
		return true
	}
	return false
}

// filterByKindTag is the generic "framework matches KindTag exactly"
// filter — used by Schema (dialect), Test (test framework), and
// ContractTest where the discriminator lives unprefixed on KindTag.
func filterByKindTag(framework string, e code_core.Entity) bool {
	if framework == "" {
		return true
	}
	return strings.EqualFold(e.KindTag, framework)
}

// filterByTablePrefix matches SchemaField entities whose qualified
// name begins with "<framework>." — used to scope schema-field
// browsing to a single table without an extra schema_id round-trip.
func filterByTablePrefix(framework string, e code_core.Entity) bool {
	if framework == "" {
		return true
	}
	prefix := framework + "."
	return strings.HasPrefix(e.QualifiedName, prefix)
}

// FrameworkStepsTouchingParams identifies the entity whose touching
// flow-steps the caller wants. EntityID takes precedence; when empty
// the QualifiedName is resolved through the existing code.core
// lookup, then routed into the reverse selector index.
type FrameworkStepsTouchingParams struct {
	EntityID      string `json:"entity_id,omitempty"`
	QualifiedName string `json:"qualified_name,omitempty"`
}

// FrameworkStepsTouchingRow is one flow-step that resolves to the
// supplied entity. SelectorID is the overlay-side selector that
// produced the binding; FlowID is the optional flow scope (empty
// when the selector was resolved outside a flow); ViaAnchor records
// which anchor on the selector matched. BoundAtSeq is the kernel
// sequence at which the binding was recorded so consumers can age it.
type FrameworkStepsTouchingRow struct {
	SelectorID string `json:"selector_id"`
	FlowID     string `json:"flow_id,omitempty"`
	ViaAnchor  string `json:"via_anchor"`
	BoundAtSeq uint64 `json:"bound_at_seq"`
}

// FrameworkStepsTouchingResult is the response envelope.
type FrameworkStepsTouchingResult struct {
	EntityID string                      `json:"entity_id"`
	Steps    []FrameworkStepsTouchingRow `json:"steps"`
}

// FrameworkStepsTouching answers "which flow steps touch this entity?"
// by consulting the Pass-0.5-A reverse selector→entity index
// (Store.SelectorsBoundTo). It is the backbone of Studio's "Show
// flow steps that touch this route" drill-down — the per-entity
// follow-up clicked from any row in the framework.routes / events /
// schemas tables.
//
// Returns an empty slice (not nil) when the entity has no bindings —
// callers render an empty drill-down rather than an error.
func (s *Service) FrameworkStepsTouching(ctx context.Context, p FrameworkStepsTouchingParams) (FrameworkStepsTouchingResult, error) {
	out := FrameworkStepsTouchingResult{Steps: []FrameworkStepsTouchingRow{}}
	if s.Code == nil {
		return out, nil
	}
	id := p.EntityID
	if id == "" && p.QualifiedName != "" {
		ent, err := s.Code.LookupByQualifiedName(ctx, p.QualifiedName)
		if err != nil {
			return out, err
		}
		if ent != nil {
			id = ent.ID
		}
	}
	if id == "" {
		return out, nil
	}
	out.EntityID = id
	bindings, err := s.Code.SelectorsBoundTo(ctx, id)
	if err != nil {
		return out, err
	}
	for _, b := range bindings {
		out.Steps = append(out.Steps, FrameworkStepsTouchingRow{
			SelectorID: b.SelectorID,
			FlowID:     b.FlowID,
			ViaAnchor:  b.ViaAnchor,
			BoundAtSeq: b.BoundAtSeq,
		})
	}
	return out, nil
}
