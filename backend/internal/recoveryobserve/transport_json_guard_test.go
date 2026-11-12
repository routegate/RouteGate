package recoveryobserve

import "testing"

func TestRejectAmbiguousObservationJSON(t *testing.T) {
	for _, raw := range []string{
		`{"claim":{"nonce":"one"},"reply":{"observation":{"witness":{"generation":{"pid":311}}}}}`,
		`{"claim":{"allowed":[{"x":1},{"x":2}]},"reply":{}}`,
		`{} `,
	} {
		if err := rejectAmbiguousObservationJSON([]byte(raw)); err != nil {
			t.Fatalf("unique JSON rejected: %q: %v", raw, err)
		}
	}

	for name, raw := range map[string]string{
		"duplicate_root": `{"claim":{},"claim":{}}`,
		"case_alias_root": `{"claim":{},"Claim":{}}`,
		"escaped_key": `{"claim":{},"cl\u0061im":{}}`,
		"duplicate_nested": `{"reply":{"observation":{"witness":{"generation":{"pid":311,"pid":312}}}}}`,
		"duplicate_array_item": `{"reply":[{"observedAt":1,"observedAt":2}]}`,
		"root_array": `[{"claim":{}}]`,
		"root_null": `null`,
		"trailing_document": `{} {}`,
		"invalid_json": `{"claim":`,
	} {
		t.Run(name, func(t *testing.T) {
			if err := rejectAmbiguousObservationJSON([]byte(raw)); err == nil {
				t.Fatalf("ambiguous JSON accepted: %s", raw)
			}
		})
	}
}
