// Package wirevalue maps payloads between JSON, as a user writes them on the
// command line and reads them back, and macula's wire CBOR.
//
// JSON in: a string is text, an integer (no fraction or exponent) an integer
// exact over int64, any other number a float, null is null, and an object
// whose only key is "$bytes", holding standard padded base64, is a byte
// string. A boolean is refused: macula's CBOR has none, so send 0 or 1.
//
// JSON out: bytes as the same {"$bytes": ...} object and floats always with a
// fraction or exponent, so a payload received reads back as the same value
// and can be sent back unchanged (a payload's integers are within int64: the
// wire refuses any other). Map keys come out sorted by their encoded bytes,
// the wire's own order, so the output of one value is always the same text.
// Two cases do not round-trip, and macula's payloads have neither: a map whose
// only key is a text "$bytes" reads back as bytes, and a key that is not text
// is written as its diagnostic string, which can repeat a text key.
package wirevalue

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"sort"
	"strconv"
	"strings"

	"github.com/macula-io/macula-go/cbor"
)

var (
	// ErrBoolean is a JSON boolean: macula's wire has no boolean.
	ErrBoolean = errors.New("no boolean on the macula wire: send 0 or 1")
	// ErrIntegerRange is an integer outside int64, which the wire refuses.
	ErrIntegerRange = errors.New("an integer outside int64, the integers the macula wire carries")
)

const bytesKey = "$bytes"

// FromJSON is one JSON value as wire CBOR.
func FromJSON(text []byte) (cbor.Value, error) {
	decoder := json.NewDecoder(bytes.NewReader(text))
	decoder.UseNumber()
	var v any
	if err := decoder.Decode(&v); err != nil {
		return cbor.Value{}, fmt.Errorf("not one JSON value: %w", err)
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return cbor.Value{}, errors.New("not one JSON value: more follows it")
	}
	return fromAny(v)
}

func fromAny(v any) (cbor.Value, error) {
	switch t := v.(type) {
	case nil:
		return cbor.Null(), nil
	case bool:
		return cbor.Value{}, ErrBoolean
	case string:
		return cbor.Text(t), nil
	case json.Number:
		return fromNumber(t)
	case []any:
		items := make([]cbor.Value, len(t))
		for i, item := range t {
			value, err := fromAny(item)
			if err != nil {
				return cbor.Value{}, err
			}
			items[i] = value
		}
		return cbor.List(items), nil
	case map[string]any:
		if raw, only := t[bytesKey]; only && len(t) == 1 {
			text, ok := raw.(string)
			if !ok {
				return cbor.Value{}, errors.New(`"$bytes" holds base64 text`)
			}
			b, err := base64.StdEncoding.DecodeString(text)
			if err != nil {
				return cbor.Value{}, fmt.Errorf(`"$bytes" is not standard padded base64: %w`, err)
			}
			return cbor.Bytes(b), nil
		}
		entries := make([]cbor.MapEntry, 0, len(t))
		for k, item := range t {
			value, err := fromAny(item)
			if err != nil {
				return cbor.Value{}, err
			}
			entries = append(entries, cbor.MapEntry{Key: cbor.Text(k), Val: value})
		}
		return cbor.Map(entries), nil
	}
	return cbor.Value{}, fmt.Errorf("a JSON value of type %T", v)
}

func fromNumber(n json.Number) (cbor.Value, error) {
	text := n.String()
	if !strings.ContainsAny(text, ".eE") {
		i, err := strconv.ParseInt(text, 10, 64)
		if err != nil {
			return cbor.Value{}, ErrIntegerRange
		}
		return cbor.Int(i), nil
	}
	f, err := strconv.ParseFloat(text, 64)
	if err != nil || math.IsInf(f, 0) {
		return cbor.Value{}, fmt.Errorf("the number %s is not a finite float", text)
	}
	return cbor.Float(f), nil
}

// ToJSON is a wire value as JSON.
func ToJSON(v cbor.Value) []byte {
	var buf bytes.Buffer
	write(&buf, v)
	return buf.Bytes()
}

func write(buf *bytes.Buffer, v cbor.Value) {
	switch v.Kind() {
	case cbor.KindNull:
		buf.WriteString("null")
	case cbor.KindUInt, cbor.KindNegInt:
		if i, ok := v.AsInt64(); ok {
			buf.WriteString(strconv.FormatInt(i, 10))
			return
		}
		// A uint64 beyond int64 (a station's own counters may be): its digits.
		buf.WriteString(v.String())
	case cbor.KindFloat:
		f, _ := v.AsFloat()
		text := strconv.FormatFloat(f, 'g', -1, 64)
		if !strings.ContainsAny(text, ".eEn") {
			text += ".0"
		}
		buf.WriteString(text)
	case cbor.KindText:
		s, _ := v.AsText()
		quoted, _ := json.Marshal(s)
		buf.Write(quoted)
	case cbor.KindBytes:
		b, _ := v.AsBytes()
		buf.WriteString(`{"$bytes":"` + base64.StdEncoding.EncodeToString(b) + `"}`)
	case cbor.KindList:
		items, _ := v.AsList()
		buf.WriteByte('[')
		for i, item := range items {
			if i > 0 {
				buf.WriteByte(',')
			}
			write(buf, item)
		}
		buf.WriteByte(']')
	case cbor.KindMap:
		entries, _ := v.AsMap()
		entries = append([]cbor.MapEntry(nil), entries...)
		sort.Slice(entries, func(i, j int) bool {
			return bytes.Compare(cbor.Encode(entries[i].Key), cbor.Encode(entries[j].Key)) < 0
		})
		buf.WriteByte('{')
		for i, e := range entries {
			if i > 0 {
				buf.WriteByte(',')
			}
			key, ok := e.Key.AsText()
			if !ok {
				key = e.Key.String()
			}
			quoted, _ := json.Marshal(key)
			buf.Write(quoted)
			buf.WriteByte(':')
			write(buf, e.Val)
		}
		buf.WriteByte('}')
	default:
		quoted, _ := json.Marshal(v.String())
		buf.Write(quoted)
	}
}
