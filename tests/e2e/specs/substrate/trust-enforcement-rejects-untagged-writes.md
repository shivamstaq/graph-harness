# trust-enforcement-rejects-untagged-writes

> F14 / SPEC §9.1 writer-monopoly e2e: a direct write to the code.core SQLite from outside the daemon process is caught by `daemon audit-untagged`. The in-process Store seam (TrustPolicy + authorizeImplicitWrite) rejects synchronous attempts at write time; the audit CLI is the post-hoc safety net for any writer that bypasses the Store API entirely.
This is the F16 audit CLI's gate exercise — without it, the writer-monopoly invariant is only observable via Go-test-level unit tests, not from a workspace operator's view.


**Wave:** substrate
**Tags:** substrate, trust, audit, p1.5t07, f14, f16
**Spec:** `trust-enforcement-rejects-untagged-writes.yaml`

## Reference

_(link to design doc, RFC, ticket, or related code)_

## Test Setup

_(What fixture or helper builds the repo? What CLI commands run? What's asserted?)_

## Flow

```
_(ASCII diagram of actors, commands, state transitions)_
```

## Notes

_(Gotchas, related specs, follow-up work.)_
