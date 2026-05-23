// Package amqppy is the events.amqp.py extractor: detects AMQP
// (RabbitMQ) publish / consume patterns in Python source written
// against pika or aio-pika.
//
// Patterns covered:
//
//	Publisher:  channel.basic_publish(exchange="x", routing_key="r", body=...)
//	Subscriber: channel.basic_consume(queue="orders", on_message_callback=cb)
//	            channel.queue_declare(queue="orders")  # informative; counted as subscriber
package amqppy

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/shivamstaq/graph-harness/extractors/events/common"
	"github.com/shivamstaq/graph-harness/internal/code_framework"
	"github.com/shivamstaq/graph-harness/internal/kernel"
)

const (
	extractorName = "events.amqp.py"
	transport     = common.TransportAMQP
)

// Extractor implements code_framework.Extractor.
type Extractor struct{ deps code_framework.Deps }

// New is the Constructor.
func New(deps code_framework.Deps) (code_framework.Extractor, error) {
	return &Extractor{deps: deps}, nil
}

// Name returns the registered extractor name.
func (e *Extractor) Name() string { return extractorName }

// Inputs returns subscribed event kinds.
func (e *Extractor) Inputs() []code_framework.EventKind { return common.CommonInputs() }

// Outputs returns emitted entity kinds.
func (e *Extractor) Outputs() []code_framework.EntityKind { return common.CommonOutputs() }

// Capabilities returns capability descriptor.
func (e *Extractor) Capabilities() code_framework.Capabilities {
	return code_framework.Capabilities{
		Family:     "events",
		Languages:  []string{common.LanguagePython},
		Frameworks: []string{"pika", "aio-pika"},
		Fallback:   code_framework.FallbackTreesitterOnly,
		BatchHint:  code_framework.BatchPerEvent,
	}
}

// OnEvent parses the Python file and emits AMQP entity events.
func (e *Extractor) OnEvent(ctx context.Context, in kernel.Event) ([]kernel.Event, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	path, ok := common.PathFromEvent(in)
	if !ok {
		return nil, nil
	}
	if common.LanguageForPath(path) != common.LanguagePython {
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
	tree, err := common.ParsePythonSource(src)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	defer tree.Close()
	root := tree.RootNode()
	module := common.PyModuleName(path)

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
			EnclosingQualifiedName: common.PyEnclosingQualifiedName(root, src, module, pos),
		}
		if publisher {
			return common.EmitPublisher(args)
		}
		return common.EmitSubscriber(args)
	}

	var out []kernel.Event
	for _, c := range common.FindPyCalls(root, src, func(obj, attr string) bool {
		switch attr {
		case "basic_publish", "publish", "basic_consume", "consume":
			return true
		}
		return false
	}) {
		attr := ""
		if fn := c.Function; fn != nil && fn.Kind() == "attribute" {
			_, attr = common.PyAttribute(fn, src)
		}
		var topic string
		switch attr {
		case "basic_publish", "publish":
			// routing_key kwarg is the topic (pika); fallback to 2nd positional.
			if v := common.PyKeywordArg(c.ArgsNode, src, "routing_key"); v != nil {
				topic, _ = common.PyStringLiteral(v, src)
			}
			if topic == "" && len(c.Args) >= 2 {
				topic, _ = common.PyStringLiteral(c.Args[1], src)
			}
		default:
			// basic_consume / consume — queue kwarg or 1st positional.
			if v := common.PyKeywordArg(c.ArgsNode, src, "queue"); v != nil {
				topic, _ = common.PyStringLiteral(v, src)
			}
			if topic == "" && len(c.Args) >= 1 {
				topic, _ = common.PyStringLiteral(c.Args[0], src)
			}
		}
		if topic == "" {
			continue
		}
		publisher := attr == "basic_publish" || attr == "publish"
		ev, err := emit(topic, c.Pos, publisher)
		if err != nil {
			return nil, err
		}
		out = append(out, ev...)
	}
	return common.SortEventsForDeterminism(common.DedupedEvents(out)), nil
}

func init() {
	code_framework.Register(extractorName, New, code_framework.Descriptor{
		Family:     "events",
		Languages:  []string{common.LanguagePython},
		Frameworks: []string{"pika", "aio-pika"},
		Fallback:   code_framework.FallbackTreesitterOnly,
		BatchHint:  code_framework.BatchPerEvent,
		Inputs:     common.CommonInputs(),
		Outputs:    common.CommonOutputs(),
	})
}
