// Package redispubsubgo is the events.redispubsub.go extractor: detects
// Redis pub/sub call patterns in Go source written against go-redis
// (redis/go-redis/v9 and its v8 predecessor) and gomodule/redigo.
//
// Patterns covered:
//
//	go-redis (v8/v9)
//	  Publisher:  rdb.Publish(ctx, "channel", payload)
//	  Subscriber: rdb.Subscribe(ctx, "channel-a", "channel-b")
//	              rdb.PSubscribe(ctx, "pattern.*")
//
//	gomodule/redigo
//	  Publisher:  conn.Do("PUBLISH", "channel", payload)
//	  Subscriber: psc.Subscribe("channel-a", "channel-b")
//
// Channel names must be string literals; non-literal channels are
// dropped per the v1 limitation.
package redispubsubgo

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
	extractorName = "events.redispubsub.go"
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

// Inputs returns the input event kinds.
func (e *Extractor) Inputs() []code_framework.EventKind { return common.CommonInputs() }

// Outputs returns the entity kinds emitted.
func (e *Extractor) Outputs() []code_framework.EntityKind { return common.CommonOutputs() }

// Capabilities advertises framework-family coverage.
func (e *Extractor) Capabilities() code_framework.Capabilities {
	return code_framework.Capabilities{
		Family:     "events",
		Languages:  []string{common.LanguageGo},
		Frameworks: []string{"go-redis", "gomodule/redigo"},
		Fallback:   code_framework.FallbackTreesitterOnly,
		BatchHint:  code_framework.BatchPerEvent,
	}
}

// OnEvent parses the changed Go file and emits Event + EventPublisher
// + EventSubscriber kernel events for each detected Redis pub/sub call.
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

	// go-redis: Publish(ctx, channel, payload)
	for _, c := range common.FindGoCalls(root, src, func(recv, field string) bool {
		return field == "Publish"
	}) {
		// channel is positional argument 1 (after ctx); fall back to arg 0
		// for non-ctx variants.
		topic := nthString(c.Args, src, 1)
		if topic == "" {
			topic = nthString(c.Args, src, 0)
		}
		if topic == "" {
			continue
		}
		ev, err := emit(topic, c.Pos, true)
		if err != nil {
			return nil, err
		}
		out = append(out, ev...)
	}

	// go-redis: Subscribe(ctx, channels...) / PSubscribe(ctx, patterns...)
	// redigo: psc.Subscribe(channels...)
	for _, c := range common.FindGoCalls(root, src, func(recv, field string) bool {
		return field == "Subscribe" || field == "PSubscribe"
	}) {
		for _, a := range c.Args {
			if s, ok := common.GoStringLiteral(a, src); ok {
				ev, err := emit(s, c.Pos, false)
				if err != nil {
					return nil, err
				}
				out = append(out, ev...)
			}
		}
	}

	// redigo: conn.Do("PUBLISH", "channel", payload)
	for _, c := range common.FindGoCalls(root, src, func(recv, field string) bool {
		return field == "Do" || field == "Send"
	}) {
		if len(c.Args) < 2 {
			continue
		}
		cmd, _ := common.GoStringLiteral(c.Args[0], src)
		if cmd != "PUBLISH" && cmd != "SUBSCRIBE" && cmd != "PSUBSCRIBE" {
			continue
		}
		topic, _ := common.GoStringLiteral(c.Args[1], src)
		if topic == "" {
			continue
		}
		ev, err := emit(topic, c.Pos, cmd == "PUBLISH")
		if err != nil {
			return nil, err
		}
		out = append(out, ev...)
	}

	return common.SortEventsForDeterminism(common.DedupedEvents(out)), nil
}

func nthString(args []*tree_sitter.Node, src []byte, n int) string {
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
		Frameworks: []string{"go-redis", "gomodule/redigo"},
		Fallback:   code_framework.FallbackTreesitterOnly,
		BatchHint:  code_framework.BatchPerEvent,
		Inputs:     common.CommonInputs(),
		Outputs:    common.CommonOutputs(),
	})
}
