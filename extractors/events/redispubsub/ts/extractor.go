// Package redispubsubts is the events.redispubsub.ts extractor: detects
// Redis pub/sub patterns in TS/JS source using ioredis or node-redis.
//
// Patterns covered:
//
//	Publisher:  redis.publish("channel", payload)
//	Subscriber: redis.subscribe("channel"), redis.psubscribe("pattern.*")
package redispubsubts

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
	extractorName = "events.redispubsub.ts"
	transport     = common.TransportRedisPubSub
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
		Languages:  []string{common.LanguageTypeScript},
		Frameworks: []string{"ioredis", "node-redis"},
		Fallback:   code_framework.FallbackTreesitterOnly,
		BatchHint:  code_framework.BatchPerEvent,
	}
}

// OnEvent parses the TS file and emits Redis pubsub events.
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
		case "publish", "subscribe", "psubscribe":
			return true
		}
		return false
	}) {
		method := ""
		if fn := c.Function; fn != nil && fn.Kind() == "member_expression" {
			_, method = common.TSMemberAccess(fn, src)
		}
		// Argument 0 is the channel. ioredis subscribe takes variadic args.
		for i, a := range c.Args {
			s, ok := common.TSStringLiteral(a, src)
			if !ok {
				break
			}
			publisher := method == "publish"
			ev, err := emit(s, c.Pos, publisher)
			if err != nil {
				return nil, err
			}
			out = append(out, ev...)
			// publish takes exactly one channel; stop after first string.
			if publisher {
				break
			}
			_ = i
		}
	}
	return common.SortEventsForDeterminism(common.DedupedEvents(out)), nil
}

func init() {
	code_framework.Register(extractorName, New, code_framework.Descriptor{
		Family:     "events",
		Languages:  []string{common.LanguageTypeScript},
		Frameworks: []string{"ioredis", "node-redis"},
		Fallback:   code_framework.FallbackTreesitterOnly,
		BatchHint:  code_framework.BatchPerEvent,
		Inputs:     common.CommonInputs(),
		Outputs:    common.CommonOutputs(),
	})
}
