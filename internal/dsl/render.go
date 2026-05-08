package dsl

import (
	"fmt"
	"strconv"
	"strings"
)

// Render turns a File AST back into canonical .gh source. Round-trip property:
// Parse → Render → Parse produces an identical AST. Used by the importer
// (semantic.overlay) to canonicalize on-disk files and by the test suite
// for round-trip property tests.
func Render(f *File) string {
	var b strings.Builder
	for i, d := range f.Decls {
		if i > 0 {
			b.WriteString("\n")
		}
		switch {
		case d.Import != nil:
			fmt.Fprintf(&b, "import %q as %s\n", d.Import.Path, d.Import.Alias)
		case d.Selector != nil:
			renderSelector(&b, d.Selector)
		case d.Flow != nil:
			renderFlow(&b, d.Flow)
		case d.Query != nil:
			fmt.Fprintf(&b, "query %q {\n%s\n}\n", d.Query.Name, strings.TrimSpace(d.Query.Body))
		case d.Rule != nil:
			// Should never reach here in P1 (validateSurface rejects), but
			// keep symmetric for future compatibility.
			fmt.Fprintf(&b, "rule %s(%s) :-%s.\n", d.Rule.Head, strings.TrimSpace(d.Rule.Args), strings.TrimSpace(d.Rule.Body))
		}
	}
	return b.String()
}

func renderSelector(b *strings.Builder, s *Selector) {
	fmt.Fprintf(b, "selector %s {\n", s.Name)
	if s.Unique {
		b.WriteString("  unique\n")
	}
	for _, a := range s.Anchors {
		renderAnchor(b, a, "  ")
	}
	if s.Thresholds != nil {
		renderThresholds(b, s.Thresholds, "  ")
	}
	b.WriteString("}\n")
}

func renderAnchor(b *strings.Builder, a *Anchor, indent string) {
	marker := a.Marker
	if marker == "" {
		marker = "anchor"
	}
	fmt.Fprintf(b, "%s%s %s", indent, marker, a.Kind)
	if a.Value != nil {
		b.WriteString(" ")
		writeAnchorValue(b, a.Value, indent)
	}
	b.WriteString("\n")
}

func renderThresholds(b *strings.Builder, t *Thresholds, indent string) {
	fmt.Fprintf(b, "%sthresholds {\n", indent)
	for _, it := range t.Items {
		fmt.Fprintf(b, "%s  %s: ", indent, it.Name)
		switch {
		case it.Float != nil:
			b.WriteString(formatFloat(*it.Float))
		case it.Int != nil:
			fmt.Fprintf(b, "%d", *it.Int)
		case it.Range != nil:
			fmt.Fprintf(b, "[%s, %s]", formatFloat(it.Range.Lo), formatFloat(it.Range.Hi))
		}
		b.WriteString("\n")
	}
	fmt.Fprintf(b, "%s}\n", indent)
}

func renderFlow(b *strings.Builder, f *Flow) {
	fmt.Fprintf(b, "flow %s {\n", f.Name)
	if f.Description != "" {
		fmt.Fprintf(b, "  description %q\n", f.Description)
	}
	if f.Scope != "" {
		fmt.Fprintf(b, "  scope %s\n", f.Scope)
	}
	if f.Risk != "" {
		fmt.Fprintf(b, "  risk %s\n", f.Risk)
	}
	for _, s := range f.Steps {
		fmt.Fprintf(b, "  step %s targets selector { ", s.Name)
		if s.Targets != nil && s.Targets.InlineSelector != nil {
			for i, a := range s.Targets.InlineSelector.Anchors {
				if i > 0 {
					b.WriteString("; ")
				}
				fmt.Fprintf(b, "%s ", a.Kind)
				writeAnchorValue(b, a.Value, "  ")
			}
		}
		b.WriteString(" }\n")
	}
	b.WriteString("}\n")
}

// writeAnchorValue is symmetric to AnchorValue's parser branches.
func writeAnchorValue(b *strings.Builder, v *AnchorValue, indent string) {
	if v == nil {
		return
	}
	switch {
	case v.Str != nil:
		fmt.Fprintf(b, "%q", *v.Str)
	case v.Int != nil:
		fmt.Fprintf(b, "%d", *v.Int)
	case v.Float != nil:
		b.WriteString(formatFloat(*v.Float))
	case v.Sig != nil:
		writeFunctionSig(b, v.Sig)
	case v.Neighbor != nil:
		writeCallNeighborhood(b, v.Neighbor, indent)
	}
}

func writeFunctionSig(b *strings.Builder, s *FunctionSig) {
	b.WriteString("sig(")
	for i, p := range s.Params {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(p)
	}
	b.WriteString(") -> ")
	if s.Return != nil {
		switch {
		case s.Return.Single != nil:
			b.WriteString(*s.Return.Single)
		case s.Return.Tuple != nil:
			b.WriteString("(")
			for i, t := range s.Return.Tuple.Items {
				if i > 0 {
					b.WriteString(", ")
				}
				b.WriteString(t)
			}
			b.WriteString(")")
		}
	}
}

func writeCallNeighborhood(b *strings.Builder, n *CallNeighborhood, indent string) {
	b.WriteString("{\n")
	if n.Callers != nil {
		fmt.Fprintf(b, "%s  callers: ", indent)
		writeStringList(b, n.Callers)
		b.WriteString("\n")
	}
	if n.Callees != nil {
		fmt.Fprintf(b, "%s  callees: ", indent)
		writeStringList(b, n.Callees)
		b.WriteString("\n")
	}
	fmt.Fprintf(b, "%s}", indent)
}

func writeStringList(b *strings.Builder, sl *StringList) {
	b.WriteString("[")
	for i, s := range sl.Items {
		if i > 0 {
			b.WriteString(", ")
		}
		fmt.Fprintf(b, "%q", s)
	}
	b.WriteString("]")
}

// formatFloat keeps round-trip stability: a parse of "0.95" must render as
// "0.95" and re-parse to the same float64. Using strconv.FormatFloat with -1
// precision gives the shortest representation that round-trips bit-exactly.
func formatFloat(f float64) string {
	s := strconv.FormatFloat(f, 'f', -1, 64)
	// Ensure at least one decimal so the lexer keeps Float tokenization
	// (Int regex would otherwise consume "0").
	if !strings.ContainsRune(s, '.') {
		s += ".0"
	}
	return s
}
