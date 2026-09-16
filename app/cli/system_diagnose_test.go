package cli

import (
	"testing"

	"github.com/amuxos/amux/internal/diagnose"
)

func TestOneDiagnoseQueryRequiresExactlyOneID(t *testing.T) {
	if _, err := oneDiagnoseQuery("", "", "", ""); err == nil {
		t.Fatal("empty query should fail")
	}
	if _, err := oneDiagnoseQuery("message", "turn", "", ""); err == nil {
		t.Fatal("multiple query ids should fail")
	}
	query, err := oneDiagnoseQuery("", "", " task-1 ", "")
	if err != nil || query.Kind != diagnose.QueryTaskID || query.Value != "task-1" {
		t.Fatalf("query = %+v err=%v", query, err)
	}
}
