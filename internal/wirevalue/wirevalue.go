// Package wirevalue bridges JSON (what a human or an agent types on the
// command line, or wants printed back) and cbor.Value (what
// macula-go's frames actually carry). Macula's wire model has no
// bool and no distinct "float vs int" ambiguity the way JSON does — see
// macula-go's cbor package doc — so this package is deliberately
// narrow rather than a generic converter.
package wirevalue

import (
	"encoding/hex"
	"encoding/json"
	"fmt"

	"github.com/macula-io/macula-go/cbor"
)

// FromJSON parses a JSON document (typically the --args flag) into a
// cbor.Value suitable for a CALL/STREAM_OPEN payload. JSON booleans are
// rejected explicitly rather than silently coerced: Macula's wire
// protocol has no bool type at all, so 'true'/'false' has no faithful
// representation — the caller must pick 0/1 or restructure the payload.
func FromJSON(data []byte) (cbor.Value, error) {
	var v any
	if err := json.Unmarshal(data, &v); err != nil {
		return cbor.Value{}, fmt.Errorf("wirevalue: invalid JSON: %w", err)
	}
	return fromAny(v)
}

func fromAny(v any) (cbor.Value, error) {
	switch t := v.(type) {
	case nil:
		return cbor.Null(), nil
	case bool:
		return cbor.Value{}, fmt.Errorf("wirevalue: JSON boolean %v has no wire representation (macula's CBOR has no bool type) — use 0/1 instead", t)
	case string:
		return cbor.Text(t), nil
	case float64:
		if t == float64(int64(t)) {
			return cbor.Int(int64(t)), nil
		}
		return cbor.Float(t), nil
	case []any:
		vals := make([]cbor.Value, len(t))
		for i, item := range t {
			cv, err := fromAny(item)
			if err != nil {
				return cbor.Value{}, err
			}
			vals[i] = cv
		}
		return cbor.List(vals), nil
	case map[string]any:
		entries := make([]cbor.MapEntry, 0, len(t))
		for k, item := range t {
			cv, err := fromAny(item)
			if err != nil {
				return cbor.Value{}, err
			}
			entries = append(entries, cbor.MapEntry{Key: cbor.Text(k), Val: cv})
		}
		return cbor.Map(entries), nil
	default:
		return cbor.Value{}, fmt.Errorf("wirevalue: unsupported JSON value of type %T", v)
	}
}

// ToJSON converts a cbor.Value into a plain Go value that
// encoding/json can marshal directly. Bytes have no native JSON
// representation, so they're rendered as a "0x"-prefixed hex string —
// unambiguous against a real Text value, which never starts that way
// after JSON-escaping (a real "0x..." Text value round-trips as
// itself; ToJSON does not attempt to distinguish the two, since a hex
// string in this tool's output is always meant to be read as bytes).
func ToJSON(v cbor.Value) any {
	if b, ok := v.AsBytes(); ok {
		return "0x" + hex.EncodeToString(b)
	}
	if s, ok := v.AsText(); ok {
		return s
	}
	if i, ok := v.AsInt64(); ok {
		return i
	}
	if f, ok := v.AsFloat(); ok {
		return f
	}
	if v.IsNull() {
		return nil
	}
	if list, ok := v.AsList(); ok {
		out := make([]any, len(list))
		for i, item := range list {
			out[i] = ToJSON(item)
		}
		return out
	}
	if entries, ok := v.AsMap(); ok {
		out := make(map[string]any, len(entries))
		for _, e := range entries {
			key := e.Key.String()
			if s, ok := e.Key.AsText(); ok {
				key = s
			}
			out[key] = ToJSON(e.Val)
		}
		return out
	}
	return v.String()
}
