// Package kafkapy is the events.kafka.py extractor: detects Kafka
// publish / subscribe patterns in Python source written against
// confluent-kafka-python or kafka-python.
//
// Patterns covered:
//
//	confluent-kafka-python
//	  Publisher:  producer.produce("topic", value=...)
//	  Subscriber: consumer.subscribe(["topic-a", "topic-b"])
//
//	kafka-python
//	  Publisher:  producer.send("topic", value=b"...")
//	  Subscriber: KafkaConsumer("topic", group_id="g")
//	              consumer.subscribe(["topic-a"])
package kafkapy

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
	extractorName = "events.kafka.py"
	transport     = common.TransportKafka
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
		Frameworks: []string{"confluent-kafka-python", "kafka-python"},
		Fallback:   code_framework.FallbackTreesitterOnly,
		BatchHint:  code_framework.BatchPerEvent,
	}
}

// OnEvent parses the Python file and emits Event / Publisher / Subscriber events.
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

	// confluent-kafka / kafka-python producer.produce / producer.send
	// (publishers) — first positional arg is the topic string.
	for _, c := range common.FindPyCalls(root, src, func(obj, attr string) bool {
		return attr == "produce" || attr == "send"
	}) {
		topic, _ := common.PyFirstStringArg(c.Args, src)
		if topic != "" {
			ev, err := emit(topic, c.Pos, true)
			if err != nil {
				return nil, err
			}
			out = append(out, ev...)
		}
	}

	// consumer.subscribe([topics...]) — first arg is the list.
	for _, c := range common.FindPyCalls(root, src, func(obj, attr string) bool {
		return attr == "subscribe"
	}) {
		for _, a := range c.Args {
			if a.Kind() == "list" {
				for _, s := range pyListStrings(a, src) {
					ev, err := emit(s, c.Pos, false)
					if err != nil {
						return nil, err
					}
					out = append(out, ev...)
				}
			} else if s, ok := common.PyStringLiteral(a, src); ok {
				ev, err := emit(s, c.Pos, false)
				if err != nil {
					return nil, err
				}
				out = append(out, ev...)
			}
		}
	}

	// KafkaConsumer("topic", ...) — kafka-python's constructor form.
	for _, c := range common.FindPyCalls(root, src, func(obj, attr string) bool {
		return attr == "KafkaConsumer"
	}) {
		for _, a := range c.Args {
			if s, ok := common.PyStringLiteral(a, src); ok {
				ev, err := emit(s, c.Pos, false)
				if err != nil {
					return nil, err
				}
				out = append(out, ev...)
			}
		}
	}

	return common.SortEventsForDeterminism(common.DedupedEvents(out)), nil
}

func pyListStrings(n *tree_sitter.Node, src []byte) []string {
	if n == nil || n.Kind() != "list" {
		return nil
	}
	var out []string
	for i := uint(0); i < n.NamedChildCount(); i++ {
		c := n.NamedChild(i)
		if s, ok := common.PyStringLiteral(c, src); ok {
			out = append(out, s)
		}
	}
	return out
}

func init() {
	code_framework.Register(extractorName, New, code_framework.Descriptor{
		Family:     "events",
		Languages:  []string{common.LanguagePython},
		Frameworks: []string{"confluent-kafka-python", "kafka-python"},
		Fallback:   code_framework.FallbackTreesitterOnly,
		BatchHint:  code_framework.BatchPerEvent,
		Inputs:     common.CommonInputs(),
		Outputs:    common.CommonOutputs(),
	})
}
