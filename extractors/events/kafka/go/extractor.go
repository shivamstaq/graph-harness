// Package kafkago is the events.kafka.go extractor: detects Kafka
// publish / subscribe call patterns in Go source files written against
// segmentio/kafka-go or confluent-kafka-go.
//
// Pass 1 (1-Events). Patterns covered:
//
//	segmentio/kafka-go
//	  Publisher:  w.WriteMessages(ctx, kafka.Message{Topic: "x", ...})
//	              kafka.NewWriter(kafka.WriterConfig{Topic: "x", ...})
//	  Subscriber: kafka.NewReader(kafka.ReaderConfig{Topic: "x", ...})
//
//	confluent-kafka-go
//	  Publisher:  p.Produce(&kafka.Message{TopicPartition: kafka.TopicPartition{Topic: &t}}, ...)
//	  Subscriber: c.Subscribe("x", ...)
//	              c.SubscribeTopics([]string{"x", "y"}, ...)
//
// Topic values must be string literals. Non-literal sites are dropped at
// extract time per the v1 limitation (see extractors/events/README.md).
package kafkago

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	tree_sitter "github.com/tree-sitter/go-tree-sitter"

	"github.com/shivamstaq/graph-harness/extractors/events/common"
	"github.com/shivamstaq/graph-harness/internal/code_framework"
	"github.com/shivamstaq/graph-harness/internal/kernel"
)

const (
	extractorName = "events.kafka.go"
	transport     = common.TransportKafka
	framework     = "kafka"
)

// Extractor implements code_framework.Extractor for Kafka producers and
// consumers in Go.
type Extractor struct {
	deps code_framework.Deps
}

// New is the Constructor registered with code_framework.
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
		Frameworks: []string{"segmentio/kafka-go", "confluent-kafka-go"},
		Fallback:   code_framework.FallbackTreesitterOnly,
		BatchHint:  code_framework.BatchPerEvent,
	}
}

// OnEvent parses the changed Go file and emits Event + EventPublisher
// + EventSubscriber kernel events for each detected Kafka call site.
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
	src, err := readFile(e.deps.Workspace, path)
	if err != nil {
		return nil, nil // ENOENT etc. — drop quietly per §6.16 tolerance
	}
	return scanGo(path, src)
}

func readFile(workspace, rel string) ([]byte, error) {
	full := rel
	if !filepath.IsAbs(rel) && workspace != "" {
		full = filepath.Join(workspace, rel)
	}
	return os.ReadFile(full)
}

// scanGo walks the parsed Go file looking for Kafka call patterns.
func scanGo(path string, src []byte) ([]kernel.Event, error) {
	tree, err := common.ParseGoSource(src)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	defer tree.Close()
	root := tree.RootNode()
	pkg := common.GoPackageName(root, src)

	emit := func(eventName string, pos uint32, publisher bool) ([]kernel.Event, error) {
		eventName = common.NormalizeTopic(eventName)
		if eventName == "" {
			return nil, nil
		}
		args := common.EmitArgs{
			ExtractorName:          extractorName,
			Transport:              transport,
			EventName:              eventName,
			Path:                   path,
			EnclosingQualifiedName: common.GoEnclosingQualifiedName(root, src, pkg, pos),
		}
		if publisher {
			return common.EmitPublisher(args)
		}
		return common.EmitSubscriber(args)
	}

	var out []kernel.Event

	// --- segmentio/kafka-go: WriteMessages(ctx, kafka.Message{Topic: ...}) ---
	writeMessagesCalls := common.FindGoCalls(root, src, func(recv, field string) bool {
		return field == "WriteMessages"
	})
	for _, c := range writeMessagesCalls {
		for _, a := range c.Args {
			topic := goExtractMessageTopic(a, src)
			if topic == "" {
				continue
			}
			ev, err := emit(topic, c.Pos, true)
			if err != nil {
				return nil, err
			}
			out = append(out, ev...)
		}
	}

	// --- segmentio/kafka-go: kafka.NewWriter(kafka.WriterConfig{Topic: ...}) ---
	// --- segmentio/kafka-go: kafka.NewReader(kafka.ReaderConfig{Topic: ...}) ---
	for _, c := range common.FindGoCalls(root, src, func(recv, field string) bool {
		return (field == "NewWriter" || field == "NewReader") && recv == "kafka"
	}) {
		publisher := strings.HasSuffix(c.Function.Utf8Text(src), "NewWriter")
		for _, a := range c.Args {
			topic := goExtractWriterReaderConfigTopic(a, src)
			if topic == "" {
				continue
			}
			ev, err := emit(topic, c.Pos, publisher)
			if err != nil {
				return nil, err
			}
			out = append(out, ev...)
		}
	}

	// --- confluent-kafka-go: p.Produce(&kafka.Message{TopicPartition: kafka.TopicPartition{Topic: &t}}, ...) ---
	produceCalls := common.FindGoCalls(root, src, func(recv, field string) bool {
		return field == "Produce"
	})
	for _, c := range produceCalls {
		for _, a := range c.Args {
			topic := goExtractTopicPartitionTopic(a, root, src)
			if topic == "" {
				continue
			}
			ev, err := emit(topic, c.Pos, true)
			if err != nil {
				return nil, err
			}
			out = append(out, ev...)
		}
	}

	// --- confluent-kafka-go: c.Subscribe("x", ...) | c.SubscribeTopics([]string{"x", "y"}, ...) ---
	subCalls := common.FindGoCalls(root, src, func(recv, field string) bool {
		return field == "Subscribe" || field == "SubscribeTopics"
	})
	for _, c := range subCalls {
		// Subscribe takes a string literal as first arg.
		if topic, _ := common.GoFirstStringArg(c.Args, src); topic != "" {
			ev, err := emit(topic, c.Pos, false)
			if err != nil {
				return nil, err
			}
			out = append(out, ev...)
		}
		// SubscribeTopics takes a string-slice composite literal.
		for _, a := range c.Args {
			for _, lit := range goSliceStringLiterals(a, src) {
				ev, err := emit(lit, c.Pos, false)
				if err != nil {
					return nil, err
				}
				out = append(out, ev...)
			}
		}
	}

	return common.SortEventsForDeterminism(common.DedupedEvents(out)), nil
}

// goExtractMessageTopic recovers the topic string from a
// `kafka.Message{Topic: "x"}` composite literal expression. Also handles
// pointer/&-prefixed versions and parenthesized wrappers.
func goExtractMessageTopic(arg *tree_sitter.Node, src []byte) string {
	lit := unwrapGoCompositeLiteral(arg)
	if lit == nil {
		return ""
	}
	// segmentio: kafka.Message{Topic: "x"}
	if v := common.GoCompositeLiteralField(lit, src, "Topic"); v != nil {
		if s, ok := common.GoStringLiteral(v, src); ok {
			return s
		}
	}
	return ""
}

// goExtractWriterReaderConfigTopic recovers the Topic string from a
// `kafka.WriterConfig{Topic: "x"}` / `kafka.ReaderConfig{Topic: "x"}`
// composite literal.
func goExtractWriterReaderConfigTopic(arg *tree_sitter.Node, src []byte) string {
	lit := unwrapGoCompositeLiteral(arg)
	if lit == nil {
		return ""
	}
	if v := common.GoCompositeLiteralField(lit, src, "Topic"); v != nil {
		if s, ok := common.GoStringLiteral(v, src); ok {
			return s
		}
	}
	return ""
}

// goExtractTopicPartitionTopic recovers the topic string from a
// confluent-kafka-go Message{TopicPartition: kafka.TopicPartition{Topic: &t}}
// literal. The Topic is typically `&someVar` (pointer to a string), so
// the caller passes the AST root + src so we can resolve `&t` to a
// nearby `t := "literal"` short_var_declaration.
func goExtractTopicPartitionTopic(arg, root *tree_sitter.Node, src []byte) string {
	lit := unwrapGoCompositeLiteral(arg)
	if lit == nil {
		return ""
	}
	tp := common.GoCompositeLiteralField(lit, src, "TopicPartition")
	if tp == nil {
		return ""
	}
	tpLit := unwrapGoCompositeLiteral(tp)
	if tpLit == nil {
		return ""
	}
	v := common.GoCompositeLiteralField(tpLit, src, "Topic")
	if v == nil {
		return ""
	}
	if s, ok := common.GoStringLiteral(v, src); ok {
		return s
	}
	if v.Kind() == "unary_expression" && v.NamedChildCount() > 0 {
		inner := v.NamedChild(0)
		if s, ok := common.GoStringLiteral(inner, src); ok {
			return s
		}
		// &someIdent — try to resolve someIdent to a nearby string
		// literal assignment.
		if inner != nil && inner.Kind() == "identifier" {
			name := inner.Utf8Text(src)
			if val := goLookupStringVar(root, src, name); val != "" {
				return val
			}
		}
	}
	return ""
}

// goLookupStringVar performs a best-effort lookup of a local variable's
// string-literal value. Scans the entire AST for short_var_declaration
// and var_spec nodes whose left-hand-side identifier matches name and
// whose right-hand-side is a string literal. Returns the literal or "".
//
// v1 limitation: this only resolves identifiers bound to constant
// string literals at the AST level — anything routed through env vars,
// function calls, or struct fields is not chased.
func goLookupStringVar(root *tree_sitter.Node, src []byte, name string) string {
	var found string
	common.Walk(root, func(n *tree_sitter.Node) bool {
		if found != "" {
			return false
		}
		switch n.Kind() {
		case "short_var_declaration":
			left := n.ChildByFieldName("left")
			right := n.ChildByFieldName("right")
			if left == nil || right == nil {
				return true
			}
			// Look for `name := "literal"`.
			if left.NamedChildCount() == 0 || right.NamedChildCount() == 0 {
				return true
			}
			id := left.NamedChild(0)
			val := right.NamedChild(0)
			if id != nil && id.Utf8Text(src) == name {
				if s, ok := common.GoStringLiteral(val, src); ok {
					found = s
				}
			}
		case "var_spec":
			// `var name = "literal"` or `var name string = "literal"`
			if n.NamedChildCount() < 2 {
				return true
			}
			// Find the identifier child(ren) and the value.
			var idNode, valNode *tree_sitter.Node
			for i := uint(0); i < n.NamedChildCount(); i++ {
				c := n.NamedChild(i)
				if c == nil {
					continue
				}
				if c.Kind() == "identifier" && idNode == nil {
					idNode = c
				} else if (c.Kind() == "interpreted_string_literal" || c.Kind() == "raw_string_literal" || c.Kind() == "expression_list") && valNode == nil {
					valNode = c
				}
			}
			if idNode != nil && idNode.Utf8Text(src) == name && valNode != nil {
				target := valNode
				if target.Kind() == "expression_list" && target.NamedChildCount() > 0 {
					target = target.NamedChild(0)
				}
				if s, ok := common.GoStringLiteral(target, src); ok {
					found = s
				}
			}
		}
		return true
	})
	return found
}

// unwrapGoCompositeLiteral peels &, parentheses, and pointer prefixes
// off a node to land on the underlying composite_literal. Returns nil
// if no composite literal is present.
func unwrapGoCompositeLiteral(n *tree_sitter.Node) *tree_sitter.Node {
	for n != nil {
		switch n.Kind() {
		case "composite_literal":
			return n
		case "unary_expression":
			if n.NamedChildCount() == 0 {
				return nil
			}
			n = n.NamedChild(0)
		case "parenthesized_expression":
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

// goSliceStringLiterals walks a composite_literal (e.g.
// `[]string{"x", "y"}`) and returns each string-literal element.
func goSliceStringLiterals(n *tree_sitter.Node, src []byte) []string {
	lit := unwrapGoCompositeLiteral(n)
	if lit == nil {
		return nil
	}
	body := lit.ChildByFieldName("body")
	if body == nil {
		for i := uint(0); i < lit.NamedChildCount(); i++ {
			c := lit.NamedChild(i)
			if c != nil && c.Kind() == "literal_value" {
				body = c
				break
			}
		}
	}
	if body == nil {
		return nil
	}
	var out []string
	for i := uint(0); i < body.NamedChildCount(); i++ {
		el := body.NamedChild(i)
		if el == nil {
			continue
		}
		target := el
		if el.Kind() == "literal_element" {
			if el.NamedChildCount() > 0 {
				target = el.NamedChild(0)
			}
		} else if el.Kind() == "keyed_element" && el.NamedChildCount() >= 2 {
			// Numeric / positional keyed (e.g. 0: "x") — take the value.
			vNode := el.NamedChild(1)
			if vNode != nil && vNode.Kind() == "literal_element" && vNode.NamedChildCount() > 0 {
				target = vNode.NamedChild(0)
			} else {
				target = vNode
			}
		}
		if s, ok := common.GoStringLiteral(target, src); ok {
			out = append(out, s)
		}
	}
	return out
}

func init() {
	code_framework.Register(extractorName, New, code_framework.Descriptor{
		Family:     "events",
		Languages:  []string{common.LanguageGo},
		Frameworks: []string{"segmentio/kafka-go", "confluent-kafka-go"},
		Fallback:   code_framework.FallbackTreesitterOnly,
		BatchHint:  code_framework.BatchPerEvent,
		Inputs:     common.CommonInputs(),
		Outputs:    common.CommonOutputs(),
	})
}
