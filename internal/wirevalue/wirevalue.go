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
		return fromList(t)
	case map[string]any:
		return fromMap(t)
	}
	return cbor.Value{}, fmt.Errorf("a JSON value of type %T", v)
}

func fromList(items []any) (cbor.Value, error) {
	values := make([]cbor.Value, len(items))
	for i, item := range items {
		value, err := fromAny(item)
		if err != nil {
			return cbor.Value{}, err
		}
		values[i] = value
	}
	return cbor.List(values), nil
}

func fromMap(m map[string]any) (cbor.Value, error) {
	if raw, only := m[bytesKey]; only && len(m) == 1 {
		return fromBytesMarked(raw)
	}
	entries := make([]cbor.MapEntry, 0, len(m))
	for k, item := range m {
		value, err := fromAny(item)
		if err != nil {
			return cbor.Value{}, err
		}
		entries = append(entries, cbor.MapEntry{Key: cbor.Text(k), Val: value})
	}
	return cbor.Map(entries), nil
}

// fromBytesMarked reads the one-key form {"$bytes": "<base64>"}.
func fromBytesMarked(raw any) (cbor.Value, error) {
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
		writeInt(buf, v)
	case cbor.KindFloat:
		writeFloat(buf, v)
	case cbor.KindText:
		writeText(buf, v)
	case cbor.KindBytes:
		writeBytes(buf, v)
	case cbor.KindList:
		writeList(buf, v)
	case cbor.KindMap:
		writeMap(buf, v)
	default:
		quoted, _ := json.Marshal(v.String())
		buf.Write(quoted)
	}
}

func writeInt(buf *bytes.Buffer, v cbor.Value) {
	if i, ok := v.AsInt64(); ok {
		buf.WriteString(strconv.FormatInt(i, 10))
		return
	}
	// A uint64 beyond int64, which a payload cannot hold (the decoder refuses
	// one) but a value built in Go can: its digits.
	buf.WriteString(v.String())
}

func writeFloat(buf *bytes.Buffer, v cbor.Value) {
	f, _ := v.AsFloat()
	text := strconv.FormatFloat(f, 'g', -1, 64)
	if !strings.ContainsAny(text, ".eEn") {
		text += ".0"
	}
	buf.WriteString(text)
}

func writeText(buf *bytes.Buffer, v cbor.Value) {
	s, _ := v.AsText()
	quoted, _ := json.Marshal(s)
	buf.Write(quoted)
}

func writeBytes(buf *bytes.Buffer, v cbor.Value) {
	b, _ := v.AsBytes()
	buf.WriteString(`{"$bytes":"` + base64.StdEncoding.EncodeToString(b) + `"}`)
}

func writeList(buf *bytes.Buffer, v cbor.Value) {
	items, _ := v.AsList()
	buf.WriteByte('[')
	for i, item := range items {
		if i > 0 {
			buf.WriteByte(',')
		}
		write(buf, item)
	}
	buf.WriteByte(']')
}

// writeMap sorts entries by canonical key bytes, so the same map always writes
// the same JSON.
func writeMap(buf *bytes.Buffer, v cbor.Value) {
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
		writeMapKey(buf, e.Key)
		buf.WriteByte(':')
		write(buf, e.Val)
	}
	buf.WriteByte('}')
}

func writeMapKey(buf *bytes.Buffer, key cbor.Value) {
	text, ok := key.AsText()
	if !ok {
		text = key.String()
	}
	quoted, _ := json.Marshal(text)
	buf.Write(quoted)
}
