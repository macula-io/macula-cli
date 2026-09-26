package wirevalue

import (
	"bytes"
	"errors"
	"testing"

	"github.com/macula-io/macula-go/cbor"
)

func TestJSONBecomesWireCBOR(t *testing.T) {
	v, err := FromJSON([]byte(`{"s": "x", "i": -7, "big": 9223372036854775807, "f": 1.5, "n": null,
		"l": [1, "a"], "m": {"k": 2}, "b": {"$bytes": "AQID"}}`))
	if err != nil {
		t.Fatal(err)
	}
	want := cbor.Map([]cbor.MapEntry{
		{Key: cbor.Text("s"), Val: cbor.Text("x")},
		{Key: cbor.Text("i"), Val: cbor.Int(-7)},
		{Key: cbor.Text("big"), Val: cbor.Int(9223372036854775807)},
		{Key: cbor.Text("f"), Val: cbor.Float(1.5)},
		{Key: cbor.Text("n"), Val: cbor.Null()},
		{Key: cbor.Text("l"), Val: cbor.List([]cbor.Value{cbor.Int(1), cbor.Text("a")})},
		{Key: cbor.Text("m"), Val: cbor.Map([]cbor.MapEntry{{Key: cbor.Text("k"), Val: cbor.Int(2)}})},
		{Key: cbor.Text("b"), Val: cbor.Bytes([]byte{1, 2, 3})},
	})
	if !bytes.Equal(cbor.Encode(v), cbor.Encode(want)) {
		t.Fatalf("got %v\nwant %v", v, want)
	}
}

func TestABooleanIsRefusedNamingTheRule(t *testing.T) {
	for _, text := range []string{`true`, `{"a": false}`, `[1, true]`} {
		if _, err := FromJSON([]byte(text)); !errors.Is(err, ErrBoolean) {
			t.Errorf("%s: %v, want ErrBoolean", text, err)
		}
	}
}

func TestAnIntegerOutsideInt64IsRefused(t *testing.T) {
	for _, text := range []string{`9223372036854775808`, `-9223372036854775809`} {
		if _, err := FromJSON([]byte(text)); !errors.Is(err, ErrIntegerRange) {
			t.Errorf("%s: %v, want ErrIntegerRange", text, err)
		}
	}
}

func TestMalformedInputIsRefused(t *testing.T) {
	for _, text := range []string{``, `{`, `{"$bytes": "!!"}`, `1 2`} {
		if _, err := FromJSON([]byte(text)); err == nil {
			t.Errorf("%q: accepted", text)
		}
	}
}

func TestAnObjectWithMoreThanBytesStaysAMap(t *testing.T) {
	v, err := FromJSON([]byte(`{"$bytes": "AQID", "x": 1}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := v.AsMap(); !ok {
		t.Fatalf("%v is not a map", v)
	}
}

func TestWireCBORBecomesJSONThatReadsBack(t *testing.T) {
	v := cbor.Map([]cbor.MapEntry{
		{Key: cbor.Text("b"), Val: cbor.Bytes([]byte{1, 2, 3})},
		{Key: cbor.Text("f"), Val: cbor.Float(2)},
		{Key: cbor.Text("u"), Val: cbor.Uint64(18446744073709551615)},
		{Key: cbor.Text("n"), Val: cbor.Null()},
		{Key: cbor.Text("l"), Val: cbor.List([]cbor.Value{cbor.Int(-1), cbor.Text("é")})},
	})
	text := ToJSON(v)
	want := `{"b":{"$bytes":"AQID"},"f":2.0,"u":18446744073709551615,"n":null,"l":[-1,"é"]}`
	if string(text) != want {
		t.Fatalf("got %s\nwant %s", text, want)
	}
	back, err := FromJSON([]byte(`{"b":{"$bytes":"AQID"},"f":2.0,"n":null,"l":[-1,"é"]}`))
	if err != nil {
		t.Fatal(err)
	}
	if f, _ := back.Get("f"); f.Kind() != cbor.KindFloat {
		t.Fatalf("2.0 read back as %v, want a float", f)
	}
}
