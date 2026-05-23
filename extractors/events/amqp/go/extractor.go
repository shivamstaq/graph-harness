// Package amqpgo is the events.amqp.go extractor: detects AMQP (RabbitMQ)
// publish / consume call patterns in Go source files written against
// rabbitmq/amqp091-go (and the older streadway/amqp).
//
// Patterns covered:
//
//	Publisher:  ch.Publish(exchange, "routing.key", mandatory, immediate, amqp.Publishing{...})
//	            ch.PublishWithContext(ctx, exchange, "routing.key", ...)
//	Subscriber: ch.Consume("queue", consumer, autoAck, ...)
//
// For AMQP v1 we treat both routing-key strings (Publish) and
// queue-name strings (Consume) as the topic-equivalent string. The
// AMQP docs distinguish them, but our cross-language matching only
// cares about the string identity (P2.T16 v1).
package amqpgo

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
	extractorName = "events.amqp.go"
	transport     = common.TransportAMQP
)

var publishMethods = map[string]bool{
	"Publish":            true,
	"PublishWithContext": true,
}

var subscribeMethods = map[string]bool{
	"Consume":            true,
	"ConsumeWithContext": true,
}

// Extractor implements code_framework.Extractor for AMQP in Go.
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
		Frameworks: []string{"rabbitmq/amqp091-go", "streadway/amqp"},
		Fallback:   code_framework.FallbackTreesitterOnly,
		BatchHint:  code_framework.BatchPerEvent,
	}
}

// OnEvent parses the changed Go file and emits Event + EventPublisher
// + EventSubscriber kernel events for each detected AMQP call.
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
		var topic string
		switch {
		case field == "Publish":
			// Publish(exchange, key, mandatory, immediate, msg) — routing
			// key is the 2nd positional. Take first arg if 2nd is not a
			// literal (degenerate AMQP setup with empty exchange).
			topic = nthStringArg(c.Args, src, 1)
			if topic == "" {
				topic = nthStringArg(c.Args, src, 0)
			}
		case field == "PublishWithContext":
			// PublishWithContext(ctx, exchange, key, mandatory, immediate, msg)
			topic = nthStringArg(c.Args, src, 2)
			if topic == "" {
				topic = nthStringArg(c.Args, src, 1)
			}
		default:
			// Consume(queue, consumer, ...) — queue is the 1st positional.
			topic, _ = common.GoFirstStringArg(c.Args, src)
		}
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

// nthStringArg returns the value of the nth positional argument if it
// is a Go string literal; empty string otherwise.
func nthStringArg(args []*tree_sitter.Node, src []byte, n int) string {
	if n < 0 || n >= len(args) {
		return ""
	}
	if s, ok := common.GoStringLiteral(args[n], src); ok {
		return s
	}
	return ""
}

func init() {
	code_framework.Register(extractorName, New, code_framework.Descriptor{
		Family:     "events",
		Languages:  []string{common.LanguageGo},
		Frameworks: []string{"rabbitmq/amqp091-go", "streadway/amqp"},
		Fallback:   code_framework.FallbackTreesitterOnly,
		BatchHint:  code_framework.BatchPerEvent,
		Inputs:     common.CommonInputs(),
		Outputs:    common.CommonOutputs(),
	})
}
