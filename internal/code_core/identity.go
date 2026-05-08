// Package code_core implements the code.core layer: content-addressable
// canonical IDs (SPEC §6.12) plus the SQLite-backed adjacency store for
// `calls` and `references` relations.
package code_core

import (
	"crypto/sha256"
	"encoding/hex"
)

// EntityKind enumerates the kinds materialized into code.core. The set
// covers structural entities recovered by all three fact sources (LSP,
// SCIP, tree-sitter); per-language normalization of names/signatures
// lives in the [normalize] package.
type EntityKind string

// Entity kinds materialized by code.core.
//
// File / Function / Method / TypeDecl are the structural kinds every
// fact source can recover from any supported language. Class /
// Interface are first-class entities for languages that distinguish
// them from a generic TypeDecl (TypeScript interfaces, Python
// classes); their identity formula matches Class/Iface in SPEC §6.12.
// Symbol is the catch-all kind for fact-source emissions whose
// concrete kind does not appear above; per SPEC §6.12 it carries a
// `kind_tag` discriminator to keep canonical IDs unique across kind
// taxonomies.
const (
	KindFile      EntityKind = "File"
	KindFunction  EntityKind = "Function"
	KindMethod    EntityKind = "Method"
	KindTypeDecl  EntityKind = "TypeDecl"
	KindClass     EntityKind = "Class"
	KindInterface EntityKind = "Interface"
	KindSymbol    EntityKind = "Symbol"
)

// Entity is a materialized code.core entity with its content-addressable
// ID. Optional fields are zero-valued when not applicable: anonymous
// entities populate ParentID + KindTag + Ordinal in lieu of
// QualifiedName + Signature; non-anonymous entities populate the
// reverse. Provenance for the entity is recorded out-of-band via the
// `code_entity_provenance` table (SPEC §4.4 + §6.11) — it is not part
// of the entity's content-addressable identity.
type Entity struct {
	ID            string
	Kind          EntityKind
	LanguageID    string
	QualifiedName string
	Receiver      string // methods only
	Path          string // files only
	BodyHash      string // functions/methods only
	KindTag       string // §6.12 catch-all + anonymous-entity discriminator
	ParentID      string // anonymous entities only
	Ordinal       uint32 // anonymous entities only
}

// FileID = sha256(workspace_relative_path) per SPEC §6.12.
func FileID(workspaceRelPath string) string {
	return sha256Hex(workspaceRelPath)
}

// FunctionID = sha256(language_id || qualified_name || normalized_signature).
func FunctionID(languageID, qualifiedName, normalizedSig string) string {
	return sha256Hex(languageID + "\x00" + qualifiedName + "\x00" + normalizedSig)
}

// MethodID = sha256(language_id || receiver_qualified_name || method_name || normalized_signature).
func MethodID(languageID, receiverQN, methodName, normalizedSig string) string {
	return sha256Hex(languageID + "\x00" + receiverQN + "\x00" + methodName + "\x00" + normalizedSig)
}

// TypeDeclID = sha256(language_id || qualified_name). Used for any of
// TypeDecl, Class, Interface — SPEC §6.12 collapses them to one
// identity formula (the kind_tag attribute disambiguates downstream
// without participating in the hash).
func TypeDeclID(languageID, qualifiedName string) string {
	return sha256Hex(languageID + "\x00" + qualifiedName)
}

// SymbolID = sha256(language_id || qualified_name || kind_tag) per
// SPEC §6.12. Used for the `Symbol` catch-all kind when a fact source
// emits an entity whose concrete kind is not enumerated by code.core
// (e.g. Python module-level constants surfacing through SCIP, TS
// `enum` members). The kind_tag provides discrimination across the
// open-ended kind taxonomy without expanding the identity-formula
// surface for every new fact-source variant.
func SymbolID(languageID, qualifiedName, kindTag string) string {
	return sha256Hex(languageID + "\x00" + qualifiedName + "\x00" + kindTag)
}

// AnonymousID = sha256(parent_id || kind_tag || ordinal_within_parent)
// per SPEC §6.12. Closures, lambdas, anonymous structs, and
// generated entities use this formula because they have no stable
// qualified name. Stability under edit relies on the parent's
// canonical ID staying stable and the ordinal (0-based position
// among same-kind anonymous siblings) not shifting; reorder-the-
// closures edits intentionally produce new IDs (renames-as-new-ID,
// SPEC §6.12).
func AnonymousID(parentID, kindTag string, ordinal uint32) string {
	return sha256Hex(parentID + "\x00" + kindTag + "\x00" + uint32Decimal(ordinal))
}

func sha256Hex(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

func uint32Decimal(n uint32) string {
	if n == 0 {
		return "0"
	}
	var buf [10]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
