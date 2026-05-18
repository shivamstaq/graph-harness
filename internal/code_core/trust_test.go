package code_core

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/shivamstaq/graph-harness/internal/kernel"
)

// TestPutEntityWithToken_RejectsUntaggedWrite asserts the SPEC §9.1
// writer-monopoly invariant: with a TrustPolicy installed, writes
// missing a valid kernel.WriteToken fail with ErrMissingKernelSeqTag.
func TestPutEntityWithToken_RejectsUntaggedWrite(t *testing.T) {
	t.Parallel()
	store := newTrustStore(t)
	store.SetTrustPolicy(kernel.NewTrustPolicy())

	// Zero WriteToken — forged / not minted by the kernel — must be
	// rejected outright. This is the path the trust-enforcement
	// e2e spec exercises: a non-daemon caller cannot construct a
	// non-zero WriteToken because the type's fields are unexported.
	err := store.PutEntityWithToken(context.Background(),
		Entity{ID: "rogue", Kind: KindFunction, QualifiedName: "Rogue"},
		kernel.WriteToken{}, 0)
	if !errors.Is(err, kernel.ErrMissingKernelSeqTag) {
		t.Fatalf("forged token: want ErrMissingKernelSeqTag, got %v", err)
	}
}

// TestPutEntityWithToken_AcceptsKernelToken asserts the happy path:
// a kernel-issued WriteToken passes verification and the row is
// stamped with the matching kernel_seq_tag column.
func TestPutEntityWithToken_AcceptsKernelToken(t *testing.T) {
	t.Parallel()
	store := newTrustStore(t)
	policy := kernel.NewTrustPolicy()
	store.SetTrustPolicy(policy)

	tok := policy.IssueWriteToken(42)
	if err := store.PutEntityWithToken(context.Background(),
		Entity{ID: "legit1234567890abcdef", Kind: KindFunction, QualifiedName: "Legit"},
		tok, 42); err != nil {
		t.Fatalf("kernel-issued token rejected: %v", err)
	}

	// Confirm the kernel_seq_tag was stamped on the row. We query the
	// raw column rather than going through a typed API since the tag
	// is intentionally not part of the public Entity shape — it is an
	// audit column, not load-bearing for any read path.
	row := store.db.QueryRowContext(context.Background(),
		`SELECT kernel_seq_tag FROM code_entities WHERE id = ?`, "legit1234567890abcdef")
	var tag uint64
	if err := row.Scan(&tag); err != nil {
		t.Fatalf("scan tag: %v", err)
	}
	if tag != 42 {
		t.Errorf("kernel_seq_tag: want 42, got %d", tag)
	}
}

// TestAuditUntaggedRows_FindsLegacyWrites confirms the audit helper
// flags any row that bypassed the tokenized write path. Used by
// post-migration smoke tests to gate flipping strict mode on.
//
// Post-P1.5.T07 every PutEntity call stamps kernel_seq_tag automatically
// (the column comes from createdSeq when no policy is installed). The
// audit's role shifted from "find PutEntity callers" to "find rows
// inserted by a writer that bypassed the Store API entirely" — i.e. a
// rogue process writing direct SQL. We simulate that with an explicit
// direct INSERT and assert the audit catches it.
func TestAuditUntaggedRows_FindsLegacyWrites(t *testing.T) {
	t.Parallel()
	store := newTrustStore(t)
	// Direct INSERT — bypasses Store.PutEntity entirely. Pre-P1.5.T07
	// this was the shape every untoken'd PutEntity call took
	// (kernel_seq_tag = 0 by default). Modeled here so the audit
	// retains a regression signal against a future writer that
	// bypasses the Store API.
	if _, err := store.db.ExecContext(context.Background(),
		`INSERT INTO code_entities (id, kind, language_id, qualified_name, created_seq)
		 VALUES (?, ?, '', ?, ?)`,
		"untagged", string(KindFunction), "Untagged", 99); err != nil {
		t.Fatalf("direct INSERT: %v", err)
	}
	rows, err := store.AuditUntaggedRows(context.Background(), 0)
	if err != nil {
		t.Fatalf("audit: %v", err)
	}
	found := false
	for _, id := range rows {
		if id == "untagged" {
			found = true
		}
	}
	if !found {
		t.Errorf("audit failed to flag untagged row; got %v", rows)
	}
}

// TestStrictMode_RejectsWriteWithoutIssuer asserts that flipping
// Strict(true) without installing a TokenIssuer makes the auto-mint
// path on PutEntity fail with ErrMissingKernelSeqTag. The daemon
// installs both at boot; if either is missing the writer-monopoly
// invariant cannot hold and we want a loud failure.
func TestStrictMode_RejectsWriteWithoutIssuer(t *testing.T) {
	t.Parallel()
	store := newTrustStore(t)
	policy := kernel.NewTrustPolicy()
	policy.SetStrict(true)
	store.SetTrustPolicy(policy)
	// Deliberately do NOT install a TokenIssuer.
	err := store.PutEntity(context.Background(),
		Entity{ID: "rejected", Kind: KindFunction, QualifiedName: "Rejected"},
		77)
	if !errors.Is(err, kernel.ErrMissingKernelSeqTag) {
		t.Fatalf("strict + no issuer should reject; got %v", err)
	}
}

// TestStrictMode_AcceptsWriteWithDaemonIssuer asserts the production
// path: daemon installs policy + issuer + strict, every PutEntity
// auto-mints a daemon-authority token and stamps kernel_seq_tag.
func TestStrictMode_AcceptsWriteWithDaemonIssuer(t *testing.T) {
	t.Parallel()
	store := newTrustStore(t)
	policy := kernel.NewTrustPolicy()
	policy.SetStrict(true)
	store.SetTrustPolicy(policy)
	store.SetTokenIssuer(TokenIssuerFunc(policy.IssueWriteToken))

	if err := store.PutEntity(context.Background(),
		Entity{ID: "accepted", Kind: KindFunction, QualifiedName: "Accepted"},
		88); err != nil {
		t.Fatalf("strict + issuer should accept; got %v", err)
	}
	rows, err := store.AuditUntaggedRows(context.Background(), 0)
	if err != nil {
		t.Fatalf("audit: %v", err)
	}
	for _, id := range rows {
		if id == "accepted" {
			t.Fatalf("auto-minted write missing tag; audit returned %v", rows)
		}
	}
}

// TestPutEntityWithToken_NoPolicyIsPermissive confirms backward
// compatibility: without a TrustPolicy installed, the token-gated
// path falls through to PutEntity. The bench fixtures + every
// non-daemon test depend on this during the migration window.
func TestPutEntityWithToken_NoPolicyIsPermissive(t *testing.T) {
	t.Parallel()
	store := newTrustStore(t)
	// No SetTrustPolicy call; default is nil → permissive.
	if err := store.PutEntityWithToken(context.Background(),
		Entity{ID: "permissive", Kind: KindFunction, QualifiedName: "Permissive"},
		kernel.WriteToken{}, 0); err != nil {
		t.Errorf("permissive mode rejected write: %v", err)
	}
}

func newTrustStore(t *testing.T) *Store {
	t.Helper()
	dir := t.TempDir()
	dsn := filepath.Join(dir, "trust.db") + "?_pragma=journal_mode(WAL)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store, err := NewStore(db)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	return store
}
