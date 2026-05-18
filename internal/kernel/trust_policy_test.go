package kernel

import (
	"errors"
	"testing"
)

func TestTrustPolicy_ZeroTokenRejected(t *testing.T) {
	p := NewTrustPolicy()
	if err := p.Verify(WriteToken{}, 0); !errors.Is(err, ErrMissingKernelSeqTag) {
		t.Errorf("zero token: want ErrMissingKernelSeqTag, got %v", err)
	}
}

func TestTrustPolicy_KernelTokenAccepted(t *testing.T) {
	p := NewTrustPolicy()
	tok := p.IssueWriteToken(42)
	if !tok.Valid() {
		t.Errorf("kernel token must be Valid()")
	}
	if tok.Seq() != 42 {
		t.Errorf("seq: want 42, got %d", tok.Seq())
	}
	if err := p.Verify(tok, 99); err != nil {
		t.Errorf("non-strict verify should accept any valid token; got %v", err)
	}
}

func TestTrustPolicy_ImporterRequiresWhitelist(t *testing.T) {
	p := NewTrustPolicy()
	// SourceImporterBenchFix is whitelisted by default.
	tok, err := p.IssueImporterToken(SourceImporterBenchFix)
	if err != nil {
		t.Fatalf("IssueImporterToken(bench_fix): %v", err)
	}
	if !tok.IsImporter() {
		t.Errorf("token should be IsImporter()")
	}
	// SourceAgent is NOT whitelisted; issuance rejected.
	_, err = p.IssueImporterToken(SourceAgent)
	if !errors.Is(err, ErrMissingKernelSeqTag) {
		t.Errorf("non-whitelisted importer: want ErrMissingKernelSeqTag, got %v", err)
	}
}

func TestTrustPolicy_StrictModeKernelSeqMustMatch(t *testing.T) {
	p := NewTrustPolicy()
	p.SetStrict(true)
	if !p.IsStrict() {
		t.Errorf("strict mode flag did not stick")
	}
	tok := p.IssueWriteToken(10)
	// At head=10 the token matches.
	if err := p.Verify(tok, 10); err != nil {
		t.Errorf("strict-mode match: want nil, got %v", err)
	}
	// At head=11 the token is stale and rejected.
	if err := p.Verify(tok, 11); !errors.Is(err, ErrMissingKernelSeqTag) {
		t.Errorf("strict-mode mismatch: want ErrMissingKernelSeqTag, got %v", err)
	}
}

func TestTrustPolicy_WhitelistDynamic(t *testing.T) {
	p := NewTrustPolicy()
	if p.IsWhitelisted(SourceAgent) {
		t.Errorf("SourceAgent should not be whitelisted by default")
	}
	p.Whitelist(SourceAgent)
	if !p.IsWhitelisted(SourceAgent) {
		t.Errorf("Whitelist did not add SourceAgent")
	}
	if _, err := p.IssueImporterToken(SourceAgent); err != nil {
		t.Errorf("post-whitelist issuance failed: %v", err)
	}
}
