package recoveryobserve

import (
    "bytes"
    "encoding/json"
    "errors"
    "io"
    "strings"
)

// ErrAmbiguousObservationJSON deliberately exposes no request data.
// A duplicate key can silently replace an earlier JSON field in Go,
// including a case-insensitive spelling of an existing struct field.
// This ambiguity is prohibited BEFORE any one-use nonce is consumed.
var errAmbiguousObservationJSON = errors.New("ambiguous recovery observation JSON")

// rejectAmbiguousObservationJSON checks every JSON object in a bounded
// request, not just the top level. It accepts only one object document;
// the caller subsequently validates exact schema and signed fields.
func rejectAmbiguousObservationJSON(raw []byte) error {
    decoder := json.NewDecoder(bytes.NewReader(raw))
    if err := walkObservationJSONValue(decoder, 0, true); err != nil {
        return errAmbiguousObservationJSON
    }
    if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
        return errAmbiguousObservationJSON
    }
    return nil
}

func walkObservationJSONValue(decoder *json.Decoder, depth int, root bool) error {
    if depth > 64 {
        return errAmbiguousObservationJSON
    }
    token, err := decoder.Token()
    if err != nil {
        return err
    }
    delim, isDelim := token.(json.Delim)
    if root && (!isDelim || delim != '{') {
        return errAmbiguousObservationJSON
    }
    if !isDelim {
        return nil
    }
    switch delim {
    case '{':
        seen := make(map[string]struct{})
        for decoder.More() {
            next, err := decoder.Token()
            if err != nil {
                return err
            }
            name, ok := next.(string)
            if !ok {
                return errAmbiguousObservationJSON
            }
            // encoding/json matches struct fields case-insensitively.
            // Reject both exact duplicates and case-only aliases.
            key := strings.ToLower(name)
            if _, duplicate := seen[key]; duplicate {
                return errAmbiguousObservationJSON
            }
            seen[key] = struct{}{}
            if err := walkObservationJSONValue(decoder, depth+1, false); err != nil {
                return err
            }
        }
        closing, err := decoder.Token()
        if err != nil || closing != json.Delim('}') {
            return errAmbiguousObservationJSON
        }
    case '[':
        for decoder.More() {
            if err := walkObservationJSONValue(decoder, depth+1, false); err != nil {
                return err
            }
        }
        closing, err := decoder.Token()
        if err != nil || closing != json.Delim(']') {
            return errAmbiguousObservationJSON
        }
    default:
        return errAmbiguousObservationJSON
    }
    return nil
}
