package common

import (
	"encoding/json"

	"github.com/shivamstaq/graph-harness/internal/code_framework"
	"github.com/shivamstaq/graph-harness/internal/kernel"
)

// EmitTest renders a code_framework.Test entity into a kernel.Event
// shaped as `TestAdded`. The Dispatcher stamps Seq/Tx/Layer at append
// time per SPEC §6.16 — the extractor only fills the payload + Kind +
// Subject pointer.
func EmitTest(t code_framework.Test) kernel.Event {
	t.Kind = code_framework.KindTest
	payload, _ := json.Marshal(t)
	return kernel.Event{
		Kind: "TestAdded",
		Subject: &kernel.EntityRef{
			Layer: "code.framework",
			Kind:  string(code_framework.KindTest),
			ID:    t.ID,
		},
		Payload: payload,
	}
}

// EmitFixture renders a Fixture entity into a `FixtureAdded` event.
func EmitFixture(f code_framework.Fixture) kernel.Event {
	f.Kind = code_framework.KindFixture
	payload, _ := json.Marshal(f)
	return kernel.Event{
		Kind: "FixtureAdded",
		Subject: &kernel.EntityRef{
			Layer: "code.framework",
			Kind:  string(code_framework.KindFixture),
			ID:    f.ID,
		},
		Payload: payload,
	}
}

// EmitContractTest renders a ContractTest entity into a
// `ContractTestAdded` event.
func EmitContractTest(c code_framework.ContractTest) kernel.Event {
	c.Kind = code_framework.KindContractTest
	payload, _ := json.Marshal(c)
	return kernel.Event{
		Kind: "ContractTestAdded",
		Subject: &kernel.EntityRef{
			Layer: "code.framework",
			Kind:  string(code_framework.KindContractTest),
			ID:    c.ID,
		},
		Payload: payload,
	}
}
