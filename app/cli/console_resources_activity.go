package cli

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/amuxos/amux/internal/agentstream"
	"github.com/amuxos/amux/internal/gateway"
	pb "github.com/amuxos/amux/internal/transport/pbgen"
)

const (
	consoleActivityMaxSessions   = 16
	consoleActivityConcurrency   = 4
	consoleActivityMaxEvents     = 8192
	consoleActivityMaxBytes      = 8 << 20
	consoleActivityObjectTimeout = 3 * time.Second
	consoleActivityTotalTimeout  = 6 * time.Second
)

// Unknown activity has no turn/permission fields at all. In particular a
// lifecycle-running Session does not establish an active Turn. The embedded
// Gateway entry keeps the established flat Session JSON shape.
type consoleSessionActivity struct {
	*gateway.SessionEntry
	*consoleActivityFacts
	ActivityStatus string `json:"activity_status"`
}
type consoleActivityFacts struct {
	TurnState            string               `json:"turn_state"`
	ActiveTurn           bool                 `json:"active_turn"`
	PendingPermissions   int                  `json:"pending_permissions"`
	PendingPermissionIDs []string             `json:"pending_permission_ids"`
	LastTurn             *agentstream.TurnRef `json:"last_turn,omitempty"`
}
type consoleActivityReplay struct {
	sessionID   string
	cursor      agentstream.Cursor
	turn        *agentstream.TurnRef
	active      bool
	state       string
	permissions map[string]struct{}
}

func (r *consoleActivityReplay) apply(event agentstream.Event) bool {
	if r.sessionID != "" && event.SessionID != r.sessionID {
		return false
	}
	if r.cursor.Epoch != "" && event.Cursor.Epoch != r.cursor.Epoch {
		return false
	}
	if event.Cursor.Seq != r.cursor.Seq+1 {
		return false
	}
	r.cursor = event.Cursor
	switch body := event.Body.(type) {
	case agentstream.TurnStarted:
		if event.Turn == nil || r.active || r.turn != nil && event.Turn.Seq <= r.turn.Seq {
			return false
		}
		turn := *event.Turn
		r.turn = &turn
		r.active = true
		r.state = "running"
		r.permissions = map[string]struct{}{}
	case agentstream.TurnFinished:
		if !r.matchesActive(event.Turn) {
			return false
		}
		r.active = false
		r.permissions = map[string]struct{}{}
		switch body.Terminal.Outcome {
		case agentstream.OutcomeSuccess:
			r.state = "succeeded"
		case agentstream.OutcomeError:
			r.state = "failed"
		case agentstream.OutcomeCancelled:
			r.state = "cancelled"
		case agentstream.OutcomeInterrupted:
			r.state = "interrupted"
		default:
			return false
		}
	case agentstream.PermissionRequested:
		if !r.matchesActive(event.Turn) || body.Request.ID == "" {
			return false
		}
		r.permissions[body.Request.ID] = struct{}{}
	case agentstream.PermissionResolved:
		if !r.matchesActive(event.Turn) {
			return false
		}
		delete(r.permissions, body.RequestID)
	default:
		if event.Turn != nil && !r.matchesActive(event.Turn) {
			return false
		}
	}
	return true
}
func (r *consoleActivityReplay) matchesActive(turn *agentstream.TurnRef) bool {
	return r.active && r.turn != nil && turn != nil && *r.turn == *turn
}
func (r *consoleActivityReplay) finish(head agentstream.Cursor) (*consoleActivityFacts, bool) {
	if head.Epoch == "" || head.Seq != r.cursor.Seq || r.cursor.Epoch != "" && head.Epoch != r.cursor.Epoch {
		return nil, false
	}
	state := r.state
	if state == "" {
		state = "idle"
	}
	if r.active && len(r.permissions) > 0 {
		state = "waiting_permission"
	}
	ids := make([]string, 0, len(r.permissions))
	for id := range r.permissions {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return &consoleActivityFacts{TurnState: state, ActiveTurn: r.active, PendingPermissions: len(ids), PendingPermissionIDs: ids, LastTurn: r.turn}, true
}

func (b *consoleBackend) consoleReadActivity(ctx context.Context, entry *gateway.SessionEntry) *consoleActivityFacts {
	ctx, cancel := context.WithTimeout(ctx, consoleActivityObjectTimeout)
	defer cancel()
	stream, err := b.hub.Attach(ctx, &pb.AmuxdAttachRequest{TargetId: entry.TargetID, Name: entry.GetName(), TimelineMode: pb.AttachRequest_HISTORY})
	if err != nil || stream == nil {
		return nil
	}
	replay := consoleActivityReplay{sessionID: entry.GetId(), permissions: map[string]struct{}{}}
	totalBytes := 0
	for count := 0; count < consoleActivityMaxEvents; count++ {
		if ctx.Err() != nil {
			return nil
		}
		event, err := stream.Recv()
		if err != nil || event == nil {
			return nil
		}
		if len(event.GetCanonicalJson()) > 0 {
			totalBytes += len(event.GetCanonicalJson())
			if totalBytes > consoleActivityMaxBytes {
				return nil
			}
			canonical, err := agentstream.UnmarshalEvent(event.GetCanonicalJson())
			if err != nil || !replay.apply(canonical) {
				return nil
			}
		} else if !event.GetCaughtUp() && event.GetEventType() != "goal_changed" {
			// HISTORY has only canonical events and an optional typed goal snapshot.
			// Legacy text/state/permission transport fields cannot prove activity.
			return nil
		}
		if event.GetCaughtUp() {
			head, err := agentstream.DecodeCursor(event.GetCursor())
			if err != nil {
				return nil
			}
			facts, ok := replay.finish(head)
			if !ok {
				return nil
			}
			return facts
		}
	}
	return nil
}
func (b *consoleBackend) consoleEnrichActivity(ctx context.Context, entries []*gateway.SessionEntry) []*consoleSessionActivity {
	out := make([]*consoleSessionActivity, len(entries))
	candidates := make([]int, 0, len(entries))
	for index, entry := range entries {
		out[index] = &consoleSessionActivity{SessionEntry: entry, ActivityStatus: "unknown"}
		if entry != nil && entry.GetState() != "archived" {
			candidates = append(candidates, index)
		}
	}
	// Bound cost independently of fleet size, prioritizing recently active
	// Sessions. Uninspected objects explicitly remain unknown.
	sort.SliceStable(candidates, func(i, j int) bool {
		left, right := entries[candidates[i]], entries[candidates[j]]
		if left.GetLastActivityAt() != right.GetLastActivityAt() {
			return left.GetLastActivityAt() > right.GetLastActivityAt()
		}
		return left.GetUpdatedAt() > right.GetUpdatedAt()
	})
	if len(candidates) > consoleActivityMaxSessions {
		candidates = candidates[:consoleActivityMaxSessions]
	}
	if b.hub == nil || len(candidates) == 0 {
		return out
	}
	ctx, cancel := context.WithTimeout(ctx, consoleActivityTotalTimeout)
	defer cancel()
	jobs := make(chan int, len(candidates))
	for _, index := range candidates {
		jobs <- index
	}
	close(jobs)
	var workers sync.WaitGroup
	for worker := 0; worker < consoleActivityConcurrency && worker < len(candidates); worker++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for index := range jobs {
				if ctx.Err() != nil {
					return
				}
				if facts := b.consoleReadActivity(ctx, entries[index]); facts != nil {
					out[index].consoleActivityFacts = facts
					out[index].ActivityStatus = "known"
				}
			}
		}()
	}
	workers.Wait()
	return out
}
