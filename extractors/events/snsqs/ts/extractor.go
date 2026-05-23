// Package snsqsts is the events.snsqs.ts extractor: detects AWS SNS
// Publish and SQS SendMessage / ReceiveMessage call patterns in TS/JS
// source using @aws-sdk/client-sns and @aws-sdk/client-sqs.
//
// Patterns covered:
//
//	SNS:  client.send(new PublishCommand({TopicArn: "arn:...:topic", Message: "..."}))
//	SQS:  client.send(new SendMessageCommand({QueueUrl: "https://.../q", MessageBody: "..."}))
//	      client.send(new ReceiveMessageCommand({QueueUrl: "https://.../q"}))
//
// The send() call wraps a *Command constructor whose options object
// carries TopicArn / QueueUrl. We walk both the send(...) call's first
// arg (a new_expression) and bare PublishInput / SendMessageInput
// constructions.
package snsqsts

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

const extractorName = "events.snsqs.ts"

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
		Languages:  []string{common.LanguageTypeScript},
		Frameworks: []string{"@aws-sdk/client-sns", "@aws-sdk/client-sqs"},
		Fallback:   code_framework.FallbackTreesitterOnly,
		BatchHint:  code_framework.BatchPerEvent,
	}
}

// OnEvent parses the TS file and emits Event / Publisher / Subscriber events.
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

// classify maps a Command constructor name to (transport, is_publisher).
// Returns ("", false) for irrelevant commands.
func classify(name string) (string, bool) {
	switch name {
	case "PublishCommand", "PublishBatchCommand":
		return common.TransportSNS, true
	case "SendMessageCommand", "SendMessageBatchCommand":
		return common.TransportSQS, true
	case "ReceiveMessageCommand", "DeleteMessageCommand":
		return common.TransportSQS, false
	}
	return "", false
}

func scan(path string, src []byte) ([]kernel.Event, error) {
	tree, err := common.ParseTypeScriptSource(path, src)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	defer tree.Close()
	root := tree.RootNode()
	module := common.TSModuleName(path)

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
			EnclosingQualifiedName: common.TSEnclosingQualifiedName(root, src, module, pos),
		}
		if publisher {
			return common.EmitPublisher(args)
		}
		return common.EmitSubscriber(args)
	}

	var out []kernel.Event

	// Walk every new_expression looking for a constructor we care about.
	common.Walk(root, func(n *tree_sitter.Node) bool {
		if n.Kind() != "new_expression" {
			return true
		}
		ctor := n.ChildByFieldName("constructor")
		if ctor == nil {
			return true
		}
		ctorName := ctor.Utf8Text(src)
		txp, publisher := classify(ctorName)
		if txp == "" {
			return true
		}
		argsNode := n.ChildByFieldName("arguments")
		if argsNode == nil {
			return true
		}
		// First arg is the options object.
		var opts *tree_sitter.Node
		for i := uint(0); i < argsNode.NamedChildCount(); i++ {
			c := argsNode.NamedChild(i)
			if c == nil {
				continue
			}
			if c.Kind() == "object" || c.Kind() == "object_expression" {
				opts = c
				break
			}
		}
		if opts == nil {
			return true
		}
		field := "TopicArn"
		if txp == common.TransportSQS {
			field = "QueueUrl"
		}
		v := common.TSObjectField(opts, src, field)
		if v == nil {
			return true
		}
		topic, ok := common.TSStringLiteral(v, src)
		if !ok {
			return true
		}
		ev, err := emit(topic, txp, uint32(n.StartByte()), publisher)
		if err == nil {
			out = append(out, ev...)
		}
		return true
	})

	return common.SortEventsForDeterminism(common.DedupedEvents(out)), nil
}

func init() {
	code_framework.Register(extractorName, New, code_framework.Descriptor{
		Family:     "events",
		Languages:  []string{common.LanguageTypeScript},
		Frameworks: []string{"@aws-sdk/client-sns", "@aws-sdk/client-sqs"},
		Fallback:   code_framework.FallbackTreesitterOnly,
		BatchHint:  code_framework.BatchPerEvent,
		Inputs:     common.CommonInputs(),
		Outputs:    common.CommonOutputs(),
	})
}
