package common

import (
	"encoding/json"
	"fmt"

	"github.com/shivamstaq/graph-harness/internal/code_framework"
	"github.com/shivamstaq/graph-harness/internal/kernel"
)

// ExtractedRoute is the framework-agnostic shape an extractor produces
// per route detection site. The common package turns each one into a
// (RouteAdded, HandlerBound) pair of kernel.Event records.
type ExtractedRoute struct {
	// Method is the HTTP method (upper-case, e.g. "GET"). For Django
	// routes with no explicit method constraint, callers pass "ANY".
	Method string
	// PathPattern is the literal route pattern as it appears in the
	// framework declaration (e.g. "/api/orders/<int:pk>").
	PathPattern string
	// Framework names the library family (e.g. "django", "fastapi",
	// "flask"); surfaced in Route.Framework.
	Framework string
	// Middleware is the optional list of middleware applied to this
	// route in the framework's declaration site.
	Middleware []string
	// ResponseKind names the response shape if discoverable
	// (e.g. "JSONResponse" for FastAPI). Empty when unknown.
	ResponseKind string
	// HandlerQN is the dotted qualified name of the handler function.
	// May be a partial reference (e.g. "views.foo") when the handler
	// is referenced by attribute access from another module — in that
	// case ConfidenceComputed/Dynamic applies.
	HandlerQN string
	// Confidence is the per-route confidence score per the rubric
	// (ConfidenceLiteral / ConfidenceComputed / ConfidenceDynamic).
	Confidence float64
	// SourcePath is the workspace-relative path of the file the
	// extractor parsed. Used as a fallback selector anchor when no
	// HandlerQN is available.
	SourcePath string
}

// ToEvents materializes a slice of ExtractedRoute into the kernel
// events the dispatcher appends to the EventLog. Two events per
// route: RouteAdded carrying the Route entity, HandlerBound
// carrying the Handler entity.
func ToEvents(extractorName string, in []ExtractedRoute) ([]kernel.Event, error) {
	out := make([]kernel.Event, 0, len(in)*2)
	for _, r := range in {
		method := MethodCanon(r.Method)
		if method == "" {
			method = "ANY"
		}
		var handlerSel code_framework.SelectorRef
		if r.HandlerQN != "" {
			handlerSel = QualifiedNameSelector(r.HandlerQN)
		} else if r.SourcePath != "" {
			handlerSel = PathGlobSelector(r.SourcePath)
		}

		prov := Provenance(extractorName, r.Confidence)
		routeAttrs := map[string]any{
			"method":       method,
			"path_pattern": r.PathPattern,
			"framework":    r.Framework,
		}
		if len(r.Middleware) > 0 {
			routeAttrs["middleware"] = r.Middleware
		}
		if r.ResponseKind != "" {
			routeAttrs["response_kind"] = r.ResponseKind
		}

		route := code_framework.Route{
			ID:           code_framework.MakeContentID(code_framework.KindRoute, handlerSel, routeAttrs),
			Kind:         code_framework.KindRoute,
			Method:       method,
			PathPattern:  r.PathPattern,
			Framework:    r.Framework,
			Middleware:   r.Middleware,
			ResponseKind: r.ResponseKind,
			AnchoredTo:   handlerSel,
			Provenance:   prov,
		}
		handler := code_framework.Handler{
			ID:         code_framework.MakeContentID(code_framework.KindHandler, handlerSel, map[string]any{"qualified_name": r.HandlerQN}),
			Kind:       code_framework.KindHandler,
			AnchoredTo: handlerSel,
			Provenance: prov,
		}

		rPayload, err := json.Marshal(route)
		if err != nil {
			return nil, fmt.Errorf("marshal Route: %w", err)
		}
		hPayload, err := json.Marshal(handler)
		if err != nil {
			return nil, fmt.Errorf("marshal Handler: %w", err)
		}

		out = append(out,
			kernel.Event{
				Layer:   "code.framework",
				Kind:    "RouteAdded",
				Payload: rPayload,
			},
			kernel.Event{
				Layer:   "code.framework",
				Kind:    "HandlerBound",
				Payload: hPayload,
			},
		)
	}
	return out, nil
}
