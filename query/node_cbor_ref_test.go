// The reference CBOR adapter : the first implementation, over fxamacker/cbor's decode into `any`. It is kept
// as the oracle the production decoder (node_cbor.go) is compared against, and nothing else uses it.
package query

import (
	"encoding/json"
	"fmt"
	"math"
	"math/big"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/fxamacker/cbor/v2"
	"github.com/samber/oops"
)

// The CBOR adapter (specs/cbor.md ## CBOR adapter) : a Node over a decoded CBOR document. The whole
// document is decoded at once, with the limits and rules below, and the node tree is read from the result.

var refDecMode = func() cbor.DecMode {
	dm, err := cbor.DecOptions{
		DefaultMapType:  reflect.TypeOf(map[string]any{}), // a map key that is not text is an error
		TagsMd:          cbor.TagsAllowed,
		TimeTag:         cbor.DecTagOptional, // tags 0 and 1 become a time.Time
		IntDec:          cbor.IntDecConvertSignedOrBigInt,
		DupMapKey:       cbor.DupMapKeyEnforcedAPF,
		MaxNestedLevels: 64,
	}.DecMode()
	if err != nil {
		panic(err)
	}
	return dm
}()

// newRefCBORNode decodes one CBOR data item, which must be all of data.
func newRefCBORNode(data []byte) (Node, error) {
	var v any
	if err := refDecMode.Unmarshal(data, &v); err != nil {
		return nil, oops.Wrapf(err, "query: invalid CBOR")
	}
	return refCBORNode{v}, nil
}

// refCBORNode wraps one decoded value : nil, bool, int64, uint64, big.Int, float64, string, []byte, []any,
// map[string]any, time.Time (tags 0 and 1) or cbor.Tag (any other tag).
type refCBORNode struct{ v any }

func (c refCBORNode) Kind() NodeKind {
	switch v := c.v.(type) {
	case nil:
		return KindNull
	case bool:
		return KindBool
	case int64, uint64, big.Int, *big.Int, float64:
		return KindNumber
	case string:
		return KindString
	case []byte:
		return KindBytes
	case []any:
		return KindArray
	case map[string]any:
		return KindObject
	case time.Time:
		return KindInstant
	case cbor.Tag:
		switch v.Number {
		case 4:
			if _, ok := refDecimalFraction(v); ok {
				return KindNumber
			}
		case 37:
			if b, ok := v.Content.([]byte); ok && len(b) == 16 {
				return KindUUID
			}
		case 100:
			if _, ok := refIntegerOf(v.Content); ok {
				return KindDate
			}
		}
	}
	return KindInvalid
}

// unsupported describes a value no Node kind covers, for an error message.
func (c refCBORNode) unsupported() string {
	if t, ok := c.v.(cbor.Tag); ok {
		return fmt.Sprintf("CBOR tag %d with this content", t.Number)
	}
	return fmt.Sprintf("CBOR value of type %T", c.v)
}

// integerOf returns v as an int64 when it is an integer that fits.
func refIntegerOf(v any) (int64, bool) {
	switch x := v.(type) {
	case int64:
		return x, true
	case uint64:
		if x <= math.MaxInt64 {
			return int64(x), true
		}
	case big.Int:
		if x.IsInt64() {
			return x.Int64(), true
		}
	case *big.Int:
		if x != nil && x.IsInt64() {
			return x.Int64(), true
		}
	}
	return 0, false
}

func refIntegerText(v any) (string, bool) {
	switch x := v.(type) {
	case int64:
		return strconv.FormatInt(x, 10), true
	case uint64:
		return strconv.FormatUint(x, 10), true
	case big.Int:
		return x.String(), true
	case *big.Int:
		if x != nil {
			return x.String(), true
		}
	}
	return "", false
}

// decimalFraction reads tag 4, [exponent, mantissa], as the text "<mantissa>e<exponent>".
func refDecimalFraction(t cbor.Tag) (string, bool) {
	pair, ok := t.Content.([]any)
	if !ok || len(pair) != 2 {
		return "", false
	}
	exp, ok := refIntegerOf(pair[0])
	if !ok {
		return "", false
	}
	mant, ok := refIntegerText(pair[1])
	if !ok {
		return "", false
	}
	return mant + "e" + strconv.FormatInt(exp, 10), true
}

// floatText prints f so it never reads as an integer : 3 is "3.0".
func refFloatText(f float64) string {
	switch {
	case math.IsNaN(f):
		return "NaN"
	case math.IsInf(f, 1):
		return "Infinity"
	case math.IsInf(f, -1):
		return "-Infinity"
	}
	s := strconv.FormatFloat(f, 'g', -1, 64)
	if !strings.ContainsAny(s, ".e") {
		s += ".0"
	}
	return s
}

// numberTextOf is the text of a number node.
func (c refCBORNode) numberText() (string, bool) {
	switch x := c.v.(type) {
	case float64:
		return refFloatText(x), true
	case cbor.Tag:
		return refDecimalFraction(x)
	}
	return refIntegerText(c.v)
}

// decimalText is a number node as plain decimal digits, without an exponent where the exponent is
// small, which is what a type that reads numbers from text (money, a range bound) expects.
func (c refCBORNode) decimalText() (string, bool) {
	switch x := c.v.(type) {
	case float64:
		if math.IsNaN(x) || math.IsInf(x, 0) {
			return refFloatText(x), true
		}
		return strconv.FormatFloat(x, 'f', -1, 64), true
	case cbor.Tag:
		pair, ok := x.Content.([]any)
		if !ok || len(pair) != 2 {
			return "", false
		}
		exp, ok := refIntegerOf(pair[0])
		mant, ok2 := refIntegerText(pair[1])
		if !ok || !ok2 {
			return "", false
		}
		return refPlainDecimal(mant, exp), true
	}
	return refIntegerText(c.v)
}

// plainDecimal writes mantissa * 10^exp without an exponent when |exp| is at most 64.
func refPlainDecimal(mantissa string, exp int64) string {
	if exp == 0 || exp > 64 || exp < -64 {
		if exp == 0 {
			return mantissa
		}
		return mantissa + "e" + strconv.FormatInt(exp, 10)
	}
	neg := strings.HasPrefix(mantissa, "-")
	digits := strings.TrimPrefix(mantissa, "-")
	var out string
	if exp > 0 {
		out = digits + strings.Repeat("0", int(exp))
	} else {
		point := len(digits) + int(exp)
		if point > 0 {
			out = digits[:point] + "." + digits[point:]
		} else {
			out = "0." + strings.Repeat("0", -point) + digits
		}
	}
	if neg {
		return "-" + out
	}
	return out
}

func (c refCBORNode) StrictString() (string, error) {
	if s, ok := c.v.(string); ok {
		return s, nil
	}
	return "", errNotString
}

func (c refCBORNode) StrictBool() (bool, error) {
	if b, ok := c.v.(bool); ok {
		return b, nil
	}
	return false, nodeError("value is not a boolean")
}

func (c refCBORNode) StrictInt64() (int64, error) {
	if i, ok := refIntegerOf(c.v); ok {
		return i, nil
	}
	return 0, nodeError("value is not an integer that fits 64 bits")
}

func (c refCBORNode) StrictFloat64() (float64, error) {
	if f, ok := c.v.(float64); ok {
		return f, nil
	}
	if c.Kind() != KindNumber {
		return 0, nodeError("value is not a number")
	}
	text, _ := c.numberText()
	return strconv.ParseFloat(text, 64)
}

func (c refCBORNode) ArrayItems() ([]Node, error) {
	a, ok := c.v.([]any)
	if !ok {
		return nil, errNotArray
	}
	out := make([]Node, len(a))
	for i, v := range a {
		out[i] = refCBORNode{v}
	}
	return out, nil
}

func (c refCBORNode) ObjectGet(key string) (Node, bool) {
	m, ok := c.v.(map[string]any)
	if !ok {
		return nil, false
	}
	v, ok := m[key]
	if !ok {
		return nil, false
	}
	return refCBORNode{v}, true
}

func (c refCBORNode) ObjectEach(fn func(string, Node) error) error {
	m, ok := c.v.(map[string]any)
	if !ok {
		return errNotObject
	}
	for k, v := range m {
		if err := fn(k, refCBORNode{v}); err != nil {
			return err
		}
	}
	return nil
}

func (c refCBORNode) StrictBytes() ([]byte, error) {
	switch x := c.v.(type) {
	case []byte:
		return x, nil
	case cbor.Tag:
		if b, ok := x.Content.([]byte); ok && x.Number == 37 {
			return b, nil
		}
	}
	return nil, nodeError("value is not a byte string")
}

func (c refCBORNode) Instant() (time.Time, error) {
	if t, ok := c.v.(time.Time); ok {
		return t, nil
	}
	return time.Time{}, nodeError("value is not a date and time")
}

func (c refCBORNode) Days() (int64, error) {
	if t, ok := c.v.(cbor.Tag); ok && t.Number == 100 {
		if d, ok := refIntegerOf(t.Content); ok {
			return d, nil
		}
	}
	return 0, nodeError("value is not an epoch-based date")
}

// Raw returns the node as JSON text, which is what a json or jsonb column receives : a number
// keeps its digits (a non-finite float reads as NaN, Infinity or -Infinity, which the number
// encoders accept and jsonText refuses).
func (c refCBORNode) Raw() (string, error) {
	if c.Kind() == KindNumber {
		text, _ := c.numberText()
		return text, nil
	}
	return c.jsonText()
}

// jsonText converts the node to JSON : maps with text keys become objects (keys sorted), arrays arrays,
// text, integers, booleans and null map directly, finite floats and tag 4 become numbers. A byte
// string, a date or time, any other tag, and a non-finite float have no JSON form.
func (c refCBORNode) jsonText() (string, error) {
	var b strings.Builder
	if err := refWriteCBORJSON(&b, c.v); err != nil {
		return "", err
	}
	return b.String(), nil
}

func refNoJSONForm(what string) error {
	return errBadValue("json", "", what+" cannot be written as JSON")
}

func refWriteCBORJSON(b *strings.Builder, v any) error {
	switch x := v.(type) {
	case nil:
		b.WriteString("null")
	case bool:
		b.WriteString(strconv.FormatBool(x))
	case string:
		enc, err := json.Marshal(x)
		if err != nil {
			return refNoJSONForm("a string that is not valid UTF-8")
		}
		b.Write(enc)
	case float64:
		if math.IsNaN(x) || math.IsInf(x, 0) {
			return refNoJSONForm("a non-finite number")
		}
		b.WriteString(strconv.FormatFloat(x, 'g', -1, 64))
	case []any:
		b.WriteByte('[')
		for i, e := range x {
			if i > 0 {
				b.WriteByte(',')
			}
			if err := refWriteCBORJSON(b, e); err != nil {
				return err
			}
		}
		b.WriteByte(']')
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		b.WriteByte('{')
		for i, k := range keys {
			if i > 0 {
				b.WriteByte(',')
			}
			enc, err := json.Marshal(k)
			if err != nil {
				return refNoJSONForm("a key that is not valid UTF-8")
			}
			b.Write(enc)
			b.WriteByte(':')
			if err := refWriteCBORJSON(b, x[k]); err != nil {
				return err
			}
		}
		b.WriteByte('}')
	case []byte:
		return refNoJSONForm("a byte string")
	case time.Time:
		return refNoJSONForm("a date and time")
	case cbor.Tag:
		if text, ok := refDecimalFraction(x); ok && x.Number == 4 {
			b.WriteString(text)
			return nil
		}
		return refNoJSONForm(fmt.Sprintf("a value with CBOR tag %d", x.Number))
	default:
		if text, ok := refIntegerText(v); ok {
			b.WriteString(text)
			return nil
		}
		return refNoJSONForm(fmt.Sprintf("a value of type %T", v))
	}
	return nil
}
