package acp

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestNormalizedPromptUsage(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want map[string]int64
	}{
		{
			name: "all reported counters",
			raw:  `{"inputTokens":100,"outputTokens":20,"totalTokens":120,"thoughtTokens":8,"cachedReadTokens":60,"cachedWriteTokens":12}`,
			want: map[string]int64{"input_tokens": 100, "output_tokens": 20, "total_tokens": 120, "reasoning_output_tokens": 8, "cached_input_tokens": 60, "cache_creation_input_tokens": 12},
		},
		{
			name: "explicit zero differs from missing and null",
			raw:  `{"inputTokens":0,"outputTokens":null,"cachedReadTokens":0}`,
			want: map[string]int64{"input_tokens": 0, "cached_input_tokens": 0},
		},
		{
			name: "missing total is not inferred from overlapping categories",
			raw:  `{"inputTokens":100,"outputTokens":20,"thoughtTokens":8,"cachedReadTokens":60}`,
			want: map[string]int64{"input_tokens": 100, "output_tokens": 20, "reasoning_output_tokens": 8, "cached_input_tokens": 60},
		},
		{
			name: "invalid fields do not discard valid counters",
			raw:  `{"inputTokens":-1,"outputTokens":"20","totalTokens":120,"thoughtTokens":3.5,"cachedReadTokens":{},"cachedWriteTokens":true}`,
			want: map[string]int64{"total_tokens": 120},
		},
		{
			name: "retain integer precision and discard overflow",
			raw:  `{"inputTokens":9223372036854775807,"outputTokens":9223372036854775808,"totalTokens":9007199254740993}`,
			want: map[string]int64{"input_tokens": 9223372036854775807, "total_tokens": 9007199254740993},
		},
		{
			name: "opaque fields cannot inject usage or scope",
			raw:  `{"inputTokens":10,"input_tokens":999,"cumulative_input_tokens":999,"used":88,"size":100,"cost":{"amount":5},"_meta":{"secret":"hidden"},"provider_secret":"hidden"}`,
			want: map[string]int64{"input_tokens": 10},
		},
		{name: "missing object", raw: ""},
		{name: "null object", raw: "null"},
		{name: "empty object", raw: "{}"},
		{name: "no recognized counters", raw: `{"_meta":{"inputTokens":100}}`},
		{name: "all unknown", raw: `{"inputTokens":null,"outputTokens":-1,"totalTokens":"120"}`},
		{name: "non object", raw: "[1,2,3]"},
		{name: "malformed JSON", raw: `{"inputTokens":1`},
		{name: "extra JSON value", raw: `{"inputTokens":1} {}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			raw := normalizedPromptUsage(json.RawMessage(tt.raw))
			if tt.want == nil {
				if raw != nil {
					t.Fatalf("usage = %s, want no usage", raw)
				}
				return
			}
			var got map[string]int64
			if err := json.Unmarshal(raw, &got); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("usage = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestUsageUpdatePreservesKnownMeasurements(t *testing.T) {
	for _, tt := range []struct {
		name, raw, want string
	}{
		{name: "missing capacity", raw: `{"used":20}`, want: `{"used":20}`},
		{name: "explicit zero", raw: `{"used":0,"size":0}`, want: `{"used":0,"size":0}`},
		{name: "null occupancy", raw: `{"used":null,"size":1000}`, want: `{"size":1000}`},
		{name: "invalid occupancy", raw: `{"used":-1,"size":1000}`, want: `{"size":1000}`},
		{name: "cost only", raw: `{"cost":{"amount":1.2,"currency":"USD"}}`, want: `{"cost":{"amount":1.2,"currency":"USD"}}`},
		{name: "missing cost amount", raw: `{"used":20,"cost":{"currency":"USD"}}`, want: `{"used":20}`},
		{name: "invalid cost", raw: `{"used":20,"cost":{"amount":-1,"currency":"USD"}}`, want: `{"used":20}`},
		{name: "null cost", raw: `{"size":1000,"cost":null}`, want: `{"size":1000}`},
		{name: "opaque fields omitted", raw: `{"used":20,"input_tokens":100,"cost":{"amount":1.2,"currency":"USD","private":"hidden"}}`, want: `{"used":20,"cost":{"amount":1.2,"currency":"USD"}}`},
		{name: "no measurements", raw: `{"cost":{},"used":null,"size":"unknown"}`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			line := []byte(`{"jsonrpc":"2.0","method":"session/update","params":{"update":` + tt.raw[:len(tt.raw)-1] + `,"sessionUpdate":"usage_update"}}}`)
			got, ok := semanticUpdateFromLine(line)
			if ok != (tt.want != "") {
				t.Fatalf("semanticUpdateFromLine ok=%v, want %v", ok, tt.want != "")
			}
			assertUsageJSON(t, got.Usage, tt.want)
		})
	}
}
