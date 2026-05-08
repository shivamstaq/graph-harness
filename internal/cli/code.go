package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/shivamstaq/graph-harness/internal/code_core"
	"github.com/shivamstaq/graph-harness/internal/facts"
	"github.com/shivamstaq/graph-harness/internal/kernel"
)

// newCodeCmd is the parent for `graph-harness code …` subcommands —
// the CLI surface for code.core inspection. The subcommands expose
// the consumer-facing API in [code_core] so e2e specs can assert on
// entity provenance, the SPEC §4.5 fold, and code.core events
// without reaching into SQL.
func newCodeCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "code",
		Short: "Inspect code.core entities, provenance, and events",
	}
	c.AddCommand(
		newCodeProvenanceCmd(),
		newCodeEventsCmd(),
		newCodeListCmd(),
	)
	return c
}

// newCodeListCmd implements `graph-harness code list [--qualified-name
// X] [--language Y]`. Walks the code_entities table and emits every
// row matching the filters as one EntityView per line of NDJSON
// (default) or one row per line of human-readable text. Used by
// e2e specs that need to assert on multi-entity sets — e.g. "User.save
// in Go and Python both materialize as distinct entities" — without
// reaching into SQLite directly.
func newCodeListCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "list",
		Short: "List code.core entities matching optional filters",
		RunE: func(cmd *cobra.Command, _ []string) error {
			ws, err := activeWorkspace()
			if err != nil {
				return err
			}
			ctx := context.Background()
			log, err := facts.OpenEventLog(ws.EventLog)
			if err != nil {
				return err
			}
			defer func() { _ = log.Close() }()
			store, db, err := openCodeStore(ws)
			if err != nil {
				return err
			}
			defer func() { _ = db.Close() }()
			if err := indexWorkspaceCode(ctx, ws, store, log); err != nil {
				return err
			}

			qnFilter, _ := cmd.Flags().GetString("qualified-name")
			langFilter, _ := cmd.Flags().GetString("language")
			asJSON, _ := cmd.Flags().GetBool("json")
			rows, err := store.ListEntities(ctx, code_core.ListFilter{
				QualifiedName: qnFilter,
				LanguageID:    langFilter,
			})
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			for _, ent := range rows {
				if asJSON {
					view, err := store.LookupEntity(ctx, ent.ID)
					if err != nil {
						return err
					}
					buf, err := json.Marshal(view)
					if err != nil {
						return err
					}
					_, _ = fmt.Fprintln(out, string(buf))
					continue
				}
				_, _ = fmt.Fprintf(out, "%s  %s  %s  %s\n",
					ent.ID[:12], ent.Kind, ent.LanguageID, ent.QualifiedName)
			}
			if !asJSON {
				_, _ = fmt.Fprintf(out, "%d entities\n", len(rows))
			}
			return nil
		},
	}
	c.Flags().String("qualified-name", "", "filter by exact qualified_name")
	c.Flags().String("language", "", "filter by language_id (e.g. go, typescript, python)")
	c.Flags().Bool("json", false, "emit one EntityView NDJSON line per row")
	return c
}

// newCodeProvenanceCmd implements `graph-harness code provenance
// <id-or-qualified-name>`. Looks the entity up by content-addressable
// ID first; falls back to qualified-name lookup so spec authors can
// write fixture-friendly names rather than tracking SHA256s. Emits
// the [code_core.EntityView] (entity row + folded provenance summary
// + per-source claims) — the same shape T-surfaces-bench's MCP
// `gh://entity/code.core/<kind>/<id>` resource and the JSON-RPC
// `entity.provenance` method return.
//
// Default rendering is human-readable; --json emits the EntityView
// verbatim for spec-friendly grep/contains assertions.
func newCodeProvenanceCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "provenance <id-or-qualified-name>",
		Short: "Print the entity + folded provenance for a code.core entity",
		Long: "Looks up the entity by content-addressable ID first; falls back to " +
			"qualified-name lookup. Returns the same EntityView shape served by the " +
			"JSON-RPC entity.provenance method and the gh://entity/code.core/<kind>/<id> " +
			"MCP resource — entity attributes plus a SPEC §4.5 provenance fold " +
			"(SourceCount, Confidence=min, Freshness=worst, LatestSeenSeq=max) and " +
			"the per-source claim list in canonical source_class order.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ws, err := activeWorkspace()
			if err != nil {
				return err
			}
			ctx := context.Background()
			log, err := facts.OpenEventLog(ws.EventLog)
			if err != nil {
				return err
			}
			defer func() { _ = log.Close() }()
			store, db, err := openCodeStore(ws)
			if err != nil {
				return err
			}
			defer func() { _ = db.Close() }()
			if err := indexWorkspaceCode(ctx, ws, store, log); err != nil {
				return err
			}

			needle := args[0]
			lang, _ := cmd.Flags().GetString("language")

			var view code_core.EntityView
			// When --language is set we always treat the positional
			// argument as a qualified_name and narrow by language —
			// this is the disambiguator for cross-language name
			// collisions like Go vs Python `User.save`.
			if lang != "" {
				rows, err := store.ListEntities(ctx, code_core.ListFilter{
					QualifiedName: needle,
					LanguageID:    lang,
				})
				if err != nil {
					return err
				}
				if len(rows) == 0 {
					return fmt.Errorf("no code.core entity found for qualified_name=%q language=%q", needle, lang)
				}
				view, err = store.LookupEntity(ctx, rows[0].ID)
				if err != nil {
					return err
				}
			} else {
				v, err := store.LookupEntity(ctx, needle)
				if err != nil && !errors.Is(err, code_core.ErrEntityNotFound) {
					return err
				}
				if errors.Is(err, code_core.ErrEntityNotFound) {
					// Fallback: treat the argument as a qualified name.
					ent, qnErr := store.LookupByQualifiedName(ctx, needle)
					if qnErr != nil {
						return qnErr
					}
					if ent == nil {
						return fmt.Errorf("no code.core entity found for %q (tried id and qualified_name)", needle)
					}
					v, err = store.LookupEntity(ctx, ent.ID)
					if err != nil {
						return err
					}
				}
				view = v
			}

			asJSON, _ := cmd.Flags().GetBool("json")
			if asJSON {
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")
				return enc.Encode(view)
			}
			return renderEntityView(cmd, view)
		},
	}
	c.Flags().Bool("json", false, "emit EntityView as JSON")
	c.Flags().String("language", "", "narrow lookup to a specific language_id (e.g. go, python) — required when the positional arg is a qualified_name that collides across languages")
	return c
}

func renderEntityView(cmd *cobra.Command, v code_core.EntityView) error {
	out := cmd.OutOrStdout()
	_, _ = fmt.Fprintf(out, "entity %s\n", v.Entity.ID)
	_, _ = fmt.Fprintf(out, "  kind:           %s\n", v.Entity.Kind)
	_, _ = fmt.Fprintf(out, "  language_id:    %s\n", v.Entity.LanguageID)
	_, _ = fmt.Fprintf(out, "  qualified_name: %s\n", v.Entity.QualifiedName)
	if v.Entity.Receiver != "" {
		_, _ = fmt.Fprintf(out, "  receiver:       %s\n", v.Entity.Receiver)
	}
	if v.Entity.Path != "" {
		_, _ = fmt.Fprintf(out, "  path:           %s\n", v.Entity.Path)
	}
	if v.Entity.NormalizedSignature != "" {
		_, _ = fmt.Fprintf(out, "  signature:      %s\n", v.Entity.NormalizedSignature)
	}
	_, _ = fmt.Fprintln(out, "  provenance:")
	_, _ = fmt.Fprintf(out, "    source_count:    %d\n", v.Provenance.Summary.SourceCount)
	_, _ = fmt.Fprintf(out, "    confidence:      %.2f\n", v.Provenance.Summary.Confidence)
	_, _ = fmt.Fprintf(out, "    freshness:       %s\n", v.Provenance.Summary.Freshness)
	_, _ = fmt.Fprintf(out, "    latest_seen_seq: %d\n", v.Provenance.Summary.LatestSeenSeq)
	_, _ = fmt.Fprintln(out, "    sources:")
	for _, s := range v.Provenance.Sources {
		_, _ = fmt.Fprintf(out, "      - %s (conf=%.2f, freshness=%s, seq=%d, by=%s)\n",
			s.SourceClass, s.Confidence, s.Freshness, s.LastSeenSeq, s.ProducedBy)
	}
	return nil
}

// CodeEventRow is the JSON shape emitted by `graph-harness code events
// --json`. It is a flattened projection of [kernel.Event] plus the
// inlined payload as parsed JSON so spec assertions can `contains`
// against payload fields without having to escape an inner string.
type CodeEventRow struct {
	Seq        uint64            `json:"seq"`
	TS         string            `json:"ts"`
	Layer      string            `json:"layer"`
	Kind       string            `json:"kind"`
	Subject    *kernel.EntityRef `json:"subject,omitempty"`
	Payload    json.RawMessage   `json:"payload,omitempty"`
	ProducedBy string            `json:"produced_by,omitempty"`
}

// newCodeEventsCmd implements `graph-harness code events [--kind X]`.
// Walks the kernel event log via [facts.EventLog.ReadAll] and prints
// every `code.core` event (or just those matching --kind, e.g.
// SymbolDisambiguation). Used by e2e specs that need to verify the
// three-source unifier emitted the expected events.
//
// The default human-readable rendering shows seq, kind, and a short
// payload preview; --json emits each event as one line of compact
// JSON so spec assertions can `contains` on specific fields.
func newCodeEventsCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "events",
		Short: "Print code.core events from the kernel event log",
		Long: "Walks the kernel event log and emits every event in the code.core layer. " +
			"--kind filters to a single kind (e.g. SymbolDisambiguation, EntityMaterialized). " +
			"--json emits NDJSON, one event per line, so spec authors can grep/contains " +
			"against payload fields.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			ws, err := activeWorkspace()
			if err != nil {
				return err
			}
			ctx := context.Background()
			log, err := facts.OpenEventLog(ws.EventLog)
			if err != nil {
				return err
			}
			defer func() { _ = log.Close() }()

			// Re-index so any newly-appended events from a freshly
			// modified workspace are reflected; no-op when nothing
			// changed since identity is content-addressable.
			store, db, err := openCodeStore(ws)
			if err != nil {
				return err
			}
			defer func() { _ = db.Close() }()
			if err := indexWorkspaceCode(ctx, ws, store, log); err != nil {
				return err
			}

			kindFilter, _ := cmd.Flags().GetString("kind")
			asJSON, _ := cmd.Flags().GetBool("json")
			events, err := log.ReadAll(ctx)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			matched := 0
			for _, ev := range events {
				if ev.Layer != "code.core" {
					continue
				}
				if kindFilter != "" && ev.Kind != kindFilter {
					continue
				}
				matched++
				if asJSON {
					row := CodeEventRow{
						Seq:        ev.Seq,
						TS:         ev.TS.UTC().Format("2006-01-02T15:04:05.000000000Z07:00"),
						Layer:      ev.Layer,
						Kind:       ev.Kind,
						Subject:    ev.Subject,
						Payload:    ev.Payload,
						ProducedBy: string(ev.ProducedBy),
					}
					buf, err := json.Marshal(row)
					if err != nil {
						return err
					}
					_, _ = fmt.Fprintln(out, string(buf))
					continue
				}
				_, _ = fmt.Fprintf(out, "[%d] %s.%s\n", ev.Seq, ev.Layer, ev.Kind)
				if len(ev.Payload) > 0 {
					_, _ = fmt.Fprintf(out, "    payload: %s\n", string(ev.Payload))
				}
			}
			if !asJSON {
				_, _ = fmt.Fprintf(out, "%d code.core events\n", matched)
			}
			return nil
		},
	}
	c.Flags().String("kind", "", "filter by event kind (e.g. SymbolDisambiguation)")
	c.Flags().Bool("json", false, "emit NDJSON, one event per line")
	return c
}
