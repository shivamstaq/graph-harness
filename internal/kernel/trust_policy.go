// Package kernel — trust enforcement at the storage seam
// (P0.5.T18 / SPEC §9.1 writer-monopoly clause).
//
// The kernel is the only legitimate writer to layer SQLite stores.
// Layers reject writes that do not carry a kernel-issued write
// authority (the WriteToken capability defined here). Direct writes
// from non-daemon paths — rogue CLI commands, library callers,
// layer-internal code paths — fail loudly with ErrMissingKernelSeqTag.
//
// # Enforcement strategy
//
// Two layers of defense:
//
//  1. **In-process capability**: WriteToken is an opaque value
//     produced only via TrustPolicy.IssueWriteToken (called from
//     the daemon's writer loop). Public Store.* write methods
//     accept a WriteToken parameter; without one they refuse to
//     write. The token carries the kernel seq it was minted at,
//     which the layer stores as kernel_seq_tag on the row.
//
//  2. **Storage-seam tag** (deferred): an additive `kernel_seq_tag`
//     column on every layer's primary write tables, validated at
//     commit time. The column is shipped in this task; runtime
//     validation against the kernel head lands as the rollout
//     progresses (one layer at a time) so bench fixtures and
//     importers can be migrated in step. The full enforcement is
//     gated behind TrustPolicy.StrictMode — off by default during
//     migration, flipped on once every writer has been updated.
//
// # Whitelist
//
// Some non-daemon writers are legitimate by contract: the bench
// importer (`internal/bench`) loads fixtures into layer stores
// directly; the bench oracle reads + writes during ground-truth
// scoring. Those paths construct a WriteToken via
// TrustPolicy.IssueImporterToken(class) where class names the
// SourceClass that justifies the bypass. The token records the
// importer identity so audit logs can show "row X was written by
// `importer:bench_fixture`, not by the daemon."
//
// # Sentinel error
//
//	ErrMissingKernelSeqTag is the error layers return when a write
//	arrives without a valid WriteToken. The wire form is
//	"missing-kernel-seq-tag" — matched verbatim by the
//	tests/e2e/specs/substrate/trust-enforcement-rejects-untagged-
//	writes.yaml spec.
package kernel

import (
	"errors"
	"sync"
)

// ErrMissingKernelSeqTag is returned by layer adapters when a write
// arrives without a valid kernel-issued WriteToken. SPEC §9.1; the
// canonical user-facing message is "missing-kernel-seq-tag".
var ErrMissingKernelSeqTag = errors.New("missing-kernel-seq-tag")

// WriteToken is an opaque capability that proves a write call is
// authorized by the kernel. WriteTokens are constructed only via
// TrustPolicy.IssueWriteToken or TrustPolicy.IssueImporterToken;
// callers cannot fabricate one from the outside (struct fields are
// unexported), so a non-zero token in a write call body is
// cryptographic-style proof of authorization.
//
// The zero value of WriteToken is invalid — Store write methods
// MUST check Token.Valid() and refuse with ErrMissingKernelSeqTag
// when it returns false.
type WriteToken struct {
	authority writeAuthority
	seq       uint64
	source    SourceClass
}

// writeAuthority is the unexported tag that distinguishes legitimate
// WriteTokens from a struct-literal forgery. The kernel mints only
// values from this set; comparing against authorityZero detects a
// forged or zero-value token.
type writeAuthority uint8

const (
	authorityZero     writeAuthority = 0 // forged / zero — always rejected
	authorityKernel   writeAuthority = 1 // minted by kernel writer loop
	authorityImporter writeAuthority = 2 // minted for a whitelisted importer
)

// Valid reports whether the token carries a non-zero authority. A
// token returned by IssueWriteToken / IssueImporterToken is always
// Valid; the zero value is not.
func (t WriteToken) Valid() bool { return t.authority != authorityZero }

// Seq returns the kernel seq the token was minted at. Used by Store
// write methods to stamp the row's kernel_seq_tag column. Zero for
// importer tokens whose write does not correspond to a kernel event.
func (t WriteToken) Seq() uint64 { return t.seq }

// SourceClass reports the producer identity bound to the token.
// Audit logs surface this so operators can answer "who wrote row X."
func (t WriteToken) SourceClass() SourceClass { return t.source }

// IsImporter reports whether the token is an importer bypass.
func (t WriteToken) IsImporter() bool { return t.authority == authorityImporter }

// TrustPolicy is the kernel's authority on who may write to layer
// stores. One TrustPolicy per kernel; the daemon installs it on its
// owned Service at boot.
//
// Importers register via Whitelist(SourceClass) at startup; the
// embedded layer manifests list which source classes are allowed.
// Unknown SourceClasses passed to IssueImporterToken are rejected
// even in non-strict mode so the policy stays auditable.
type TrustPolicy struct {
	mu          sync.RWMutex
	whitelist   map[SourceClass]struct{}
	strict      bool
}

// NewTrustPolicy builds a fresh policy. Strict mode defaults to off
// during the P0.5 → P1 rollout; flip via SetStrict(true) once every
// writer has been migrated.
func NewTrustPolicy() *TrustPolicy {
	return &TrustPolicy{
		whitelist: map[SourceClass]struct{}{
			// Bench fixtures + bench oracle are the only non-daemon
			// writers whitelisted at v1 ship-bar; SPEC §6 lists them
			// explicitly. Additional importers register via
			// Whitelist() when their manifest declares the
			// `importer` capability.
			SourceImporterBenchFix:    {},
			SourceImporterBenchOracle: {},
		},
	}
}

// SetStrict toggles strict enforcement. In strict mode the policy
// also validates that the kernel seq stamped on the token matches
// the current head before issuing — preventing replay attacks where
// a token from an earlier seq is reused. Strict mode lands in P1+
// when the migration completes; the bool gate is here so the rollout
// is one switch, not a full refactor.
func (p *TrustPolicy) SetStrict(b bool) {
	p.mu.Lock()
	p.strict = b
	p.mu.Unlock()
}

// IsStrict reports the current strict-mode setting.
func (p *TrustPolicy) IsStrict() bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.strict
}

// Whitelist adds class to the importer-allowed set. Idempotent.
func (p *TrustPolicy) Whitelist(class SourceClass) {
	p.mu.Lock()
	p.whitelist[class] = struct{}{}
	p.mu.Unlock()
}

// IsWhitelisted reports whether class is in the importer-allowed set.
func (p *TrustPolicy) IsWhitelisted(class SourceClass) bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	_, ok := p.whitelist[class]
	return ok
}

// IssueWriteToken mints a daemon-authority token bound to the given
// kernel seq. Called by the daemon's writer loop immediately before
// invoking a layer write. The token is consumed by the Store API and
// stamped onto the row's kernel_seq_tag column.
func (p *TrustPolicy) IssueWriteToken(seq uint64) WriteToken {
	return WriteToken{authority: authorityKernel, seq: seq, source: SourceKernelReplay}
}

// IssueImporterToken mints an importer-authority token for a
// whitelisted SourceClass. Returns ErrMissingKernelSeqTag (used as
// the trust-rejection sentinel; the wire form matches the spec
// keyword) when class is not whitelisted, so callers can map it to
// the canonical error response.
func (p *TrustPolicy) IssueImporterToken(class SourceClass) (WriteToken, error) {
	if !p.IsWhitelisted(class) {
		return WriteToken{}, ErrMissingKernelSeqTag
	}
	return WriteToken{authority: authorityImporter, source: class}, nil
}

// Verify reports whether a token is currently acceptable for a
// write. In non-strict mode any Valid() token passes; in strict mode
// daemon tokens must match the head seq supplied by the caller
// (typically the kernel.EventLog's LastSeq()).
func (p *TrustPolicy) Verify(t WriteToken, headSeq uint64) error {
	if !t.Valid() {
		return ErrMissingKernelSeqTag
	}
	if !p.IsStrict() {
		return nil
	}
	if t.authority == authorityKernel && t.seq != headSeq {
		return ErrMissingKernelSeqTag
	}
	if t.authority == authorityImporter && !p.IsWhitelisted(t.source) {
		return ErrMissingKernelSeqTag
	}
	return nil
}
