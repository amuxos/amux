package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/amuxos/amux/internal/feishumsg"
	"github.com/amuxos/amux/internal/principal"
	"github.com/amuxos/amux/internal/schedule"
	"github.com/amuxos/amux/internal/sessioncredential"
	pb "github.com/amuxos/amux/internal/transport/pbgen"
	"github.com/google/uuid"
)

type personalScheduleTestHub struct {
	Hub
	writes []feishumsg.PersonalScheduleCard
}

func (h *personalScheduleTestHub) Route(_ context.Context, _, method string, payload any) (*pb.TunnelMsg, error) {
	if method != feishumsg.PersonalScheduleCardRoute {
		return nil, errors.New("unexpected route")
	}
	card := payload.(feishumsg.PersonalScheduleCard)
	h.writes = append(h.writes, card)
	raw, err := json.Marshal(feishumsg.PersonalResult{Result: feishumsg.Result{
		ChatID: "oc_" + card.Account.Profile, MessageID: "om_" + card.Account.Profile,
	}})
	return &pb.TunnelMsg{Payload: raw}, err
}

func TestPersonalScheduleOwnerAndCardScope(t *testing.T) {
	ctx := context.Background()
	db, err := openBridgeDB(filepath.Join(t.TempDir(), "bridge.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.db.Close()
	store, err := schedule.OpenStore(filepath.Join(t.TempDir(), "schedule.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	control := &deliveryAuthScheduleControl{store: store}
	hub := &personalScheduleTestHub{}
	b := &Bridge{db: db, hub: hub, schedules: control, secrets: &Secrets{AppId: "shared"}}
	owners := map[string]string{"a": uuid.NewString(), "b": uuid.NewString()}
	b.SetFeishuPrincipalResolver(func(open string) (string, error) { return owners[open], nil })

	tasks := map[string]int64{}
	for _, name := range []string{"a", "b"} {
		principalID := owners[name]
		task, err := store.Create(schedule.Task{Name: "fixture-" + name, Spec: "every 1m", Enabled: true})
		if err != nil {
			t.Fatal(err)
		}
		tasks[name] = task.ID
		if err := store.BindCreator(task.ID, schedule.Creator{PrincipalID: principalID, BotID: b.currentBridgeId(), OpenID: name, Source: "turn:" + name}); err != nil {
			t.Fatal(err)
		}
		b.rememberPersonalScheduleLogin(&pendingCredentialAdmission{
			openID: name, target: "runtime", actor: sessioncredential.FeishuActor{TenantKey: "tenant", UserID: name},
			identity: principal.ExecutionIdentity{
				IdentityMode: principal.IdentityModeIdentified, Source: principal.SourceFeishu,
				IsolationBackend: principal.BackendLinuxDAC, OriginActorPrincipalID: principalID, EffectivePrincipalID: principalID,
			},
		})
	}

	bindings, err := b.personalScheduleBindings(ctx)
	if err != nil || len(bindings) != 2 {
		t.Fatalf("bindings = %v; error = %v", len(bindings), err)
	}
	for _, binding := range bindings {
		name := binding.OpenID
		other := "a"
		if name == "a" {
			other = "b"
		}
		base := feishumsg.PersonalScheduleEvent{
			EventID: "message:" + name, MessageID: "om_command_" + name, ChatID: "oc_" + name,
			Account: feishumsg.PersonalScheduleAccount{OpenID: "ou_personal_" + name, Profile: name},
		}
		if err := b.handlePersonalScheduleEvent(ctx, binding, base); err != nil {
			t.Fatal(err)
		}
		if card := string(hub.writes[len(hub.writes)-1].Card); !strings.Contains(card, "fixture-"+name) || strings.Contains(card, "fixture-"+other) {
			t.Fatal("personal list has wrong scopes")
		}
		click := base
		click.EventID, click.MessageID, click.Token = "action:"+name, "om_"+name, "callback-token"
		click.Action = map[string]any{managementActionKey: "schedule.pause", "schedule_id": fmt.Sprint(tasks[name])}
		forwarded := click
		forwarded.ChatID = "oc_other"
		before := len(hub.writes)
		if err := b.handlePersonalScheduleEvent(ctx, binding, forwarded); err != nil || len(hub.writes) != before {
			t.Fatalf("forwarded card produced a write: %v", err)
		}
		foreign := click
		foreign.EventID += ":other"
		foreign.Action = map[string]any{managementActionKey: "schedule.pause", "schedule_id": fmt.Sprint(tasks[other])}
		if err := b.handlePersonalScheduleEvent(ctx, binding, foreign); err != nil || len(hub.writes) != before+1 || hub.writes[before].Text == "" {
			t.Fatalf("foreign schedule was not rejected with a personal message: %v", err)
		}
		for range 2 {
			if err := b.handlePersonalScheduleEvent(ctx, binding, click); err != nil {
				t.Fatal(err)
			}
		}
		if len(hub.writes) != before+2 || hub.writes[before+1].Token != click.Token {
			t.Fatal("own action did not update the same card exactly once")
		}
	}
	if len(control.acted) != 2 {
		t.Fatalf("schedule mutations = %v", control.acted)
	}
}
