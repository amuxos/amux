package acp

import "encoding/json"

// normalizedPromptUsage projects the experimental ACP prompt-response counters
// without inferring their accounting scope. Agents may report a latest-call or
// session-wide snapshot; neither is evidence for an amux cumulative_* counter.
// Keep the reported total instead of adding overlapping token categories, and
// leave absent, null or invalid metrics unknown rather than manufacturing zero.
func normalizedPromptUsage(raw json.RawMessage) json.RawMessage {
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil {
		return nil
	}
	metrics := make(map[string]int64)
	for source, target := range map[string]string{
		"inputTokens":       "input_tokens",
		"outputTokens":      "output_tokens",
		"totalTokens":       "total_tokens",
		"thoughtTokens":     "reasoning_output_tokens",
		"cachedReadTokens":  "cached_input_tokens",
		"cachedWriteTokens": "cache_creation_input_tokens",
	} {
		var value *int64
		if json.Unmarshal(fields[source], &value) == nil && value != nil && *value >= 0 {
			metrics[target] = *value
		}
	}
	if len(metrics) == 0 {
		return nil
	}
	data, _ := json.Marshal(metrics)
	return data
}

// normalizedUsageUpdate keeps context occupancy and cumulative session cost
// separate from prompt-response token counters. Missing or malformed optional
// fields do not turn into a zero-valued measurement.
func normalizedUsageUpdate(raw json.RawMessage) json.RawMessage {
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil {
		return nil
	}
	usage := make(map[string]json.RawMessage)
	for _, name := range []string{"used", "size"} {
		var value *int64
		if json.Unmarshal(fields[name], &value) == nil && value != nil && *value >= 0 {
			usage[name] = fields[name]
		}
	}
	var cost map[string]json.RawMessage
	if json.Unmarshal(fields["cost"], &cost) == nil {
		var amount *float64
		var currency string
		if json.Unmarshal(cost["amount"], &amount) == nil && amount != nil && *amount >= 0 &&
			json.Unmarshal(cost["currency"], &currency) == nil && currency != "" {
			usage["cost"], _ = json.Marshal(map[string]json.RawMessage{"amount": cost["amount"], "currency": cost["currency"]})
		}
	}
	if len(usage) == 0 {
		return nil
	}
	data, _ := json.Marshal(usage)
	return data
}

func (p *Proc) setTurnUsage(usage json.RawMessage) {
	p.turnMu.Lock()
	p.turnUsage = append(p.turnUsage[:0], usage...)
	p.turnMu.Unlock()
}

// turnResultUsage keeps the latest context-window snapshot beside the final
// prompt counters. These are separate measurements, so neither token counts nor
// the cumulative session cost are added or converted to a per-turn cost.
func (p *Proc) turnResultUsage(promptUsage json.RawMessage) json.RawMessage {
	p.turnMu.Lock()
	defer p.turnMu.Unlock()
	fields := make(map[string]json.RawMessage)
	_ = json.Unmarshal(p.turnUsage, &fields)
	if fields == nil {
		fields = make(map[string]json.RawMessage)
	}
	var promptFields map[string]json.RawMessage
	_ = json.Unmarshal(promptUsage, &promptFields)
	for name, value := range promptFields {
		fields[name] = value
	}
	if len(fields) == 0 {
		return nil
	}
	data, _ := json.Marshal(fields)
	return data
}
