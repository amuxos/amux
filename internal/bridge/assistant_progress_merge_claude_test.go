package bridge

import "testing"

// Claude SDK steered turns: several CLI runs re-emit the same answer and,
// without message boundaries, the bridge sees it glued together.
func TestMergeSteeredFinalAnswerCollapsesGluedRepeats(t *testing.T) {
	x := "DONE-MB\n\n2 + 2 = 4\n\n3 + 3 = 6\n\n4 + 4 = 8"
	if got := mergeSteeredFinalAnswer([]string{x + x}, x); got != x {
		t.Fatalf("glued repeat not collapsed:\n%q", got)
	}
	if got := mergeSteeredFinalAnswer([]string{x + "\n\n" + x + "\n\n" + x}, x); got != x {
		t.Fatalf("blank-line repeat not collapsed:\n%q", got)
	}
	if got := mergeSteeredFinalAnswer([]string{"收到甲" + x}, x); got != "收到甲\n\n"+x {
		t.Fatalf("prefix before glued result lost:\n%q", got)
	}
	if got := mergeSteeredFinalAnswer([]string{"88"}, "88"); got != "88" {
		t.Fatalf("short repeated answer changed: %q", got)
	}
}

func TestCollapseRepeatedTextKeepsOrdinaryText(t *testing.T) {
	for _, s := range []string{"aba", "4", "hello\n\nworld", "88", "haha", "ok\n\nok"} {
		if got := collapseRepeatedText(s); got != s {
			t.Fatalf("collapse(%q) = %q", s, got)
		}
	}
}
