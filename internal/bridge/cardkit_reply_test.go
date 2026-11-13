package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/amuxos/amux/internal/agentui"
	lark "github.com/larksuite/oapi-sdk-go/v3"
)

type fakeCardKitReplyAPI struct {
	cardID      string
	cardIDs     []string
	createErr   error
	updateErr   error
	insertErrs  []error
	finishErr   error
	createdData []string
	inserts     []cardKitElementInsert
	replaces    []cardKitElementReplace
	updates     []cardKitReplyUpdate
	finishes    []cardKitReplyFinish
}

func (f *fakeCardKitReplyAPI) CreateCard(_ context.Context, data string) (string, error) {
	f.createdData = append(f.createdData, data)
	if f.createErr != nil {
		return "", f.createErr
	}
	if len(f.cardIDs) > 0 {
		cardID := f.cardIDs[0]
		f.cardIDs = f.cardIDs[1:]
		return cardID, nil
	}
	return f.cardID, nil
}

func (f *fakeCardKitReplyAPI) UpdateElement(_ context.Context, update cardKitReplyUpdate) error {
	f.updates = append(f.updates, update)
	return f.updateErr
}

func (f *fakeCardKitReplyAPI) InsertElements(_ context.Context, insert cardKitElementInsert) error {
	f.inserts = append(f.inserts, insert)
	if len(f.insertErrs) > 0 {
		err := f.insertErrs[0]
		f.insertErrs = f.insertErrs[1:]
		return err
	}
	return f.updateErr
}

func (f *fakeCardKitReplyAPI) ReplaceElement(_ context.Context, replace cardKitElementReplace) error {
	f.replaces = append(f.replaces, replace)
	return f.updateErr
}

func (f *fakeCardKitReplyAPI) FinishCard(_ context.Context, finish cardKitReplyFinish) error {
	f.finishes = append(f.finishes, finish)
	return f.finishErr
}

func TestCardKitReplyDataEnablesStreamingMarkdown(t *testing.T) {
	raw := renderCardKitReplyData("hello **world**")
	if !strings.Contains(raw, `"summary"`) || !strings.Contains(raw, `"streaming_config"`) {
		t.Fatalf("card config missing summary or streaming_config: %s", raw)
	}
	var card struct {
		Schema string `json:"schema"`
		Config struct {
			StreamingMode  bool   `json:"streaming_mode"`
			UpdateMulti    bool   `json:"update_multi"`
			WideScreenMode bool   `json:"wide_screen_mode"`
			WidthMode      string `json:"width_mode"`
			Summary        struct {
				Content string `json:"content"`
			} `json:"summary"`
			StreamingConfig struct {
				PrintFrequencyMS map[string]int `json:"print_frequency_ms"`
				PrintStep        map[string]int `json:"print_step"`
				PrintStrategy    string         `json:"print_strategy"`
			} `json:"streaming_config"`
		} `json:"config"`
		Body struct {
			Elements        []cardElement `json:"elements"`
			Direction       string        `json:"direction"`
			VerticalSpacing string        `json:"vertical_spacing"`
			Padding         string        `json:"padding"`
		} `json:"body"`
	}
	if err := json.Unmarshal([]byte(raw), &card); err != nil {
		t.Fatalf("renderCardKitReplyData returned invalid JSON: %v\n%s", err, raw)
	}
	if card.Schema != "2.0" {
		t.Fatalf("schema = %q, want 2.0", card.Schema)
	}
	if !card.Config.StreamingMode || !card.Config.UpdateMulti || !card.Config.WideScreenMode || card.Config.WidthMode != "fill" {
		t.Fatalf("config = %+v, want streaming/update_multi and fill-width widescreen", card.Config)
	}
	if card.Config.Summary.Content != "" {
		t.Fatalf("summary = %+v, want empty streaming preview", card.Config.Summary)
	}
	if card.Config.StreamingConfig.PrintFrequencyMS["default"] != 70 ||
		card.Config.StreamingConfig.PrintStep["default"] != 1 ||
		card.Config.StreamingConfig.PrintStrategy != "fast" {
		t.Fatalf("streaming config = %+v", card.Config.StreamingConfig)
	}
	if len(card.Body.Elements) != 1 {
		t.Fatalf("elements = %d, want 1", len(card.Body.Elements))
	}
	if card.Body.Direction != "vertical" || card.Body.VerticalSpacing != "12px" || card.Body.Padding != "12px 16px 16px 16px" {
		t.Fatalf("body layout = direction:%q spacing:%q padding:%q, want compact baseline rhythm", card.Body.Direction, card.Body.VerticalSpacing, card.Body.Padding)
	}
	root := card.Body.Elements[0]
	if root.Tag != "column_set" || root.ElementID != cardKitActivityElementID || len(root.Columns) != 1 || len(root.Columns[0].Elements) != 4 {
		t.Fatalf("turn card root = %+v, want one shared activity_section container", root)
	}
	element := root.Columns[0].Elements[1]
	if element.Tag != "markdown" || element.ElementID != cardKitReplyElementID || element.Content != renderAgentResultBody("hello **world**") {
		t.Fatalf("element = %+v", element)
	}
	brandDivider, brandFooter := root.Columns[0].Elements[2], root.Columns[0].Elements[3]
	if brandDivider.Tag != "hr" || brandDivider.ElementID != amuxBrandDividerID ||
		brandFooter.Tag != "markdown" || brandFooter.ElementID != amuxBrandFooterID || brandFooter.Content != amuxBrandFooterMarkdown() || brandFooter.TextSize != "notation" {
		t.Fatalf("brand footer = %+v, %+v", brandDivider, brandFooter)
	}
}

func TestCardKitMessageContentReferencesCardID(t *testing.T) {
	raw := renderCardKitMessageContent("card-123")
	var content struct {
		Type string `json:"type"`
		Data struct {
			CardID string `json:"card_id"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(raw), &content); err != nil {
		t.Fatalf("renderCardKitMessageContent returned invalid JSON: %v\n%s", err, raw)
	}
	if content.Type != "card" || content.Data.CardID != "card-123" {
		t.Fatalf("content = %+v", content)
	}
}

func TestCardKitReplyLifecycleUsesFullTextIncreasingSequenceAndFreshUUIDs(t *testing.T) {
	api := &fakeCardKitReplyAPI{cardID: "card-123"}
	var sentContent string
	backend := &bindingReplyCardBackend{
		cardKit: api,
		sendCard: func(_ context.Context, content, _ string) (string, error) {
			sentContent = content
			return "message-123", nil
		},
	}

	handle, err := backend.Create(context.Background(), "hello")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if handle.cardID != "card-123" || handle.messageID != "message-123" || handle.mode != replyCardModeCardKit {
		t.Fatalf("handle = %+v", handle)
	}
	if sentContent != renderCardKitMessageContent("card-123") {
		t.Fatalf("sent content = %s", sentContent)
	}
	if err := backend.Update(context.Background(), handle, "hello world"); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if err := backend.Finish(context.Background(), handle, "hello world!"); err != nil {
		t.Fatalf("Finish: %v", err)
	}
	if err := backend.Finish(context.Background(), handle, "ignored"); err != nil {
		t.Fatalf("second Finish: %v", err)
	}

	if len(api.updates) != 2 {
		t.Fatalf("updates = %+v, want 2", api.updates)
	}
	if got := []string{api.updates[0].Text, api.updates[1].Text}; !reflect.DeepEqual(got, []string{renderAgentResultBody("hello world"), renderAgentResultBody("hello world!")}) {
		t.Fatalf("update texts = %#v", got)
	}
	if api.updates[0].Sequence != 1 || api.updates[1].Sequence != 2 {
		t.Fatalf("update sequences = %d, %d", api.updates[0].Sequence, api.updates[1].Sequence)
	}
	if api.updates[0].UUID == "" || api.updates[1].UUID == "" || api.updates[0].UUID == api.updates[1].UUID {
		t.Fatalf("update UUIDs must be fresh: %q %q", api.updates[0].UUID, api.updates[1].UUID)
	}
	if len(api.finishes) != 1 || api.finishes[0].Sequence != 3 || api.finishes[0].UUID == "" {
		t.Fatalf("finishes = %+v", api.finishes)
	}
	if api.finishes[0].UUID == api.updates[1].UUID {
		t.Fatal("finish UUID must differ from update UUID")
	}
	if !handle.finished || handle.lastText != "hello world!" {
		t.Fatalf("finished handle = %+v", handle)
	}
}

func TestCardKitReplyUpdateKeepsBridgeOwnedBrandFooterSingle(t *testing.T) {
	api := &fakeCardKitReplyAPI{cardID: "card-123"}
	backend := &bindingReplyCardBackend{
		cardKit: api,
		sendCard: func(context.Context, string, string) (string, error) {
			return "message-123", nil
		},
	}

	handle, err := backend.Create(context.Background(), "starting")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	answer := "杭州明天天气有雨\n\n---\n\n" + legacyAgentBrandFooter()
	if err := backend.Finish(context.Background(), handle, answer); err != nil {
		t.Fatalf("Finish: %v", err)
	}

	if len(api.updates) != 1 {
		t.Fatalf("updates = %+v, want one final answer update", api.updates)
	}
	if got, want := api.updates[0].Text, "杭州明天天气有雨"; got != want {
		t.Fatalf("final answer update = %q, want %q", got, want)
	}
}

func TestCardKitMainTurnActivityFirstAddsAnswerToSameCard(t *testing.T) {
	api := &fakeCardKitReplyAPI{cardID: "card-123"}
	sends := 0
	backend := &bindingReplyCardBackend{
		cardKit: api,
		sendCard: func(context.Context, string, string) (string, error) {
			sends++
			return "message-123", nil
		},
	}
	activity := renderTurnActivity(toolActivityCardMeta{
		Status:   "running",
		Activity: &agentui.ActivityView{Status: "running", CurrentStep: "检查代码"},
	})

	handle, err := backend.CreateActivity(context.Background(), activity)
	if err != nil {
		t.Fatalf("CreateActivity: %v", err)
	}
	if err := backend.Update(context.Background(), handle, "最终回答"); err != nil {
		t.Fatalf("Update answer: %v", err)
	}
	if err := backend.Finish(context.Background(), handle, "最终回答"); err != nil {
		t.Fatalf("Finish: %v", err)
	}

	if sends != 1 {
		t.Fatalf("sends = %d, want one CardKit message", sends)
	}
	assertCardElementOrder(t, api.createdData[0], cardKitActivityElementID)
	assertTurnCardChildOrder(t, api.createdData[0], cardKitActivityContentID)
	if len(api.inserts) != 1 {
		t.Fatalf("inserts = %+v, want answer insert", api.inserts)
	}
	insert := api.inserts[0]
	if insert.Type != "insert_after" || insert.TargetElementID != cardKitActivityContentID || !strings.Contains(insert.Elements, cardKitReplyElementID) {
		t.Fatalf("answer insert = %+v", insert)
	}
	assertInsertedElementOrder(t, insert.Elements, cardKitAnswerDividerElementID, cardKitAnswerHeadingID, cardKitReplyElementID, amuxBrandDividerID, amuxBrandFooterID)
	if !strings.Contains(insert.Elements, "**最终结果**") {
		t.Fatalf("answer insert missing final-result heading: %s", insert.Elements)
	}
	if insert.Sequence != 1 || len(api.finishes) != 1 || api.finishes[0].Sequence != 2 {
		t.Fatalf("sequences insert=%d finishes=%+v", insert.Sequence, api.finishes)
	}
	if !handle.hasAnswer || !handle.hasActivity {
		t.Fatalf("handle = %+v, want answer and activity", handle)
	}
}

func TestCardKitMainTurnAnswerFirstMovesActivityBeforeAnswer(t *testing.T) {
	api := &fakeCardKitReplyAPI{cardID: "card-123"}
	sends := 0
	backend := &bindingReplyCardBackend{
		cardKit: api,
		sendCard: func(context.Context, string, string) (string, error) {
			sends++
			return "message-123", nil
		},
	}
	handle, err := backend.Create(context.Background(), "回答中")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	running := renderTurnActivity(toolActivityCardMeta{
		Status:   "running",
		Activity: &agentui.ActivityView{Status: "running", CurrentStep: "检查代码"},
	})
	done := renderTurnActivity(toolActivityCardMeta{
		Status:   "done",
		Activity: &agentui.ActivityView{Status: "done", CurrentStep: "检查完成"},
	})
	if err := backend.UpdateActivity(context.Background(), handle, running); err != nil {
		t.Fatalf("UpdateActivity insert: %v", err)
	}
	if err := backend.Update(context.Background(), handle, "最终回答"); err != nil {
		t.Fatalf("Update answer: %v", err)
	}
	if err := backend.UpdateActivity(context.Background(), handle, done); err != nil {
		t.Fatalf("UpdateActivity replace: %v", err)
	}
	if err := backend.Finish(context.Background(), handle, "最终回答"); err != nil {
		t.Fatalf("Finish: %v", err)
	}

	if sends != 1 {
		t.Fatalf("sends = %d, want one CardKit message", sends)
	}
	assertCardElementOrder(t, api.createdData[0], cardKitActivityElementID)
	assertTurnCardChildOrder(t, api.createdData[0], cardKitAnswerHeadingID, cardKitReplyElementID, amuxBrandDividerID, amuxBrandFooterID)
	if len(api.inserts) != 1 || api.inserts[0].Type != "insert_before" || api.inserts[0].TargetElementID != cardKitAnswerHeadingID {
		t.Fatalf("activity inserts = %+v", api.inserts)
	}
	assertInsertedElementOrder(t, api.inserts[0].Elements, cardKitActivityContentID, cardKitAnswerDividerElementID)
	if len(api.updates) != 1 || api.updates[0].Text != renderAgentResultBody("最终回答") {
		t.Fatalf("answer updates = %+v", api.updates)
	}
	if len(api.replaces) != 1 || api.replaces[0].ElementID != cardKitActivityContentID {
		t.Fatalf("activity replaces = %+v", api.replaces)
	}
	gotSequence := []int{api.inserts[0].Sequence, api.updates[0].Sequence, api.replaces[0].Sequence, api.finishes[0].Sequence}
	if !reflect.DeepEqual(gotSequence, []int{1, 2, 3, 4}) {
		t.Fatalf("sequences = %#v, want [1 2 3 4]", gotSequence)
	}
	uuids := []string{api.inserts[0].UUID, api.updates[0].UUID, api.replaces[0].UUID, api.finishes[0].UUID}
	seen := map[string]bool{}
	for _, id := range uuids {
		if id == "" || seen[id] {
			t.Fatalf("mutation UUIDs must be non-empty and unique: %#v", uuids)
		}
		seen[id] = true
	}
}

func TestCardKitConversationSectionStaysBeforeFooter(t *testing.T) {
	for _, activityFirst := range []bool{false, true} {
		name := "answer_first"
		if activityFirst {
			name = "activity_first"
		}
		t.Run(name, func(t *testing.T) {
			api := &fakeCardKitReplyAPI{cardID: "card-123"}
			sends := 0
			backend := &bindingReplyCardBackend{
				conversationSection: true,
				cardKit:             api,
				sendCard: func(context.Context, string, string) (string, error) {
					sends++
					return "message-123", nil
				},
			}
			ctx := context.Background()
			activity := renderTurnActivity(toolActivityCardMeta{Status: "running"})
			var handle *replyCardHandle
			var err error
			if activityFirst {
				handle, err = backend.CreateActivity(ctx, activity)
			} else {
				handle, err = backend.Create(ctx, "answer")
			}
			if err != nil {
				t.Fatal(err)
			}
			if activityFirst {
				// No placeholder outside the turn container before the answer exists.
				assertCardElementOrder(t, api.createdData[0], cardKitActivityElementID)
				assertTurnCardChildOrder(t, api.createdData[0], cardKitActivityContentID)
				if err := backend.Update(ctx, handle, "answer"); err != nil {
					t.Fatal(err)
				}
				if len(api.inserts) != 1 || api.inserts[0].Type != "insert_after" || api.inserts[0].TargetElementID != cardKitActivityContentID {
					t.Fatalf("answer insert = %+v", api.inserts)
				}
				assertInsertedElementOrder(t, api.inserts[0].Elements, cardKitAnswerDividerElementID,
					cardKitAnswerHeadingID, cardKitReplyElementID, amuxBrandDividerID, conversationSectionID, amuxBrandFooterID)
			} else {
				// Inspect the real nested renderer output, not only body.elements.
				assertCardElementOrder(t, api.createdData[0], cardKitActivityElementID)
				assertTurnCardChildOrder(t, api.createdData[0], cardKitAnswerHeadingID, cardKitReplyElementID,
					amuxBrandDividerID, conversationSectionID, amuxBrandFooterID)
				if err := backend.UpdateActivity(ctx, handle, activity); err != nil {
					t.Fatal(err)
				}
				if len(api.inserts) != 1 || api.inserts[0].Type != "insert_before" || api.inserts[0].TargetElementID != cardKitAnswerHeadingID {
					t.Fatalf("activity insert = %+v", api.inserts)
				}
				assertInsertedElementOrder(t, api.inserts[0].Elements, cardKitActivityContentID, cardKitAnswerDividerElementID)
			}
			if err := backend.Finish(ctx, handle, "final answer"); err != nil {
				t.Fatal(err)
			}
			if sends != 1 || !handle.hasAnswer || !handle.conversationSection || !handle.finished {
				t.Fatalf("unexpected delivery: sends=%d handle=%+v", sends, handle)
			}
			if len(api.updates) != 1 || api.updates[0].ElementID != cardKitReplyElementID {
				t.Fatalf("final update must only change answer text: %+v", api.updates)
			}
		})
	}
}

func TestCardKitMainTurnRetriesAmbiguousInsertWithSameIdentityBeforeFinish(t *testing.T) {
	api := &fakeCardKitReplyAPI{
		cardID:     "card-123",
		insertErrs: []error{&ambiguousCardKitMutationError{err: context.DeadlineExceeded}},
	}
	backend := &bindingReplyCardBackend{
		cardKit: api,
		sendCard: func(context.Context, string, string) (string, error) {
			return "message-123", nil
		},
	}
	handle, err := backend.Create(context.Background(), "answer")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	activity := renderTurnActivity(toolActivityCardMeta{Status: "done"})
	if err := backend.UpdateActivity(context.Background(), handle, activity); err == nil {
		t.Fatal("UpdateActivity error = nil, want ambiguous insert failure")
	}
	if err := backend.Finish(context.Background(), handle, "answer"); err != nil {
		t.Fatalf("Finish retries insert: %v", err)
	}

	if len(api.inserts) != 2 {
		t.Fatalf("inserts = %+v, want original plus idempotent retry", api.inserts)
	}
	first, retry := api.inserts[0], api.inserts[1]
	if first.UUID == "" || first.UUID != retry.UUID || first.Sequence != retry.Sequence || first.Elements != retry.Elements {
		t.Fatalf("insert identity changed: first=%+v retry=%+v", first, retry)
	}
	if !handle.hasActivity || handle.pendingInsert != nil {
		t.Fatalf("handle after retry = %+v", handle)
	}
	if len(api.finishes) != 1 || api.finishes[0].Sequence != first.Sequence+1 {
		t.Fatalf("finishes = %+v, want next sequence", api.finishes)
	}
}

func assertCardElementOrder(t *testing.T, raw string, wantIDs ...string) {
	t.Helper()
	var card struct {
		Body struct {
			Elements []struct {
				ElementID string `json:"element_id"`
			} `json:"elements"`
		} `json:"body"`
	}
	if err := json.Unmarshal([]byte(raw), &card); err != nil {
		t.Fatalf("invalid card JSON: %v\n%s", err, raw)
	}
	var got []string
	for _, element := range card.Body.Elements {
		got = append(got, element.ElementID)
	}
	if !reflect.DeepEqual(got, wantIDs) {
		t.Fatalf("element order = %#v, want %#v", got, wantIDs)
	}
}

func assertTurnCardChildOrder(t *testing.T, raw string, wantIDs ...string) {
	t.Helper()
	var card struct {
		Body struct {
			Elements []cardElement `json:"elements"`
		} `json:"body"`
	}
	if err := json.Unmarshal([]byte(raw), &card); err != nil {
		t.Fatalf("invalid card JSON: %v\n%s", err, raw)
	}
	if len(card.Body.Elements) != 1 || len(card.Body.Elements[0].Columns) != 1 {
		t.Fatalf("turn card root = %+v, want one column", card.Body.Elements)
	}
	children := card.Body.Elements[0].Columns[0].Elements
	got := make([]string, 0, len(children))
	for _, child := range children {
		got = append(got, child.ElementID)
	}
	if !reflect.DeepEqual(got, wantIDs) {
		t.Fatalf("turn card child order = %#v, want %#v", got, wantIDs)
	}
}

func assertInsertedElementOrder(t *testing.T, raw string, wantIDs ...string) {
	t.Helper()
	var elements []struct {
		ElementID string `json:"element_id"`
	}
	if err := json.Unmarshal([]byte(raw), &elements); err != nil {
		t.Fatalf("invalid inserted elements JSON: %v\n%s", err, raw)
	}
	var got []string
	for _, element := range elements {
		got = append(got, element.ElementID)
	}
	if !reflect.DeepEqual(got, wantIDs) {
		t.Fatalf("inserted element order = %#v, want %#v", got, wantIDs)
	}
}

func TestCardKitReplyFallsBackBeforeMessageIsVisible(t *testing.T) {
	tests := []struct {
		name      string
		createErr error
		sendErr   error
	}{
		{name: "entity create fails", createErr: errors.New("no cardkit scope")},
		{name: "entity send fails", sendErr: errors.New("send failed")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			api := &fakeCardKitReplyAPI{cardID: "card-123", createErr: tt.createErr}
			legacyCreates := 0
			backend := &bindingReplyCardBackend{
				cardKit: api,
				sendCard: func(context.Context, string, string) (string, error) {
					return "", tt.sendErr
				},
				createLegacy: func(context.Context, string) (string, error) {
					legacyCreates++
					return "legacy-123", nil
				},
			}

			handle, err := backend.Create(context.Background(), "answer")
			if err != nil {
				t.Fatalf("Create: %v", err)
			}
			if handle.mode != replyCardModeLegacy || handle.messageID != "legacy-123" {
				t.Fatalf("handle = %+v", handle)
			}
			if legacyCreates != 1 {
				t.Fatalf("legacy creates = %d, want 1", legacyCreates)
			}
		})
	}
}

func TestCardKitReplyRetriesAmbiguousSendWithSameUUID(t *testing.T) {
	api := &fakeCardKitReplyAPI{cardID: "card-123"}
	var requestUUIDs []string
	legacyCreates := 0
	backend := &bindingReplyCardBackend{
		cardKit: api,
		sendCard: func(_ context.Context, _, requestUUID string) (string, error) {
			requestUUIDs = append(requestUUIDs, requestUUID)
			if len(requestUUIDs) == 1 {
				return "", &ambiguousCardKitSendError{err: context.DeadlineExceeded}
			}
			return "message-123", nil
		},
		createLegacy: func(context.Context, string) (string, error) {
			legacyCreates++
			return "legacy-123", nil
		},
	}

	handle, err := backend.Create(context.Background(), "answer")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if handle.mode != replyCardModeCardKit || handle.messageID != "message-123" {
		t.Fatalf("handle = %+v", handle)
	}
	if len(requestUUIDs) != 2 || requestUUIDs[0] == "" || requestUUIDs[0] != requestUUIDs[1] {
		t.Fatalf("request UUIDs = %#v, want same non-empty UUID", requestUUIDs)
	}
	if legacyCreates != 0 {
		t.Fatalf("legacy creates = %d, want 0", legacyCreates)
	}
}

func TestCardKitReplyFallsBackWhenAmbiguousRetryFailsDefinitely(t *testing.T) {
	api := &fakeCardKitReplyAPI{cardID: "card-123"}
	sends := 0
	legacyCreates := 0
	backend := &bindingReplyCardBackend{
		cardKit: api,
		sendCard: func(context.Context, string, string) (string, error) {
			sends++
			if sends == 1 {
				return "", &ambiguousCardKitSendError{err: context.DeadlineExceeded}
			}
			return "", errors.New("definite send rejection")
		},
		createLegacy: func(context.Context, string) (string, error) {
			legacyCreates++
			return "legacy-123", nil
		},
	}

	handle, err := backend.Create(context.Background(), "answer")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if handle.mode != replyCardModeLegacy || handle.deliveryPending || handle.messageID != "legacy-123" {
		t.Fatalf("handle = %+v, want delivered legacy handle", handle)
	}
	if sends != 2 || legacyCreates != 1 {
		t.Fatalf("sends=%d legacyCreates=%d, want 2 and 1", sends, legacyCreates)
	}
}

func TestCardKitActivityReturnsDefiniteAmbiguousRetryFailure(t *testing.T) {
	api := &fakeCardKitReplyAPI{cardID: "card-123"}
	sends := 0
	backend := &bindingReplyCardBackend{
		cardKit: api,
		sendCard: func(context.Context, string, string) (string, error) {
			sends++
			if sends == 1 {
				return "", &ambiguousCardKitSendError{err: context.DeadlineExceeded}
			}
			return "", errors.New("definite send rejection")
		},
	}

	handle, err := backend.CreateActivity(context.Background(), renderTurnActivity(toolActivityCardMeta{Status: "running"}))
	if err == nil {
		t.Fatalf("CreateActivity handle = %+v, want definite send error", handle)
	}
	if handle != nil || sends != 2 {
		t.Fatalf("handle=%+v sends=%d, want nil and 2", handle, sends)
	}
}

func TestCardKitReplyDoesNotFallbackWhenSendOutcomeRemainsAmbiguous(t *testing.T) {
	api := &fakeCardKitReplyAPI{cardID: "card-123"}
	legacyCreates := 0
	var requestUUIDs []string
	backend := &bindingReplyCardBackend{
		cardKit: api,
		sendCard: func(_ context.Context, _, requestUUID string) (string, error) {
			requestUUIDs = append(requestUUIDs, requestUUID)
			if len(requestUUIDs) <= 2 {
				return "", &ambiguousCardKitSendError{err: context.DeadlineExceeded}
			}
			return "message-123", nil
		},
		createLegacy: func(context.Context, string) (string, error) {
			legacyCreates++
			return "legacy-123", nil
		},
	}

	handle, err := backend.Create(context.Background(), "answer")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if !handle.deliveryPending || handle.cardID != "card-123" {
		t.Fatalf("pending handle = %+v", handle)
	}
	if err := backend.Update(context.Background(), handle, "answer plus"); err != nil {
		t.Fatalf("Update retries pending delivery: %v", err)
	}
	if legacyCreates != 0 {
		t.Fatalf("legacy creates = %d, want 0", legacyCreates)
	}
	if len(api.createdData) != 1 {
		t.Fatalf("CardKit creates = %d, want 1", len(api.createdData))
	}
	if len(requestUUIDs) != 3 || requestUUIDs[0] != requestUUIDs[1] || requestUUIDs[1] != requestUUIDs[2] {
		t.Fatalf("request UUIDs = %#v, want one preserved UUID", requestUUIDs)
	}
	if handle.deliveryPending || handle.messageID != "message-123" {
		t.Fatalf("delivered handle = %+v", handle)
	}
}

func TestCardKitReplyDoesNotFallbackAfterMessageIsVisible(t *testing.T) {
	api := &fakeCardKitReplyAPI{cardID: "card-123", updateErr: errors.New("update failed")}
	legacyCreates := 0
	backend := &bindingReplyCardBackend{
		cardKit: api,
		sendCard: func(context.Context, string, string) (string, error) {
			return "message-123", nil
		},
		createLegacy: func(context.Context, string) (string, error) {
			legacyCreates++
			return "legacy-123", nil
		},
	}

	handle, err := backend.Create(context.Background(), "answer")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := backend.Update(context.Background(), handle, "answer plus"); err == nil {
		t.Fatal("Update error = nil, want failure")
	}
	if legacyCreates != 0 {
		t.Fatalf("legacy creates = %d, want 0", legacyCreates)
	}
	if handle.sequence != 1 || handle.lastText != "answer" {
		t.Fatalf("handle after failed update = %+v", handle)
	}
}

func TestCardKitReplyFinishRemainsRetryableAfterFailure(t *testing.T) {
	api := &fakeCardKitReplyAPI{cardID: "card-123", finishErr: errors.New("finish failed")}
	backend := &bindingReplyCardBackend{
		cardKit: api,
		sendCard: func(context.Context, string, string) (string, error) {
			return "message-123", nil
		},
	}
	handle, err := backend.Create(context.Background(), "answer")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := backend.Finish(context.Background(), handle, "answer"); err == nil {
		t.Fatal("first Finish error = nil, want failure")
	}
	if handle.finished {
		t.Fatal("handle marked finished after failed close")
	}
	api.finishErr = nil
	if err := backend.Finish(context.Background(), handle, "answer"); err != nil {
		t.Fatalf("second Finish: %v", err)
	}
	if !handle.finished || len(api.finishes) != 2 || api.finishes[1].Sequence <= api.finishes[0].Sequence {
		t.Fatalf("handle = %+v finishes = %+v", handle, api.finishes)
	}
}

func TestLegacyReplyFinishRemainsRetryableAfterPatchFailure(t *testing.T) {
	api := &fakeCardKitReplyAPI{createErr: errors.New("cardkit unavailable")}
	patchErr := errors.New("patch failed")
	patches := 0
	backend := &bindingReplyCardBackend{
		cardKit: api,
		createLegacy: func(context.Context, string) (string, error) {
			return "legacy-123", nil
		},
		patchLegacy: func(context.Context, string, string) error {
			patches++
			return patchErr
		},
	}
	handle, err := backend.Create(context.Background(), "partial")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := backend.Finish(context.Background(), handle, "complete"); err == nil {
		t.Fatal("first Finish error = nil, want patch failure")
	}
	if handle.finished {
		t.Fatal("legacy handle marked finished after failed patch")
	}
	patchErr = nil
	if err := backend.Finish(context.Background(), handle, "complete"); err != nil {
		t.Fatalf("second Finish: %v", err)
	}
	if !handle.finished || patches != 2 {
		t.Fatalf("handle = %+v patches = %d", handle, patches)
	}
}

func TestCardKitReplyCalibratesFinishedSplitCard(t *testing.T) {
	api := &fakeCardKitReplyAPI{cardID: "card-123"}
	backend := &bindingReplyCardBackend{
		cardKit: api,
		sendCard: func(context.Context, string, string) (string, error) {
			return "message-123", nil
		},
	}
	handle, err := backend.Create(context.Background(), "original")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := backend.Finish(context.Background(), handle, "original"); err != nil {
		t.Fatalf("Finish: %v", err)
	}
	finishSequence := handle.sequence
	if err := backend.Calibrate(context.Background(), handle, "authoritative"); err != nil {
		t.Fatalf("Calibrate: %v", err)
	}
	if !handle.finished || handle.lastText != "authoritative" || handle.sequence <= finishSequence {
		t.Fatalf("handle = %+v, finish sequence = %d", handle, finishSequence)
	}
	if len(api.updates) != 1 || api.updates[0].Text != renderAgentResultBody("authoritative") {
		t.Fatalf("updates = %+v", api.updates)
	}
}

func TestBindingReplyCardBackendStreamsCardKitToThread(t *testing.T) {
	type call struct {
		method string
		path   string
		body   map[string]any
	}
	var mu sync.Mutex
	var calls []call
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/open-apis/auth/v3/tenant_access_token/internal" {
			_, _ = w.Write([]byte(`{"code":0,"tenant_access_token":"tenant-token","expire":7200}`))
			return
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode %s: %v", r.URL.Path, err)
		}
		mu.Lock()
		calls = append(calls, call{method: r.Method, path: r.URL.Path, body: body})
		mu.Unlock()
		switch r.URL.Path {
		case "/open-apis/cardkit/v1/cards":
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]string{"card_id": "card-thread"}})
		case "/open-apis/im/v1/messages/root-message/reply":
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]string{"message_id": "reply-message"}})
		case "/open-apis/cardkit/v1/cards/card-thread/elements/answer_text/content",
			"/open-apis/cardkit/v1/cards/card-thread/elements",
			"/open-apis/cardkit/v1/cards/card-thread/elements/activity_content",
			"/open-apis/cardkit/v1/cards/card-thread/settings":
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 0})
		default:
			t.Errorf("unexpected lark API call: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	b := &Bridge{client: lark.NewClient("app-id", "app-secret", lark.WithOpenBaseUrl(server.URL), lark.WithOAuthBaseUrl(server.URL))}
	backend := newBindingReplyCardBackend(b, &LarkChat{RootMsgID: "root-message"})
	turnBackend := backend.(turnActivityCardBackend)
	handle, err := backend.Create(context.Background(), "hello")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	updatedText := "hello \"CardKit\"\n\n- first line\n- second line"
	if err := backend.Update(context.Background(), handle, updatedText); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if err := turnBackend.UpdateActivity(context.Background(), handle, renderTurnActivity(toolActivityCardMeta{
		Status:   "running",
		Activity: &agentui.ActivityView{Status: "running", CurrentStep: "检查 CardKit"},
	})); err != nil {
		t.Fatalf("insert activity: %v", err)
	}
	if err := turnBackend.UpdateActivity(context.Background(), handle, renderTurnActivity(toolActivityCardMeta{
		Status:   "done",
		Activity: &agentui.ActivityView{Status: "done", CurrentStep: "检查完成"},
	})); err != nil {
		t.Fatalf("replace activity: %v", err)
	}
	if err := backend.Finish(context.Background(), handle, updatedText); err != nil {
		t.Fatalf("Finish: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(calls) != 6 {
		t.Fatalf("calls = %+v, want 6", calls)
	}
	wantPaths := []string{
		"/open-apis/cardkit/v1/cards",
		"/open-apis/im/v1/messages/root-message/reply",
		"/open-apis/cardkit/v1/cards/card-thread/elements/answer_text/content",
		"/open-apis/cardkit/v1/cards/card-thread/elements",
		"/open-apis/cardkit/v1/cards/card-thread/elements/activity_content",
		"/open-apis/cardkit/v1/cards/card-thread/settings",
	}
	for i, want := range wantPaths {
		if calls[i].path != want {
			t.Fatalf("call %d path = %q, want %q", i, calls[i].path, want)
		}
	}
	if calls[1].body["reply_in_thread"] != true || calls[1].body["msg_type"] != "interactive" {
		t.Fatalf("thread message body = %+v", calls[1].body)
	}
	if calls[1].body["uuid"] == "" {
		t.Fatalf("thread message missing UUID: %+v", calls[1].body)
	}
	content, _ := calls[1].body["content"].(string)
	if content != renderCardKitMessageContent("card-thread") {
		t.Fatalf("thread content = %q", content)
	}
	if calls[2].body["sequence"] != float64(1) {
		t.Fatalf("update body = %+v", calls[2].body)
	}
	innerContent, _ := calls[2].body["content"].(string)
	if innerContent != renderAgentResultBody(updatedText) {
		t.Fatalf("update content = %q, want final-result Markdown %q", innerContent, renderAgentResultBody(updatedText))
	}
	if calls[3].body["sequence"] != float64(2) || calls[3].body["type"] != "insert_before" || calls[3].body["target_element_id"] != cardKitAnswerHeadingID {
		t.Fatalf("activity insert body = %+v", calls[3].body)
	}
	insertElements, _ := calls[3].body["elements"].(string)
	if !strings.Contains(insertElements, `"element_id":"activity_content"`) || !strings.Contains(insertElements, `"element_id":"answer_divider"`) || strings.Contains(insertElements, `\\"element_id\\"`) {
		t.Fatalf("activity insert elements encoded incorrectly: %q", insertElements)
	}
	if calls[4].body["sequence"] != float64(3) {
		t.Fatalf("activity replace body = %+v", calls[4].body)
	}
	replacement, _ := calls[4].body["element"].(string)
	if !strings.Contains(replacement, `"element_id":"activity_content"`) || !strings.Contains(replacement, "检查完成") {
		t.Fatalf("activity replacement = %q", replacement)
	}
	if calls[5].body["sequence"] != float64(4) {
		t.Fatalf("finish body = %+v", calls[5].body)
	}
	settings, _ := calls[5].body["settings"].(string)
	if !strings.Contains(settings, `"streaming_mode":false`) {
		t.Fatalf("finish settings = %q", settings)
	}
}

func TestBindingReplyCardBackendRoutesSingletonCardKitToLatestTarget(t *testing.T) {
	tests := []struct {
		name        string
		replyKind   string
		openID      string
		chatID      string
		wantQuery   string
		wantReceive string
	}{
		{name: "dm", replyKind: botSessionReplyDM, openID: "owner-open", wantQuery: "receive_id_type=open_id", wantReceive: "owner-open"},
		{name: "group", replyKind: botSessionReplyGroup, chatID: "workspace-chat", wantQuery: "receive_id_type=chat_id", wantReceive: "workspace-chat"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var messageQuery string
			var messageBody map[string]any
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch r.URL.Path {
				case "/open-apis/auth/v3/tenant_access_token/internal":
					_, _ = w.Write([]byte(`{"code":0,"tenant_access_token":"tenant-token","expire":7200}`))
				case "/open-apis/cardkit/v1/cards":
					_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]string{"card_id": "card-singleton"}})
				case "/open-apis/im/v1/messages":
					messageQuery = r.URL.RawQuery
					if err := json.NewDecoder(r.Body).Decode(&messageBody); err != nil {
						t.Errorf("decode message: %v", err)
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]string{"message_id": "singleton-message"}})
				default:
					t.Errorf("unexpected lark API call: %s %s", r.Method, r.URL.Path)
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer server.Close()

			db, err := openBridgeDB(filepath.Join(t.TempDir(), "bridge.db"))
			if err != nil {
				t.Fatalf("openBridgeDB: %v", err)
			}
			defer db.Close()
			if err := db.SetBotSession(BotSession{
				BridgeId:        "feishu:test",
				Enabled:         true,
				TargetID:        "runtime-1",
				SessionName:     "bot-main",
				AgentType:       "codex",
				LastReplyKind:   tt.replyKind,
				LastReplyOpenID: tt.openID,
				LastReplyChatID: tt.chatID,
			}); err != nil {
				t.Fatalf("SetBotSession: %v", err)
			}
			b := &Bridge{
				bridgeID: "feishu:test",
				db:       db,
				client:   lark.NewClient("app-id", "app-secret", lark.WithOpenBaseUrl(server.URL), lark.WithOAuthBaseUrl(server.URL)),
			}
			backend := newBindingReplyCardBackend(b, &LarkChat{RootMsgID: singletonStreamKey("feishu:test")})
			if _, err := backend.Create(context.Background(), "answer"); err != nil {
				t.Fatalf("Create: %v", err)
			}
			if messageQuery != tt.wantQuery {
				t.Fatalf("query = %q, want %q", messageQuery, tt.wantQuery)
			}
			if messageBody["receive_id"] != tt.wantReceive || messageBody["msg_type"] != "interactive" {
				t.Fatalf("message body = %+v", messageBody)
			}
			if messageBody["uuid"] == "" {
				t.Fatalf("message body missing UUID: %+v", messageBody)
			}
			if messageBody["content"] != renderCardKitMessageContent("card-singleton") {
				t.Fatalf("message content = %q", messageBody["content"])
			}
		})
	}
}

// Regression: the queue line must reach a card that already shows activity.
// Existing CardKit cards only ever replace the activity content element and
// skip unchanged JSON, so a queue line rendered outside that element (or a
// queue-only change) never reached Feishu.
func TestCardKitQueueLineReachesExistingActivityCard(t *testing.T) {
	api := &fakeCardKitReplyAPI{cardID: "card-q"}
	backend := &bindingReplyCardBackend{
		cardKit:  api,
		sendCard: func(context.Context, string, string) (string, error) { return "message-q", nil },
	}
	handle, err := backend.Create(context.Background(), "回答中")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	view := &agentui.ActivityView{Status: "running", CurrentStep: "sleep 50"}
	if err := backend.UpdateActivity(context.Background(), handle, renderTurnActivity(toolActivityCardMeta{Status: "running", Activity: view})); err != nil {
		t.Fatalf("first activity: %v", err)
	}
	if err := backend.UpdateActivity(context.Background(), handle, renderTurnActivity(toolActivityCardMeta{Status: "running", Activity: view, QueuedInputs: 2})); err != nil {
		t.Fatalf("queued activity: %v", err)
	}
	if len(api.replaces) != 1 {
		t.Fatalf("replaces = %d, want the queue change to replace the activity element", len(api.replaces))
	}
	if got := api.replaces[0].Element; !strings.Contains(got, "queue_notice") || !strings.Contains(got, "已排队 2 条消息") {
		t.Fatalf("replaced activity lacks queue line: %s", got)
	}
	if err := backend.UpdateActivity(context.Background(), handle, renderTurnActivity(toolActivityCardMeta{Status: "done", Activity: &agentui.ActivityView{Status: "done"}, QueuedInputs: 2})); err != nil {
		t.Fatalf("done activity: %v", err)
	}
	if got := api.replaces[len(api.replaces)-1].Element; strings.Contains(got, "queue_notice") {
		t.Fatalf("finished activity still shows queue line: %s", got)
	}
}
