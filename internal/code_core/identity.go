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

// Entity kinds materialized by code.core. The catch-all `Symbol` kind
// (SPEC §6.12) joins this set when the three-source unifier lands.
const (
	KindFile     EntityKind = "File"
	KindFunction EntityKind = "Function"
	KindMethod   EntityKind = "Method"
	KindTypeDecl EntityKind = "TypeDecl"
)

// Entity is a materialized code.core entity with its content-addressable ID.
type Entity struct {
	ID            string
	Kind          EntityKind
	LanguageID    string
	QualifiedName string
	Receiver      string // methods only
	Path          string // files only
	BodyHash      string // functions only
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

// TypeDeclID = sha256(language_id || qualified_name).
func TypeDeclID(languageID, qualifiedName string) string {
	return sha256Hex(languageID + "\x00" + qualifiedName)
}

func sha256Hex(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}
