package proto

import (
	"errors"
	"fmt"

	"google.golang.org/protobuf/encoding/protowire"
)

// DecodeIndex parses a SCIP Index message from buf. The caller has
// already read the file into memory; we don't expose a streaming
// decoder yet — SCIP files are typically small enough that buffered
// decode is fine, and the per-file Document subdivision keeps memory
// bounded for large indexes.
//
// Unrecognised fields are skipped per the protobuf compatibility
// contract; new fields added in upstream scip.proto won't break
// existing indexes.
func DecodeIndex(buf []byte) (*Index, error) {
	idx := &Index{}
	for len(buf) > 0 {
		num, typ, n := protowire.ConsumeTag(buf)
		if err := protowire.ParseError(n); err != nil {
			return nil, fmt.Errorf("index tag: %w", err)
		}
		buf = buf[n:]
		switch num {
		case 1: // metadata
			body, m, err := consumeBytes(buf, typ)
			if err != nil {
				return nil, fmt.Errorf("metadata: %w", err)
			}
			meta, err := decodeMetadata(body)
			if err != nil {
				return nil, err
			}
			idx.Metadata = meta
			buf = buf[m:]
		case 2: // documents
			body, m, err := consumeBytes(buf, typ)
			if err != nil {
				return nil, fmt.Errorf("document: %w", err)
			}
			doc, err := DecodeDocument(body)
			if err != nil {
				return nil, err
			}
			idx.Documents = append(idx.Documents, doc)
			buf = buf[m:]
		case 3: // external_symbols
			body, m, err := consumeBytes(buf, typ)
			if err != nil {
				return nil, fmt.Errorf("external_symbol: %w", err)
			}
			sym, err := decodeSymbolInformation(body)
			if err != nil {
				return nil, err
			}
			idx.ExternalSymbols = append(idx.ExternalSymbols, sym)
			buf = buf[m:]
		default:
			m := protowire.ConsumeFieldValue(num, typ, buf)
			if err := protowire.ParseError(m); err != nil {
				return nil, fmt.Errorf("skip field %d: %w", num, err)
			}
			buf = buf[m:]
		}
	}
	return idx, nil
}

// DecodeDocument parses a single Document submessage. Exposed so
// callers that hand-frame an Index for streaming purposes (e.g. tests)
// can reuse the routine.
func DecodeDocument(buf []byte) (*Document, error) {
	doc := &Document{}
	for len(buf) > 0 {
		num, typ, n := protowire.ConsumeTag(buf)
		if err := protowire.ParseError(n); err != nil {
			return nil, fmt.Errorf("doc tag: %w", err)
		}
		buf = buf[n:]
		switch num {
		case 1: // relative_path
			s, m, err := consumeString(buf, typ)
			if err != nil {
				return nil, fmt.Errorf("relative_path: %w", err)
			}
			doc.RelativePath = s
			buf = buf[m:]
		case 2: // occurrences
			body, m, err := consumeBytes(buf, typ)
			if err != nil {
				return nil, fmt.Errorf("occurrence: %w", err)
			}
			occ, err := decodeOccurrence(body)
			if err != nil {
				return nil, err
			}
			doc.Occurrences = append(doc.Occurrences, occ)
			buf = buf[m:]
		case 3: // symbols
			body, m, err := consumeBytes(buf, typ)
			if err != nil {
				return nil, fmt.Errorf("symbols: %w", err)
			}
			sym, err := decodeSymbolInformation(body)
			if err != nil {
				return nil, err
			}
			doc.Symbols = append(doc.Symbols, sym)
			buf = buf[m:]
		case 4: // language
			s, m, err := consumeString(buf, typ)
			if err != nil {
				return nil, fmt.Errorf("language: %w", err)
			}
			doc.Language = s
			buf = buf[m:]
		default:
			m := protowire.ConsumeFieldValue(num, typ, buf)
			if err := protowire.ParseError(m); err != nil {
				return nil, fmt.Errorf("skip doc field %d: %w", num, err)
			}
			buf = buf[m:]
		}
	}
	return doc, nil
}

func decodeMetadata(buf []byte) (*Metadata, error) {
	m := &Metadata{}
	for len(buf) > 0 {
		num, typ, n := protowire.ConsumeTag(buf)
		if err := protowire.ParseError(n); err != nil {
			return nil, err
		}
		buf = buf[n:]
		switch num {
		case 3: // project_root
			s, k, err := consumeString(buf, typ)
			if err != nil {
				return nil, err
			}
			m.ProjectRoot = s
			buf = buf[k:]
		default:
			k := protowire.ConsumeFieldValue(num, typ, buf)
			if err := protowire.ParseError(k); err != nil {
				return nil, err
			}
			buf = buf[k:]
		}
	}
	return m, nil
}

func decodeSymbolInformation(buf []byte) (*SymbolInformation, error) {
	s := &SymbolInformation{}
	for len(buf) > 0 {
		num, typ, n := protowire.ConsumeTag(buf)
		if err := protowire.ParseError(n); err != nil {
			return nil, err
		}
		buf = buf[n:]
		switch num {
		case 1: // symbol
			str, m, err := consumeString(buf, typ)
			if err != nil {
				return nil, err
			}
			s.Symbol = str
			buf = buf[m:]
		case 5: // kind
			v, m, err := consumeVarint(buf, typ)
			if err != nil {
				return nil, err
			}
			s.Kind = Kind(int32(v)) //nolint:gosec // protowire produces in-range values
			buf = buf[m:]
		case 6: // display_name
			str, m, err := consumeString(buf, typ)
			if err != nil {
				return nil, err
			}
			s.DisplayName = str
			buf = buf[m:]
		default:
			m := protowire.ConsumeFieldValue(num, typ, buf)
			if err := protowire.ParseError(m); err != nil {
				return nil, err
			}
			buf = buf[m:]
		}
	}
	return s, nil
}

func decodeOccurrence(buf []byte) (*Occurrence, error) {
	o := &Occurrence{}
	for len(buf) > 0 {
		num, typ, n := protowire.ConsumeTag(buf)
		if err := protowire.ParseError(n); err != nil {
			return nil, err
		}
		buf = buf[n:]
		switch num {
		case 1: // range — packed repeated int32
			body, m, err := consumeBytes(buf, typ)
			if err != nil {
				// Could be a non-packed encoding; fall back.
				if errors.Is(err, errWireType) && typ == protowire.VarintType {
					v, m2, err2 := consumeVarint(buf, typ)
					if err2 != nil {
						return nil, err2
					}
					o.Range = append(o.Range, int32(v)) //nolint:gosec
					buf = buf[m2:]
					continue
				}
				return nil, fmt.Errorf("range: %w", err)
			}
			ints, err := decodePackedInt32(body)
			if err != nil {
				return nil, err
			}
			o.Range = append(o.Range, ints...)
			buf = buf[m:]
		case 2: // symbol
			str, m, err := consumeString(buf, typ)
			if err != nil {
				return nil, err
			}
			o.Symbol = str
			buf = buf[m:]
		case 3: // symbol_roles
			v, m, err := consumeVarint(buf, typ)
			if err != nil {
				return nil, err
			}
			o.SymbolRoles = int32(v) //nolint:gosec
			buf = buf[m:]
		default:
			m := protowire.ConsumeFieldValue(num, typ, buf)
			if err := protowire.ParseError(m); err != nil {
				return nil, err
			}
			buf = buf[m:]
		}
	}
	return o, nil
}

func decodePackedInt32(buf []byte) ([]int32, error) {
	out := make([]int32, 0, len(buf)/2)
	for len(buf) > 0 {
		v, n := protowire.ConsumeVarint(buf)
		if err := protowire.ParseError(n); err != nil {
			return nil, fmt.Errorf("packed int32: %w", err)
		}
		out = append(out, int32(v)) //nolint:gosec
		buf = buf[n:]
	}
	return out, nil
}

var errWireType = errors.New("unexpected wire type")

func consumeBytes(buf []byte, typ protowire.Type) ([]byte, int, error) {
	if typ != protowire.BytesType {
		return nil, 0, errWireType
	}
	v, n := protowire.ConsumeBytes(buf)
	if err := protowire.ParseError(n); err != nil {
		return nil, 0, err
	}
	return v, n, nil
}

func consumeString(buf []byte, typ protowire.Type) (string, int, error) {
	if typ != protowire.BytesType {
		return "", 0, errWireType
	}
	v, n := protowire.ConsumeString(buf)
	if err := protowire.ParseError(n); err != nil {
		return "", 0, err
	}
	return v, n, nil
}

func consumeVarint(buf []byte, typ protowire.Type) (uint64, int, error) {
	if typ != protowire.VarintType {
		return 0, 0, errWireType
	}
	v, n := protowire.ConsumeVarint(buf)
	if err := protowire.ParseError(n); err != nil {
		return 0, 0, err
	}
	return v, n, nil
}
