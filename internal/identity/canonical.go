package identity

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strconv"
	"unicode/utf16"
	"unicode/utf8"
)

// CanonicalJSON encodes the integer-and-string protocol subset of RFC 8785.
// Floating point values are intentionally rejected by the identity schemas.
func CanonicalJSON(value any) ([]byte, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var parsed any
	if err := dec.Decode(&parsed); err != nil {
		return nil, err
	}
	var out bytes.Buffer
	if err := encodeCanonical(&out, parsed); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

func encodeCanonical(out *bytes.Buffer, value any) error {
	switch v := value.(type) {
	case nil:
		out.WriteString("null")
	case bool:
		out.WriteString(strconv.FormatBool(v))
	case string:
		if !utf8.ValidString(v) {
			return errors.New("canonical JSON string is not valid UTF-8")
		}
		var stringJSON bytes.Buffer
		encoder := json.NewEncoder(&stringJSON)
		encoder.SetEscapeHTML(false)
		_ = encoder.Encode(v)
		b := bytes.TrimSuffix(stringJSON.Bytes(), []byte("\n"))
		b = bytes.ReplaceAll(b, []byte(`\u2028`), []byte("\u2028"))
		b = bytes.ReplaceAll(b, []byte(`\u2029`), []byte("\u2029"))
		out.Write(b)
	case json.Number:
		if _, err := strconv.ParseInt(v.String(), 10, 64); err != nil || bytes.ContainsAny([]byte(v.String()), ".eE+") {
			return fmt.Errorf("canonical JSON number %q is not an integer", v)
		}
		out.WriteString(v.String())
	case []any:
		out.WriteByte('[')
		for i, item := range v {
			if i > 0 {
				out.WriteByte(',')
			}
			if err := encodeCanonical(out, item); err != nil {
				return err
			}
		}
		out.WriteByte(']')
	case map[string]any:
		keys := make([]string, 0, len(v))
		for key := range v {
			keys = append(keys, key)
		}
		sort.Slice(keys, func(i, j int) bool { return utf16Less(keys[i], keys[j]) })
		out.WriteByte('{')
		for i, key := range keys {
			if i > 0 {
				out.WriteByte(',')
			}
			if err := encodeCanonical(out, key); err != nil {
				return err
			}
			out.WriteByte(':')
			if err := encodeCanonical(out, v[key]); err != nil {
				return err
			}
		}
		out.WriteByte('}')
	default:
		return fmt.Errorf("unsupported canonical JSON type %s", reflect.TypeOf(value))
	}
	return nil
}

func utf16Less(a, b string) bool {
	left, right := utf16.Encode([]rune(a)), utf16.Encode([]rune(b))
	for i := 0; i < min(len(left), len(right)); i++ {
		if left[i] != right[i] {
			return left[i] < right[i]
		}
	}
	return len(left) < len(right)
}
