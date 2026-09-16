package task

import "encoding/json"

// projectUsage exposes only known normalized numeric metrics on the task stream.
// Provider-specific strings and raw fields remain outside the display projection.
func projectUsage(raw []byte) json.RawMessage {
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil {
		return nil
	}
	metrics := make(map[string]int64)
	for _, key := range []string{
		"input_tokens", "output_tokens", "total_tokens", "cached_input_tokens",
		"cache_creation_input_tokens", "reasoning_output_tokens",
		"cumulative_input_tokens", "cumulative_output_tokens", "cumulative_total_tokens",
		"cumulative_cached_input_tokens", "cumulative_cache_creation_input_tokens",
		"cumulative_reasoning_output_tokens", "context_window", "used", "size",
		"ttft_ms", "tokens_per_sec",
	} {
		var value *int64
		if json.Unmarshal(fields[key], &value) == nil && value != nil && *value >= 0 {
			metrics[key] = *value
		}
	}
	if len(metrics) == 0 {
		return nil
	}
	data, _ := json.Marshal(metrics)
	return data
}
