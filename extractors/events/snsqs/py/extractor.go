// Package snsqspy is the events.snsqs.py extractor: detects AWS SNS
// Publish and SQS send_message / receive_message calls in Python source
// written against boto3.
//
// Patterns covered:
//
//	SNS:  client.publish(TopicArn="arn:...:order-events", Message="...")
//	SQS:  client.send_message(QueueUrl="https://.../orders", MessageBody="...")
//	      client.receive_message(QueueUrl="https://.../audit-queue")
package snsqspy

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/shivamstaq/graph-harness/extractors/events/common"
	"github.com/shivamstaq/graph-harness/internal/code_framework"
	"github.com/shivamstaq/graph-harness/internal/kernel"
)

const extractorName = "events.snsqs.py"

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
		Frameworks: []string{"boto3"},
		Fallback:   code_framework.FallbackTreesitterOnly,
		BatchHint:  code_framework.BatchPerEvent,
	}
}

// OnEvent parses the Python file and emits SNS/SQS entity events.
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

// classify maps a boto3 method name to (transport, is_publisher, kwarg_name).
// Returns ("", false, "") for irrelevant methods.
func classify(method string) (string, bool, string) {
	switch method {
	case "publish":
		return common.TransportSNS, true, "TopicArn"
	case "publish_batch":
		return common.TransportSNS, true, "TopicArn"
	case "send_message", "send_message_batch":
		return common.TransportSQS, true, "QueueUrl"
	case "receive_message", "delete_message":
		return common.TransportSQS, false, "QueueUrl"
	}
	return "", false, ""
}

func scan(path string, src []byte) ([]kernel.Event, error) {
	tree, err := common.ParsePythonSource(src)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	defer tree.Close()
	root := tree.RootNode()
	module := common.PyModuleName(path)

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
			EnclosingQualifiedName: common.PyEnclosingQualifiedName(root, src, module, pos),
		}
		if publisher {
			return common.EmitPublisher(args)
		}
		return common.EmitSubscriber(args)
	}

	var out []kernel.Event
	for _, c := range common.FindPyCalls(root, src, func(obj, attr string) bool {
		txp, _, _ := classify(attr)
		return txp != ""
	}) {
		attr := ""
		if fn := c.Function; fn != nil && fn.Kind() == "attribute" {
			_, attr = common.PyAttribute(fn, src)
		}
		txp, publisher, kwarg := classify(attr)
		if txp == "" {
			continue
		}
		v := common.PyKeywordArg(c.ArgsNode, src, kwarg)
		if v == nil {
			continue
		}
		topic, ok := common.PyStringLiteral(v, src)
		if !ok {
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

func init() {
	code_framework.Register(extractorName, New, code_framework.Descriptor{
		Family:     "events",
		Languages:  []string{common.LanguagePython},
		Frameworks: []string{"boto3"},
		Fallback:   code_framework.FallbackTreesitterOnly,
		BatchHint:  code_framework.BatchPerEvent,
		Inputs:     common.CommonInputs(),
		Outputs:    common.CommonOutputs(),
	})
}
