# watcher-reindexes-on-file-change

> SPEC §6.20 + P0.5.T07/T08: when the daemon is running, modifying a source file in a way that changes the extracted entity set triggers the watcher → orchestrator path. fsnotify events within the 50 ms coalesce window collapse to one re-extract; the new entity becomes queryable via `code list`.
The substrate invariant ISN'T "any file write → kernel event": the compare-before-emit contract (P0.5.T09) correctly suppresses events for whitespace/comment-only edits because no entity changed. The spec must add a real entity (new function declaration) to provoke a state transition the watcher will pump onto the bus.


**Wave:** daemon
**Tags:** daemon, watcher, fsnotify, p0.5t07, p0.5t08
**Spec:** `watcher-reindexes-on-file-change.yaml`

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
