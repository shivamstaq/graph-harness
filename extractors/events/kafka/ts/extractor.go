// Package kafkats is the events.kafka.ts extractor: detects KafkaJS
// publish / subscribe call patterns in TypeScript / JavaScript source.
//
// Patterns covered:
//
//	Publisher:  kafka.producer().send({topic: "x", messages: [...]})
//	            producer.send({topic: "x", ...})
//	            producer.sendBatch({topicMessages: [{topic: "x", ...}, ...]})
//	Subscriber: kafka.consumer({groupId}).subscribe({topic: "x", fromBeginning: true})
//	            consumer.subscribe({topic: "x"})
//	            consumer.subscribe({topics: ["x", "y"]})  // newer KafkaJS
//
// Topic values must be string literals. Template strings without
// substitutions are accepted; substituted template strings are dropped
// per the v1 limitation.
package kafkats

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
	extractorName = "events.kafka.ts"
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

// Inputs returns the subscribed input event kinds.
func (e *Extractor) Inputs() []code_framework.EventKind { return common.CommonInputs() }

// Outputs returns the emitted entity kinds.
func (e *Extractor) Outputs() []code_framework.EntityKind { return common.CommonOutputs() }

// Capabilities advertises framework-family coverage.
func (e *Extractor) Capabilities() code_framework.Capabilities {
	return code_framework.Capabilities{
		Family:     "events",
		Languages:  []string{common.LanguageTypeScript},
		Frameworks: []string{"kafkajs"},
		Fallback:   code_framework.FallbackTreesitterOnly,
		BatchHint:  code_framework.BatchPerEvent,
	}
}

// OnEvent parses the changed TS / JS file and emits Event +
// EventPublisher + EventSubscriber kernel events.
func (e *Extractor) OnEvent(ctx context.Context, in kernel.Event) ([]kernel.Event, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	path, ok := common.PathFromEvent(in)
	if !ok {
		return nil, nil
	}
	if common.LanguageForPath(path) != common.LanguageTypeScript {
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
	tree, err := common.ParseTypeScriptSource(path, src)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	defer tree.Close()
	root := tree.RootNode()
	module := common.TSModuleName(path)

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
			EnclosingQualifiedName: common.TSEnclosingQualifiedName(root, src, module, pos),
		}
		if publisher {
			return common.EmitPublisher(args)
		}
		return common.EmitSubscriber(args)
	}

	var out []kernel.Event
	for _, c := range common.FindTSCalls(root, src, func(obj, method string) bool {
		switch method {
		case "send", "sendBatch", "subscribe":
			return true
		}
		return false
	}) {
		method := ""
		if fn := c.Function; fn != nil && fn.Kind() == "member_expression" {
			_, method = common.TSMemberAccess(fn, src)
		}
		publisher := method == "send" || method == "sendBatch"
		topics := extractTopicsFromArg(c.Args, src, method)
		for _, topic := range topics {
			ev, err := emit(topic, c.Pos, publisher)
			if err != nil {
				return nil, err
			}
			out = append(out, ev...)
		}
	}
	return common.SortEventsForDeterminism(common.DedupedEvents(out)), nil
}

// extractTopicsFromArg parses an argument node and returns the topic
// strings it carries. Handles `{topic: "x"}` for send/subscribe and
// `{topics: ["x", "y"]}` / `{topicMessages: [{topic: "x"}, ...]}` for
// batched calls.
func extractTopicsFromArg(args []*tree_sitter.Node, src []byte, method string) []string {
	if len(args) == 0 {
		return nil
	}
	// Most calls take a single options object.
	obj := args[0]
	if obj == nil || (obj.Kind() != "object" && obj.Kind() != "object_expression") {
		return nil
	}

	var out []string

	// {topic: "x"} — send / subscribe single-topic shape
	if v := common.TSObjectField(obj, src, "topic"); v != nil {
		if s, ok := common.TSStringLiteral(v, src); ok {
			out = append(out, s)
		}
	}

	// {topics: ["x", "y"]} — newer subscribe API
	if v := common.TSObjectField(obj, src, "topics"); v != nil {
		out = append(out, tsArrayStrings(v, src)...)
	}

	// {topicMessages: [{topic: "x", messages: [...]}, ...]} — sendBatch
	if v := common.TSObjectField(obj, src, "topicMessages"); v != nil {
		if v.Kind() == "array" {
			for i := uint(0); i < v.NamedChildCount(); i++ {
				el := v.NamedChild(i)
				if el == nil {
					continue
				}
				if t := common.TSObjectField(el, src, "topic"); t != nil {
					if s, ok := common.TSStringLiteral(t, src); ok {
						out = append(out, s)
					}
				}
			}
		}
	}

	_ = method
	return out
}

// tsArrayStrings walks a TS array literal and returns each
// string-literal element.
func tsArrayStrings(n *tree_sitter.Node, src []byte) []string {
	if n == nil || n.Kind() != "array" {
		return nil
	}
	var out []string
	for i := uint(0); i < n.NamedChildCount(); i++ {
		c := n.NamedChild(i)
		if s, ok := common.TSStringLiteral(c, src); ok {
			out = append(out, s)
		}
	}
	return out
}

func init() {
	code_framework.Register(extractorName, New, code_framework.Descriptor{
		Family:     "events",
		Languages:  []string{common.LanguageTypeScript},
		Frameworks: []string{"kafkajs"},
		Fallback:   code_framework.FallbackTreesitterOnly,
		BatchHint:  code_framework.BatchPerEvent,
		Inputs:     common.CommonInputs(),
		Outputs:    common.CommonOutputs(),
	})
}
