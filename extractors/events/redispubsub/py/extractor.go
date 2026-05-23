// Package redispubsubpy is the events.redispubsub.py extractor: detects
// Redis pub/sub patterns in Python source written against redis-py.
//
// Patterns covered:
//
//	Publisher:  r.publish("channel", payload)
//	Subscriber: pubsub.subscribe("ch1", "ch2"), pubsub.psubscribe("pattern.*")
//	            pubsub.subscribe(**{"ch1": handler})
package redispubsubpy

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
	extractorName = "events.redispubsub.py"
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
		Languages:  []string{common.LanguagePython},
		Frameworks: []string{"redis-py"},
		Fallback:   code_framework.FallbackTreesitterOnly,
		BatchHint:  code_framework.BatchPerEvent,
	}
}

// OnEvent parses the Python file and emits Redis pubsub events.
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
		case "publish", "subscribe", "psubscribe":
			return true
		}
		return false
	}) {
		attr := ""
		if fn := c.Function; fn != nil && fn.Kind() == "attribute" {
			_, attr = common.PyAttribute(fn, src)
		} else if fn != nil && fn.Kind() == "identifier" {
			attr = fn.Utf8Text(src)
		}
		switch attr {
		case "publish":
			topic, _ := common.PyFirstStringArg(c.Args, src)
			if topic == "" {
				continue
			}
			ev, err := emit(topic, c.Pos, true)
			if err != nil {
				return nil, err
			}
			out = append(out, ev...)
		default:
			// subscribe / psubscribe — every positional string arg is a channel.
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
	}

	return common.SortEventsForDeterminism(common.DedupedEvents(out)), nil
}

func init() {
	code_framework.Register(extractorName, New, code_framework.Descriptor{
		Family:     "events",
		Languages:  []string{common.LanguagePython},
		Frameworks: []string{"redis-py"},
		Fallback:   code_framework.FallbackTreesitterOnly,
		BatchHint:  code_framework.BatchPerEvent,
		Inputs:     common.CommonInputs(),
		Outputs:    common.CommonOutputs(),
	})
}
