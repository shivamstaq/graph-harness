// Package snsqsgo is the events.snsqs.go extractor: detects AWS SNS
// Publish and SQS SendMessage / ReceiveMessage call patterns in Go
// source files written against aws-sdk-go-v2 and the older
// aws-sdk-go v1.
//
// SNS + SQS are treated as one transport family because their topics
// and queues are typically paired (SNS topic → SQS queue subscription)
// in real-world usage. Internally the extractor stamps Transport with
// the more specific of "sns" or "sqs" based on the API call, so
// cross-language matching only succeeds when both sides agree on the
// transport.
//
// Patterns covered:
//
//	SNS (v2):  client.Publish(ctx, &sns.PublishInput{TopicArn: aws.String("arn:...:order-events"), Message: "..."})
//	SQS (v2):  client.SendMessage(ctx, &sqs.SendMessageInput{QueueUrl: aws.String("https://sqs.../orders"), ...})
//	           client.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{QueueUrl: aws.String("https://sqs.../orders")})
//
// Topic / queue identifiers are extracted from ARN-style or queue-URL
// strings; common.ARNTopicName strips the prefix down to the last
// segment.
package snsqsgo

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

const extractorName = "events.snsqs.go"

// Extractor implements code_framework.Extractor.
type Extractor struct{ deps code_framework.Deps }

// New is the Constructor.
func New(deps code_framework.Deps) (code_framework.Extractor, error) {
	return &Extractor{deps: deps}, nil
}

// Name returns the registered extractor name.
func (e *Extractor) Name() string { return extractorName }

// Inputs returns input event kinds.
func (e *Extractor) Inputs() []code_framework.EventKind { return common.CommonInputs() }

// Outputs returns emitted entity kinds.
func (e *Extractor) Outputs() []code_framework.EntityKind { return common.CommonOutputs() }

// Capabilities advertises framework-family coverage.
func (e *Extractor) Capabilities() code_framework.Capabilities {
	return code_framework.Capabilities{
		Family:     "events",
		Languages:  []string{common.LanguageGo},
		Frameworks: []string{"aws-sdk-go-v2", "aws-sdk-go"},
		Fallback:   code_framework.FallbackTreesitterOnly,
		BatchHint:  code_framework.BatchPerEvent,
	}
}

// OnEvent parses the changed Go file and emits Event / Publisher /
// Subscriber kernel events for each detected SNS/SQS call.
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

	emit := func(topic, txp string, pos uint32, publisher bool) ([]kernel.Event, error) {
		topic = common.NormalizeTopic(common.ARNTopicName(topic))
		if topic == "" {
			return nil, nil
		}
		args := common.EmitArgs{
			ExtractorName:          extractorName,
			Transport:              txp,
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
		switch field {
		case "Publish", "PublishBatch":
			return true
		case "SendMessage", "SendMessageBatch", "ReceiveMessage", "DeleteMessage":
			return true
		}
		return false
	}) {
		field := ""
		if fn := c.Function; fn != nil && fn.Kind() == "selector_expression" {
			_, field = common.GoSelectorIdent(fn, src)
		}
		txp, publisher := classify(field)
		if txp == "" {
			continue
		}
		topic := extractARNorURL(c.Args, src, txp)
		if topic == "" {
			continue
		}
		ev, err := emit(topic, txp, c.Pos, publisher)
		if err != nil {
			return nil, err
		}
		out = append(out, ev...)
	}

	return common.SortEventsForDeterminism(common.DedupedEvents(out)), nil
}

// classify maps an AWS SDK method name to (transport, is_publisher).
// Returns ("", false) for irrelevant calls.
func classify(method string) (string, bool) {
	switch method {
	case "Publish", "PublishBatch":
		return common.TransportSNS, true
	case "SendMessage", "SendMessageBatch":
		return common.TransportSQS, true
	case "ReceiveMessage", "DeleteMessage":
		return common.TransportSQS, false
	}
	return "", false
}

// extractARNorURL walks the arg list looking for an
// `&sns.PublishInput{TopicArn: aws.String("...")}` /
// `&sqs.SendMessageInput{QueueUrl: aws.String("...")}` composite
// literal. Returns the ARN / URL string or "".
func extractARNorURL(args []*tree_sitter.Node, src []byte, txp string) string {
	field := "TopicArn"
	if txp == common.TransportSQS {
		field = "QueueUrl"
	}
	for _, a := range args {
		lit := unwrapComposite(a)
		if lit == nil {
			continue
		}
		v := common.GoCompositeLiteralField(lit, src, field)
		if v == nil {
			continue
		}
		// Look for aws.String("..."), bare "...", or aws.String(constant).
		if s, ok := common.GoStringLiteral(v, src); ok {
			return s
		}
		if v.Kind() == "call_expression" {
			fn := v.ChildByFieldName("function")
			if fn != nil && strings.HasSuffix(fn.Utf8Text(src), ".String") {
				ar := v.ChildByFieldName("arguments")
				if ar != nil {
					for i := uint(0); i < ar.NamedChildCount(); i++ {
						c := ar.NamedChild(i)
						if s, ok := common.GoStringLiteral(c, src); ok {
							return s
						}
					}
				}
			}
		}
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
		Frameworks: []string{"aws-sdk-go-v2", "aws-sdk-go"},
		Fallback:   code_framework.FallbackTreesitterOnly,
		BatchHint:  code_framework.BatchPerEvent,
		Inputs:     common.CommonInputs(),
		Outputs:    common.CommonOutputs(),
	})
}
