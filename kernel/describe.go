package kernel

import (
	"fmt"
	"reflect"
	"strconv"
)

// Describer lets payloads describe themselves deterministically in trace records.
type Describer interface{ Describe() string }

// Describe returns a deterministic description of v (KRN-100). It never uses %v and never prints
// pointers, floats, maps or struct contents.
func Describe(v any) string {
	if v == nil {
		return "nil"
	}
	switch x := v.(type) {
	case Describer:
		return x.Describe()
	case fmt.Stringer:
		return x.String()
	case error:
		return x.Error()
	}
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.Slice:
		if rv.Type().Elem().Kind() == reflect.Uint8 {
			b := rv.Bytes()
			return "bytes len=" + strconv.Itoa(len(b)) + " fnv=" + hex16(fnvFold(fnvOffset64, b))
		}
	case reflect.String:
		s := rv.String()
		if len(s) <= 64 {
			return strconv.Quote(s)
		}
		return strconv.Quote(s[:64]) + "...(" + strconv.Itoa(len(s)) + " bytes)"
	case reflect.Bool:
		return strconv.FormatBool(rv.Bool())
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return strconv.FormatInt(rv.Int(), 10)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return strconv.FormatUint(rv.Uint(), 10)
	}
	return reflect.TypeOf(v).String()
}

// hex16 formats h as 16 lowercase hexadecimal digits.
func hex16(h uint64) string {
	const digits = "0123456789abcdef"
	var b [16]byte
	for i := len(b) - 1; i >= 0; i-- {
		b[i] = digits[h&0xf]
		h >>= 4
	}
	return string(b[:])
}
