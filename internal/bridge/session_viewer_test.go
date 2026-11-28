package bridge

import (
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/amuxos/amux/internal/agentui"
	"github.com/amuxos/amux/internal/config"
	"github.com/amuxos/amux/internal/hubweb"
	"github.com/amuxos/amux/internal/i18n"
)

func TestSessionViewerURL(t *testing.T) {
	t.Parallel()
	expiresAt := time.Unix(2000, 0)
	got := sessionViewerURLWithExpiry("https://amux.example.com/base/", "viewer secret", "cloud/1", "feishu a+b", expiresAt)
	u, err := url.Parse(got)
	if err != nil {
		t.Fatal(err)
	}
	if gotHost := u.Scheme + "://" + u.Host; gotHost != "https://amux.example.com" {
		t.Fatalf("url host = %q", gotHost)
	}
	if u.RawQuery != "" {
		t.Fatalf("url query = %q, want empty", u.RawQuery)
	}
	if !strings.HasPrefix(u.Path, "/base/s/") {
		t.Fatalf("path = %q, want /base/s/<token>", u.Path)
	}
	token := strings.TrimPrefix(u.Path, "/base/s/")
	if token == "" || token == "viewer secret" {
		t.Fatalf("token = %q, want signed scoped token", token)
	}
	if err := hubweb.VerifySessionToken("viewer secret", token, "cloud/1", "feishu a+b", time.Unix(1000, 0)); err != nil {
		t.Fatalf("signed token did not verify: %v", err)
	}
}

func TestSessionViewerURLEmptyWithoutBaseURL(t *testing.T) {
	t.Parallel()
	if got := sessionViewerURL("", "viewer-secret", "target-1", "feishu-a1"); got != "" {
		t.Fatalf("sessionViewerURL without base = %q, want empty", got)
	}
}

func TestTurnProcessCardUsesScopedSidebarInBothLanguages(t *testing.T) {
	now := time.Date(2026, time.November, 27, 8, 15, 0, 0, time.UTC)
	b := &Bridge{}
	meta := toolActivityCardMeta{
		Status: "running", AgentType: "codex", EffectiveModel: "gpt",
		Activity: &agentui.ActivityView{
			CurrentStep: "private progress in card", Tools: []agentui.ToolEvent{{Name: "sample-tool"}},
		},
	}
	if link := b.turnProcessURL("target", "session", "turn-2", config.FeishuThinkingProcessCollapsed, now); link != "" {
		t.Fatal("unconfigured viewer should keep inline details")
	}
	inline := renderToolActivityCard(meta, "")
	if !strings.Contains(inline, "sample-tool") || !strings.Contains(inline, "private progress in card") ||
		!strings.Contains(inline, `"tag":"hr"`) || !strings.Contains(inline, `"tag":"collapsible_panel"`) {
		t.Fatal("inline fallback lost its details, dividers, or collapsible panels")
	}
	b.SetSessionViewer("https://sessions.example", "legacy-secret")
	b.SetTurnProcessViewer(func() (string, string) {
		return "https://viewer.example/base?ignored=x#old", "viewer-secret"
	})
	if link := b.turnProcessURL("target", "session", "turn-2", config.FeishuThinkingProcessCollapsed, now); link != "" {
		t.Fatal("disabled experiment emitted a Turn viewer link")
	}
	b.ApplyLiveExperimentSettings(config.ExperimentsSettings{TurnProcessMode: config.FeishuTurnProcessSidebar})
	meta.ProcessURL = b.turnProcessURL("target", "session", "turn-2", config.FeishuThinkingProcessHidden, now)
	if meta.ProcessURL == "" || meta.ProcessURL != b.turnProcessURL("target", "session", "turn-2", config.FeishuThinkingProcessHidden, now.Add(time.Minute)) {
		t.Fatal("viewer link must exist and stay stable across card updates")
	}
	viewer, err := url.Parse(meta.ProcessURL)
	if err != nil || !strings.HasPrefix(viewer.Path, "/base/t/") || viewer.RawQuery != "" || viewer.Fragment != "" {
		t.Fatalf("incorrect viewer URL: %v, %v", viewer, err)
	}
	claims, err := hubweb.SessionTokenScope("viewer-secret", strings.TrimPrefix(viewer.Path, "/base/t/"), now)
	if err != nil || claims.TargetID != "target" || claims.Name != "session" || claims.TurnID != "turn-2" || !claims.ToolsOnly {
		t.Fatalf("incorrect turn link scope: %+v, %v", claims, err)
	}
	activity := renderTurnActivity(meta)
	for _, raw := range []string{
		renderToolActivityCard(meta, ""), renderCardKitData(activity.elements()),
		renderAgentReplyCompletionCard(&activity), string(activity.JSON),
	} {
		if strings.Contains(raw, "sample-tool") || strings.Contains(raw, "private progress in card") ||
			!strings.Contains(raw, cardKitExecutionSummaryID) {
			t.Fatal("processing card should retain its title and move inline details to the viewer")
		}
		for _, tt := range []struct {
			locale i18n.Locale
			label  string
		}{{i18n.English, "Processing details"}, {i18n.Chinese, "处理过程"}} {
			localized := selectManagementCardLocale(raw, tt.locale)
			if !strings.Contains(localized, tt.label) || !strings.Contains(localized, "sidebar-semi") {
				t.Errorf("missing localized sidebar action: %s", localized)
			}
		}
	}
	button := renderProcessingLink(meta.ProcessURL)
	if button.Type != "text" || button.Width != "default" {
		t.Fatalf("processing details should render as a text button: %+v", button)
	}
	pc, err := url.Parse(button.Behaviors[0].PCURL)
	if err != nil || pc.Query().Get("mode") != "sidebar-semi" ||
		pc.Query().Get("url") != meta.ProcessURL || button.Behaviors[0].DefaultURL != meta.ProcessURL {
		t.Fatalf("incorrect desktop and fallback paths: %+v", button.Behaviors)
	}
	b.ApplyLiveExperimentSettings(config.ExperimentsSettings{})
	meta.ProcessURL = b.turnProcessURL("target", "session", "turn-2", config.FeishuThinkingProcessCollapsed, now)
	if got := renderToolActivityCard(meta, ""); got != inline {
		t.Fatal("turning off the experiment changed the original card")
	}
}
