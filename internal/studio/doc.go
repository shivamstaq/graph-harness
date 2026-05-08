// Package studio is the local-first web UI for the semantic layer.
//
// Studio runs:
//   - HTTP server bound loopback-only with a per-session random token + strict
//     Origin check (SPEC §9.6 — these protections are non-negotiable).
//   - Embedded SPA shell via Go's embed.FS — vanilla HTML/CSS/JS today;
//     SolidJS / React decision deferred to P3 per phase risk register.
//   - Three pages: .gh overlay file editor (with kind-aware anchor-authoring
//     forms — P1.I/T34), selector live-preview against code.core (SPEC §11.4),
//     and an entity drill-down that surfaces per-source provenance (P1.I/T35).
//
// All RPCs go through the daemon's JSON-RPC surface (selectors.preview,
// selectors.test, overlay.save, status). The Studio writes overlay edits
// as canonical .gh files to disk via overlay.save, then the .gh watcher in
// semantic.overlay re-imports — Studio never bypasses the canonical event
// path.
//
// SPEC: §9.6 (Studio), §11.3 (authoring UX), §11.4 (live selector preview).
package studio
