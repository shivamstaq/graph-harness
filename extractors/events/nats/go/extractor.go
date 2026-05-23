// Package natsgo is the events.nats.go extractor: detects NATS publish
// and subscribe call patterns in Go source files written against
// nats.go.
//
// Patterns covered:
//
//	Publisher:  nc.Publish("subject", data)
//	            nc.PublishMsg(&nats.Msg{Subject: "x", ...})
//	            nc.Request("subject", data, timeout)
//	Subscriber: nc.Subscribe("subject", handler)
//	            nc.QueueSubscribe("subject", "group", handler)
//	            nc.ChanSubscribe("subject", ch)
//
// Topic values must be string literals. Non-literal subjects are
// dropped per the v1 limitation.
package natsgo

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	tree_sitter "github.com/tree-sitter/go-tree-sitter"

	"github.com/shivamstaq/graph-harness/extractors/events/common"
	"github.com/shivamstaq/graph-harness/internal/code_framework"
	"github.com/shivamstaq/graph-harness/internal/kernel"
)

const (
	extractorName = "events.nats.go"
	transport     = common.TransportNATS
)

var publishMethods = map[string]bool{
	"Publish":    true,
	"PublishMsg": true,
	"Request":    true,
	"RequestMsg": true,
}

var subscribeMethods = map[string]bool{
	"Subscribe":      true,
	"QueueSubscribe": true,
	"SubscribeSync":  true,
	"ChanSubscribe":  true,
}

// Extractor implements code_framework.Extractor for NATS in Go.
type Extractor struct{ deps code_framework.Deps }

// New is the Constructor.
func New(deps code_framework.Deps) (code_framework.Extractor, error) {
	return &Extractor{deps: deps}, nil
}

// Name returns the registered extractor name.
func (e *Extractor) Name() string { return extractorName }

// Inputs subscribes to code.core file-change drift.
func (e *Extractor) Inputs() []code_framework.EventKind { return common.CommonInputs() }

// Outputs declares the entity kinds this extractor emits.
func (e *Extractor) Outputs() []code_framework.EntityKind { return common.CommonOutputs() }

// Capabilities advertises framework-family coverage.
func (e *Extractor) Capabilities() code_framework.Capabilities {
	return code_framework.Capabilities{
		Family:     "events",
		Languages:  []string{common.LanguageGo},
		Frameworks: []string{"nats.go"},
		Fallback:   code_framework.FallbackTreesitterOnly,
		BatchHint:  code_framework.BatchPerEvent,
	}
}

// OnEvent parses the changed Go file and emits Event + EventPublisher
// + EventSubscriber kernel events for each detected NATS call.
func (e *Extractor) OnEvent(ctx context.Context, in kernel.Event) ([]kernel.Event, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	path, ok := common.PathFromEvent(in)
	if !ok {
		return nil, nil
	}
	if common.LanguageForPath(path) != common.LanguageGo {
		return nil, nil
	}
	full := path
	if !filepath.IsAbs(path) && e.deps.Workspace != "" {
		full = filepath.Join(e.deps.Workspace, path)
	}
	src, err := os.ReadFile(full)
	if err != nil {
		return nil, nil
	}
	return scan(path, src)
}

func scan(path string, src []byte) ([]kernel.Event, error) {
	tree, err := common.ParseGoSource(src)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	defer tree.Close()
	root := tree.RootNode()
	pkg := common.GoPackageName(root, src)

	emit := func(topic string, pos uint32, publisher bool) ([]kernel.Event, error) {
		topic = common.NormalizeTopic(topic)
		if topic == "" {
			return nil, nil
		}
		args := common.EmitArgs{
			ExtractorName:          extractorName,
			Transport:              transport,
			EventName:              topic,
			Path:                   path,
			EnclosingQualifiedName: common.GoEnclosingQualifiedName(root, src, pkg, pos),
		}
		if publisher {
			return common.EmitPublisher(args)
		}
		return common.EmitSubscriber(args)
	}

	var out []kernel.Event
	for _, c := range common.FindGoCalls(root, src, func(recv, field string) bool {
		return publishMethods[field] || subscribeMethods[field]
	}) {
		field := ""
		if fn := c.Function; fn != nil && fn.Kind() == "selector_expression" {
			_, field = common.GoSelectorIdent(fn, src)
		}
		topic := extractTopic(field, c.Args, src)
		if topic == "" {
			continue
		}
		publisher := publishMethods[field]
		ev, err := emit(topic, c.Pos, publisher)
		if err != nil {
			return nil, err
		}
		out = append(out, ev...)
	}
	return common.SortEventsForDeterminism(common.DedupedEvents(out)), nil
}

// extractTopic recovers the subject string for a given NATS call. For
// PublishMsg the subject is nested inside a *nats.Msg composite literal;
// other calls take the subject as the first positional arg.
func extractTopic(method string, args []*tree_sitter.Node, src []byte) string {
	switch method {
	case "PublishMsg", "RequestMsg":
		for _, a := range args {
			topic := natsExtractMsgSubject(a, src)
			if topic != "" {
				return topic
			}
		}
		return ""
	case "QueueSubscribe":
		// QueueSubscribe(subject, queue, handler) — first arg is subject.
		if topic, _ := common.GoFirstStringArg(args, src); topic != "" {
			return topic
		}
		return ""
	default:
		topic, _ := common.GoFirstStringArg(args, src)
		return topic
	}
}

func natsExtractMsgSubject(arg *tree_sitter.Node, src []byte) string {
	lit := unwrapComposite(arg)
	if lit == nil {
		return ""
	}
	v := common.GoCompositeLiteralField(lit, src, "Subject")
	if v == nil {
		return ""
	}
	if s, ok := common.GoStringLiteral(v, src); ok {
		return s
	}
	return ""
}

func unwrapComposite(n *tree_sitter.Node) *tree_sitter.Node {
	for n != nil {
		switch n.Kind() {
		case "composite_literal":
			return n
		case "unary_expression", "parenthesized_expression":
			if n.NamedChildCount() == 0 {
				return nil
			}
			n = n.NamedChild(0)
		default:
			return nil
		}
	}
	return nil
}

func init() {
	code_framework.Register(extractorName, New, code_framework.Descriptor{
		Family:     "events",
		Languages:  []string{common.LanguageGo},
		Frameworks: []string{"nats.go"},
		Fallback:   code_framework.FallbackTreesitterOnly,
		BatchHint:  code_framework.BatchPerEvent,
		Inputs:     common.CommonInputs(),
		Outputs:    common.CommonOutputs(),
	})
}
