package normalize

// ForLanguage routes sig through the appropriate per-language
// normalizer. Recognized language identifiers (case-insensitive) and
// their dispatch:
//
//	go                   → [Go]
//	typescript, ts,
//	javascript, js,
//	tsx, jsx             → [TS]   (JS shares the TS normalizer; the
//	                                 distinction is irrelevant at the
//	                                 signature-string level)
//	python, py           → [Python]
//
// Unrecognized languages fall through to a language-agnostic shape:
// whitespace canonicalization only. This is the conservative choice
// for cross-language fact ingestion — it never widens equivalence.
//
// Use ForLanguage from the three-source unifier and from selector
// anchor evaluators that need a uniform entry point regardless of the
// language attached to the entity. Callers that already know the
// language statically should call [Go] / [TS] / [Python] directly.
func ForLanguage(languageID, sig string) string {
	switch normalizeLanguageID(languageID) {
	case "go":
		return Go(sig)
	case "ts":
		return TS(sig)
	case "py":
		return Python(sig)
	default:
		return canonicalizeWhitespace(sig)
	}
}

func normalizeLanguageID(id string) string {
	// Avoid pulling in strings.ToLower for a tiny ASCII switch.
	low := make([]byte, len(id))
	for i := 0; i < len(id); i++ {
		c := id[i]
		if c >= 'A' && c <= 'Z' {
			c += 'a' - 'A'
		}
		low[i] = c
	}
	switch string(low) {
	case "go", "golang":
		return "go"
	case "typescript", "ts", "javascript", "js", "tsx", "jsx":
		return "ts"
	case "python", "py":
		return "py"
	}
	return ""
}
