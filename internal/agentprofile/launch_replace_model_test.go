package agentprofile

import (
	"reflect"
	"testing"
)

func TestReplaceArgsModel(t *testing.T) {
	tests := []struct {
		name      string
		agentType string
		args      []string
		model     string
		want      []string
	}{
		{
			name:      "separated --model replaced",
			agentType: "claude",
			args:      []string{"--model", "old", "--verbose"},
			model:     "new",
			want:      []string{"--model", "new", "--verbose"},
		},
		{
			name:      "joined --model= replaced",
			agentType: "codex",
			args:      []string{"--model=old", "x"},
			model:     "new",
			want:      []string{"--model", "new", "x"},
		},
		{
			name:      "no existing model arg prepends",
			agentType: "codex",
			args:      []string{"--verbose"},
			model:     "new",
			want:      []string{"--model", "new", "--verbose"},
		},
		{
			name:      "empty model only strips",
			agentType: "codex",
			args:      []string{"--model", "old", "--verbose"},
			model:     "",
			want:      []string{"--verbose"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := ReplaceArgsModel(tc.agentType, tc.args, tc.model)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("ReplaceArgsModel(%q, %v, %q) = %v, want %v", tc.agentType, tc.args, tc.model, got, tc.want)
			}
		})
	}
}
