package tunnel

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/amuxos/amux/internal/adapter"
	"github.com/amuxos/amux/internal/amuxhome"
	"github.com/amuxos/amux/internal/feishumsg"
	"github.com/amuxos/amux/internal/principal"
	"github.com/amuxos/amux/internal/registry"
	"github.com/amuxos/amux/internal/session"
	"github.com/amuxos/amux/internal/sessioncredential"
	"github.com/amuxos/amux/internal/transport"
	pb "github.com/amuxos/amux/internal/transport/pbgen"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
)

// heartbeatInterval is how often the runtime reports its dynamic load to the hub.
const heartbeatInterval = 10 * time.Second

// reservationSweepInterval is how often the runtime reclaims uncommitted
// admission reservations whose Commit never arrived (e.g. the scheduler crashed
// between Reserve and Commit, or a routed TaskSubmit failed before Release). It is
// the design's "预留超时未 commit 由 amuxlet 自动释放" leak backstop; committed
// slots are never expired here (only an explicit Release frees those).
const reservationSweepInterval = 15 * time.Second

// errTaskUnavailable is returned over the tunnel when a runtime without an
// in-process task control plane is asked to serve an AmuxTask method.
const errTaskUnavailable = "task service not available on this runtime"

// Client connects a runtime to the hub via the reverse tunnel.
type Client struct {
	hubAddr    string
	targetID   string
	targetName string
	version    string
	mgr        *session.Manager
	localSrv   pb.AmuxletServer
	// taskSrv answers AmuxTask methods routed over the tunnel (the amuxd task
	// router -> this runtime's in-process control plane). nil leaves task routing
	// unavailable (the runtime still serves sessions). The runner stays in-process
	// here on purpose: owner enforcement holds because the runner drives the child
	// *session.Session directly, never via a child-session RPC exposed to amuxd.
	taskSrv pb.AmuxTaskServer

	// caps/token are the Phase 2 registration capabilities and enrollment token.
	// nil caps registers in the legacy minimal mode (id/name only).
	caps  *registry.Capabilities
	token string
	// adm is the runtime's real-time two-level admission, built from caps. It
	// answers the scheduler's Reserve/Commit/Release over the tunnel. nil when no
	// capabilities are configured (legacy mode: admission is not offered).
	adm *registry.Admission

	cancelsMu sync.Mutex
	cancels   map[string]context.CancelFunc
}

func NewClient(hubAddr, targetID, targetName string, mgr *session.Manager, localSrv pb.AmuxletServer) *Client {
	return &Client{
		hubAddr:    hubAddr,
		targetID:   targetID,
		targetName: targetName,
		mgr:        mgr,
		localSrv:   localSrv,
		cancels:    make(map[string]context.CancelFunc),
	}
}

// SetCapabilities sets the static capabilities and enrollment token reported to
// the hub on register. It also builds the runtime admission (two-level quota)
// from the capabilities. Call before Run.
func (c *Client) SetCapabilities(caps *registry.Capabilities, token string) {
	c.caps = caps
	c.token = token
	if caps != nil {
		c.adm = registry.NewAdmissionFromCaps(*caps, 0)
	}
}

// SetVersion sets the runtime binary version reported to the hub.
func (c *Client) SetVersion(version string) { c.version = version }

// SetTaskServer installs the in-process AmuxTask control plane that answers task
// methods routed over the tunnel from the amuxd task router. Call before Run.
func (c *Client) SetTaskServer(s pb.AmuxTaskServer) { c.taskSrv = s }

// Run connects to the hub and maintains the tunnel, reconnecting on failure.
// Blocks until ctx is cancelled.
func (c *Client) Run(ctx context.Context) {
	// The admission outlives individual tunnel connections (it is built once in
	// SetCapabilities), so sweep uncommitted reservations for the Client's whole
	// lifetime, independent of reconnects.
	if c.adm != nil {
		go c.sweepReservations(ctx)
	}
	const (
		initialBackoff = time.Second
		maxBackoff     = 30 * time.Second
		stableConnMin  = 5 * time.Second
	)
	backoff := initialBackoff
	for {
		started := time.Now()
		err := c.connect(ctx)
		if time.Since(started) >= stableConnMin {
			backoff = initialBackoff
		}
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			fmt.Printf("tunnel: disconnected (%v), retrying in %s\n", err, backoff)
		}
		select {
		case <-time.After(backoff):
			backoff *= 2
			if backoff > maxBackoff {
				backoff = maxBackoff
			}
		case <-ctx.Done():
			return
		}
	}
}

func (c *Client) connect(ctx context.Context) error {
	dialCtx, dialCancel := context.WithTimeout(ctx, 5*time.Second)
	opts := append([]grpc.DialOption{
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithBlock(),
	}, transport.GRPCClientDialOptions()...)
	conn, err := grpc.DialContext(dialCtx, c.hubAddr, opts...)
	dialCancel()
	if err != nil {
		return fmt.Errorf("%s", dialHubErrorMessage(err))
	}
	defer conn.Close()

	client := pb.NewAmuxdClient(conn)
	streamCtx := ctx
	if c.token != "" {
		streamCtx = metadata.AppendToOutgoingContext(ctx, "x-amux-hub-token", c.token)
	}
	stream, err := client.Tunnel(streamCtx)
	if err != nil {
		return fmt.Errorf("open tunnel: %w", err)
	}

	// connCtx is cancelled when this connection ends, which terminates all
	// per-connection goroutines and prevents them from writing to the now-closed
	// sendCh on the next reconnect.
	connCtx, connCancel := context.WithCancel(ctx)
	defer connCancel()

	// Serialize all writes to the stream through a channel.
	sendCh := make(chan *pb.TunnelMsg, 64)
	sendDone := make(chan struct{})
	go func() {
		for {
			select {
			case msg := <-sendCh:
				if err := stream.Send(msg); err != nil {
					return
				}
			case <-sendDone:
				return
			}
		}
	}()
	sendFn := func(msg *pb.TunnelMsg) error {
		select {
		case <-sendDone:
			return connCtx.Err()
		case <-connCtx.Done():
			return connCtx.Err()
		default:
		}
		select {
		case sendCh <- msg:
			return nil
		case <-sendDone:
			return connCtx.Err()
		case <-connCtx.Done():
			return connCtx.Err()
		}
	}
	defer close(sendDone)

	// Register this runtime.
	registerPayload, err := c.registerPayload()
	if err != nil {
		return err
	}
	if err := sendFn(&pb.TunnelMsg{Kind: "register", Payload: registerPayload}); err != nil {
		return err
	}

	// Push current session list.
	c.pushSessions(sendFn)

	// Watch for session changes and push updates.
	go c.watchSessions(connCtx, sendFn)

	// Report dynamic load to the hub on a fixed cadence (advisory ranking input).
	go c.heartbeat(connCtx, sendFn)

	fmt.Println(tunnelConnectedMessage(c.hubAddr, c.targetID))

	// Read and dispatch incoming requests.
	for {
		msg, err := stream.Recv()
		if err != nil {
			return err
		}
		switch msg.Kind {
		case "req":
			go c.handleRequest(connCtx, sendFn, msg)
		case "cancel":
			c.cancelsMu.Lock()
			if cancel, ok := c.cancels[msg.RequestId]; ok {
				cancel()
				delete(c.cancels, msg.RequestId)
			}
			c.cancelsMu.Unlock()
		}
	}
}

func (c *Client) registerPayload() (json.RawMessage, error) {
	caps := c.caps
	if caps == nil {
		caps = runtimeCapabilities()
	}
	payload, err := json.Marshal(&pb.RegisterPayload{
		TargetId:     c.targetID,
		TargetName:   c.targetName,
		Version:      c.version,
		Token:        c.token,
		Capabilities: registryCapabilitiesToPB(caps),
	})
	if err != nil {
		return nil, fmt.Errorf("encode register payload: %w", err)
	}
	return payload, nil
}

func dialHubErrorMessage(err error) string {
	return fmt.Sprintf("dial hub: %v", err)
}

func tunnelConnectedMessage(addr, targetID string) string {
	return fmt.Sprintf("tunnel: connected to hub at %s (target=%s)", addr, targetID)
}

func (c *Client) pushSessions(sendFn func(*pb.TunnelMsg) error) {
	sessions := c.mgr.List()
	pbSessions := make([]*pb.Session, len(sessions))
	for i, s := range sessions {
		pbSessions[i] = tunnelSessionSummary(s)
	}
	payload, _ := json.Marshal(&pb.SessionsPayload{Sessions: pbSessions})
	_ = sendFn(&pb.TunnelMsg{Kind: "sessions", Payload: payload})
}

func tunnelSessionSummary(s *session.Session) *pb.Session {
	snap := s.Snapshot()
	caps := capabilitiesForAgent(s.AgentType)
	return &pb.Session{
		Id:                    snap.ID,
		Name:                  snap.Name,
		AgentProfile:          snap.AgentProfile,
		EffectiveModel:        snap.EffectiveModel,
		InputModalities:       append([]string(nil), snap.InputModalities...),
		InputCapabilitySource: snap.InputCapabilitySource,
		AgentType:             snap.AgentType,
		CwdSwitch:             string(caps.CwdSwitch),
		State:                 string(snap.State),
		Pid:                   int32(snap.PID),
		Cwd:                   snap.Cwd,
		WorkspaceSource:       snap.WorkspaceSource,
		WorkspaceScope:        snap.WorkspaceScope,
		OwnerTaskId:           snap.OwnerTaskId,
		AgentInstanceId:       snap.AgentInstanceID,
		ExecutionIdentity:     principal.ToProtoIdentity(snap.ExecutionIdentity),
		LaunchId:              snap.LaunchID,
		CreatedAt:             snap.CreatedAt.Unix(),
		UpdatedAt:             snap.UpdatedAt.Unix(),
		Protocol:              string(caps.Protocol),
		Acp:                   caps.Acp,
		Interrupt:             caps.Interrupt,
		IdleTimeoutSeconds:    int64(snap.IdleTimeoutSeconds),
		LastActivityAt:        sessionUnix(snap.LastActivityAt),
		LastSleepReason:       snap.LastSleepReason,
		AutoSleepEligible:     s.AutoSleepEligible(time.Now()),
	}
}

func capabilitiesForAgent(agentType string) adapter.Capabilities {
	a, err := adapter.Get(agentType)
	if err != nil {
		return adapter.Capabilities{}
	}
	return a.Capabilities()
}

func sessionUnix(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.Unix()
}

func runtimeCapabilities() *registry.Capabilities {
	workspaceRoots := []string{filepath.Join(amuxhome.Dir(), "workspaces")}
	return &registry.Capabilities{
		AgentTypes:     append([]string(nil), adapter.Names()...),
		Features:       []string{"acp", "structured_provider", "interrupt", "resume"},
		RuntimeTools:   availableCommands("git", "go", "node", "npm", "python3"),
		WorkspaceRoots: workspaceRoots,
		Shells:         availableShells(),
		PathStyle:      "posix",
		NetworkProfile: "default",
		Labels:         map[string]string{"target": "local"},
	}
}

func availableCommands(names ...string) []string {
	var out []string
	for _, name := range names {
		if _, err := exec.LookPath(name); err == nil {
			out = append(out, name)
		}
	}
	return out
}

func availableShells() []string {
	values := []string{os.Getenv("SHELL"), "/bin/zsh", "/bin/bash", "/bin/sh"}
	seen := map[string]struct{}{}
	var out []string
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		if _, err := os.Stat(value); err != nil {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

func (c *Client) watchSessions(ctx context.Context, sendFn func(*pb.TunnelMsg) error) {
	for {
		ch := c.mgr.WatchSessions()
		select {
		case <-ch:
			c.pushSessions(sendFn)
		case <-ctx.Done():
			return
		}
	}
}

// heartbeat reports the runtime's dynamic load to the hub until ctx is done.
// It sends once immediately so the registry has load before the first tick.
func (c *Client) heartbeat(ctx context.Context, sendFn func(*pb.TunnelMsg) error) {
	send := func() {
		payload, _ := json.Marshal(&pb.HeartbeatPayload{Load: registryLoadToPB(c.currentLoad())})
		_ = sendFn(&pb.TunnelMsg{Kind: "heartbeat", Payload: payload})
	}
	send()
	ticker := time.NewTicker(heartbeatInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			send()
		case <-ctx.Done():
			return
		}
	}
}

// sweepReservations periodically reclaims uncommitted reservations past their TTL
// until ctx is done. Committed slots are untouched (freed only by Release).
func (c *Client) sweepReservations(ctx context.Context) {
	ticker := time.NewTicker(reservationSweepInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			c.adm.ExpireReservations()
		case <-ctx.Done():
			return
		}
	}
}

// currentLoad derives a load snapshot from the live session list and the
// admission. Non-archived sessions are the authoritative running count (a live
// agent occupies a slot whether actively running or idle); the admission
// contributes in-flight reserved slots not yet turned into sessions.
func (c *Client) currentLoad() registry.Load {
	sessions := c.mgr.List()
	load := registry.Load{PerAgentRunning: map[string]int{}, PerAgentReserved: map[string]int{}}
	for _, s := range sessions {
		if s.State == session.StateArchived {
			continue
		}
		load.CurrentSessions++
		load.PerAgentRunning[s.AgentType]++
	}
	if c.adm != nil {
		snap := c.adm.Snapshot()
		load.ReservedSlots = snap.ReservedSlots
		for at, n := range snap.PerAgentReserved {
			load.PerAgentReserved[at] = n
		}
	}
	return load
}

func (c *Client) handleRequest(ctx context.Context, sendFn func(*pb.TunnelMsg) error, req *pb.TunnelMsg) {
	reqCtx, reqCancel := context.WithCancel(ctx)
	c.cancelsMu.Lock()
	c.cancels[req.RequestId] = reqCancel
	c.cancelsMu.Unlock()
	defer func() {
		c.cancelsMu.Lock()
		delete(c.cancels, req.RequestId)
		c.cancelsMu.Unlock()
		reqCancel()
	}()

	sendResp := func(payload interface{}, errMsg string) {
		var raw json.RawMessage
		if payload != nil {
			raw, _ = marshalTunnelPayload(payload)
		}
		_ = sendFn(&pb.TunnelMsg{Kind: "resp", RequestId: req.RequestId, Payload: raw, Error: errMsg})
	}

	switch req.Method {
	case "ListNativeModels":
		var r pb.ListNativeModelsRequest
		if err := unmarshalTunnelPayload(req.Payload, &r); err != nil {
			sendResp(nil, err.Error())
			return
		}
		resp, err := c.localSrv.ListNativeModels(reqCtx, &r)
		if err != nil {
			sendResp(nil, err.Error())
			return
		}
		sendResp(resp, "")
	case sessioncredential.Route:
		handler, ok := c.localSrv.(interface {
			IsolatedSessionCredential(context.Context, sessioncredential.Request) sessioncredential.Result
		})
		if !ok {
			sendResp(nil, "isolated session credential route unavailable")
			return
		}
		var input sessioncredential.Request
		if err := unmarshalTunnelPayload(req.Payload, &input); err != nil {
			sendResp(nil, "invalid isolated session credential request")
			return
		}
		sendResp(handler.IsolatedSessionCredential(reqCtx, input), "")
	case feishumsg.PersonalRoute:
		handler, ok := c.localSrv.(interface {
			SendPersonalFeishuMessage(context.Context, feishumsg.PersonalRequest) (feishumsg.PersonalResult, error)
		})
		if !ok {
			sendResp(nil, feishumsg.Copy("personal_unavailable"))
			return
		}
		var input feishumsg.PersonalRequest
		if err := unmarshalTunnelPayload(req.Payload, &input); err != nil {
			sendResp(nil, feishumsg.Copy("invalid", err))
			return
		}
		result, err := handler.SendPersonalFeishuMessage(reqCtx, input)
		if err != nil {
			sendResp(nil, err.Error())
			return
		}
		sendResp(result, "")
	case feishumsg.PersonalScheduleEventsRoute:
		end := &pb.TunnelMsg{Kind: "end", RequestId: req.RequestId}
		handler, ok := c.localSrv.(interface {
			ObservePersonalSchedule(context.Context, feishumsg.PersonalScheduleIdentity, func(feishumsg.PersonalScheduleEvent) error) error
		})
		var input feishumsg.PersonalScheduleIdentity
		if !ok || unmarshalTunnelPayload(req.Payload, &input) != nil {
			end.Error = feishumsg.Copy("personal_unavailable")
		} else if err := handler.ObservePersonalSchedule(reqCtx, input, func(event feishumsg.PersonalScheduleEvent) error {
			raw, err := json.Marshal(event)
			if err != nil {
				return err
			}
			return sendFn(&pb.TunnelMsg{Kind: "data", RequestId: req.RequestId, Payload: raw})
		}); err != nil {
			end.Error = err.Error()
		}
		_ = sendFn(end)
	case feishumsg.PersonalScheduleCardRoute:
		handler, ok := c.localSrv.(interface {
			WritePersonalScheduleCard(context.Context, feishumsg.PersonalScheduleCard) (feishumsg.PersonalResult, error)
		})
		var input feishumsg.PersonalScheduleCard
		if !ok || unmarshalTunnelPayload(req.Payload, &input) != nil {
			sendResp(nil, feishumsg.Copy("personal_unavailable"))
			return
		}
		result, err := handler.WritePersonalScheduleCard(reqCtx, input)
		if err != nil {
			sendResp(nil, err.Error())
			return
		}
		sendResp(result, "")
	case "Run":
		var r pb.RunRequest
		if err := unmarshalTunnelPayload(req.Payload, &r); err != nil {
			sendResp(nil, err.Error())
			return
		}
		resp, err := c.localSrv.Run(reqCtx, &r)
		if err != nil {
			sendResp(nil, err.Error())
			return
		}
		sendResp(resp, "")

	case "List":
		resp, err := c.localSrv.List(reqCtx, &pb.ListRequest{})
		if err != nil {
			sendResp(nil, err.Error())
			return
		}
		sendResp(resp, "")

	case "Kill":
		var r pb.KillRequest
		if err := unmarshalTunnelPayload(req.Payload, &r); err != nil {
			sendResp(nil, err.Error())
			return
		}
		resp, err := c.localSrv.Kill(reqCtx, &r)
		if err != nil {
			sendResp(nil, err.Error())
			return
		}
		sendResp(resp, "")

	case "Restart":
		var r pb.RestartRequest
		if err := unmarshalTunnelPayload(req.Payload, &r); err != nil {
			sendResp(nil, err.Error())
			return
		}
		resp, err := c.localSrv.Restart(reqCtx, &r)
		if err != nil {
			sendResp(nil, err.Error())
			return
		}
		sendResp(resp, "")

	case "ChangeCwd":
		var r pb.ChangeCwdRequest
		if err := unmarshalTunnelPayload(req.Payload, &r); err != nil {
			sendResp(nil, err.Error())
			return
		}
		resp, err := c.localSrv.ChangeCwd(reqCtx, &r)
		if err != nil {
			sendResp(nil, err.Error())
			return
		}
		sendResp(resp, "")

	case "Sleep":
		var r pb.SleepRequest
		if err := unmarshalTunnelPayload(req.Payload, &r); err != nil {
			sendResp(nil, err.Error())
			return
		}
		resp, err := c.localSrv.Sleep(reqCtx, &r)
		if err != nil {
			sendResp(nil, err.Error())
			return
		}
		sendResp(resp, "")

	case "Resume":
		var r pb.ResumeRequest
		if err := unmarshalTunnelPayload(req.Payload, &r); err != nil {
			sendResp(nil, err.Error())
			return
		}
		resp, err := c.localSrv.Resume(reqCtx, &r)
		if err != nil {
			sendResp(nil, err.Error())
			return
		}
		sendResp(resp, "")

	case "Cleanup":
		var r pb.CleanupRequest
		if err := unmarshalTunnelPayload(req.Payload, &r); err != nil {
			sendResp(nil, err.Error())
			return
		}
		resp, err := c.localSrv.Cleanup(reqCtx, &r)
		if err != nil {
			sendResp(nil, err.Error())
			return
		}
		sendResp(resp, "")
	case "Prune":
		resp, err := c.localSrv.Prune(reqCtx, &pb.PruneRequest{})
		if err != nil {
			sendResp(nil, err.Error())
			return
		}
		sendResp(resp, "")

	case "Interrupt":
		var r pb.InterruptRequest
		if err := unmarshalTunnelPayload(req.Payload, &r); err != nil {
			sendResp(nil, err.Error())
			return
		}
		resp, err := c.localSrv.Interrupt(reqCtx, &r)
		if err != nil {
			sendResp(nil, err.Error())
			return
		}
		sendResp(resp, "")

	case "PutSessionFile", "ReadSessionArtifact":
		var r pb.SessionFileRequest
		if err := unmarshalTunnelPayload(req.Payload, &r); err != nil {
			sendResp(nil, err.Error())
			return
		}
		var resp *pb.SessionFileResponse
		var err error
		trusted := withTrustedSubmit(reqCtx, req.RequestId)
		if req.Method == "PutSessionFile" {
			resp, err = c.localSrv.PutSessionFile(trusted, &r)
		} else {
			resp, err = c.localSrv.ReadSessionArtifact(trusted, &r)
		}
		if err != nil {
			sendResp(nil, err.Error())
			return
		}
		sendResp(resp, "")
	case "Submit":
		var r pb.SubmitRequest
		if err := unmarshalTunnelPayload(req.Payload, &r); err != nil {
			sendResp(nil, err.Error())
			return
		}
		resp, err := c.localSrv.Submit(withTrustedSubmit(reqCtx, req.RequestId), &r)
		if err != nil {
			sendResp(nil, err.Error())
			return
		}
		sendResp(resp, "")
	case "SteerQueued":
		var r pb.SteerQueuedRequest
		if err := unmarshalTunnelPayload(req.Payload, &r); err != nil {
			sendResp(nil, err.Error())
			return
		}
		resp, err := c.localSrv.SteerQueued(withTrustedSubmit(reqCtx, req.RequestId), &r)
		if err != nil {
			sendResp(nil, err.Error())
			return
		}
		sendResp(resp, "")
	case "Goal":
		var r pb.GoalRequest
		if err := unmarshalTunnelPayload(req.Payload, &r); err != nil {
			sendResp(nil, err.Error())
			return
		}
		resp, err := c.localSrv.Goal(reqCtx, &r)
		if err != nil {
			sendResp(nil, err.Error())
			return
		}
		sendResp(resp, "")

	case "GetLocalExpertAuthoringTurn":
		var r pb.LocalExpertAuthoringTurnRequest
		if err := unmarshalTunnelPayload(req.Payload, &r); err != nil {
			sendResp(nil, err.Error())
			return
		}
		resp, err := c.localSrv.GetLocalExpertAuthoringTurn(reqCtx, &r)
		if err != nil {
			sendResp(nil, err.Error())
			return
		}
		sendResp(resp, "")

	case "GetTurnResult":
		var r pb.GetTurnResultRequest
		if err := unmarshalTunnelPayload(req.Payload, &r); err != nil {
			sendResp(nil, err.Error())
			return
		}
		resp, err := c.localSrv.GetTurnResult(reqCtx, &r)
		if err != nil {
			sendResp(nil, err.Error())
			return
		}
		sendResp(resp, "")

	case "QueueList":
		var r pb.QueueListRequest
		if err := unmarshalTunnelPayload(req.Payload, &r); err != nil {
			sendResp(nil, err.Error())
			return
		}
		resp, err := c.localSrv.QueueList(reqCtx, &r)
		if err != nil {
			sendResp(nil, err.Error())
			return
		}
		sendResp(resp, "")

	case "QueueClear":
		var r pb.QueueClearRequest
		if err := unmarshalTunnelPayload(req.Payload, &r); err != nil {
			sendResp(nil, err.Error())
			return
		}
		resp, err := c.localSrv.QueueClear(reqCtx, &r)
		if err != nil {
			sendResp(nil, err.Error())
			return
		}
		sendResp(resp, "")

	case "QueueCancel":
		var r pb.QueueCancelRequest
		if err := unmarshalTunnelPayload(req.Payload, &r); err != nil {
			sendResp(nil, err.Error())
			return
		}
		resp, err := c.localSrv.QueueCancel(reqCtx, &r)
		if err != nil {
			sendResp(nil, err.Error())
			return
		}
		sendResp(resp, "")

	case "RespondPermission":
		var r pb.RespondPermissionRequest
		if err := unmarshalTunnelPayload(req.Payload, &r); err != nil {
			sendResp(nil, err.Error())
			return
		}
		resp, err := c.localSrv.RespondPermission(reqCtx, &r)
		if err != nil {
			sendResp(nil, err.Error())
			return
		}
		sendResp(resp, "")

	case "RevokePrincipalLaunches":
		var r pb.RevokePrincipalLaunchesRequest
		if err := unmarshalTunnelPayload(req.Payload, &r); err != nil {
			sendResp(nil, err.Error())
			return
		}
		resp, err := c.localSrv.RevokePrincipalLaunches(reqCtx, &r)
		if err != nil {
			sendResp(nil, err.Error())
			return
		}
		sendResp(resp, "")

	case "Attach":
		var r pb.AttachRequest
		if err := unmarshalTunnelPayload(req.Payload, &r); err != nil {
			sendResp(nil, err.Error())
			return
		}
		fakeStream := &fakeAttachStream{
			ctx:    reqCtx,
			reqID:  req.RequestId,
			sendFn: sendFn,
		}
		err := c.localSrv.Attach(&r, fakeStream)
		endMsg := &pb.TunnelMsg{Kind: "end", RequestId: req.RequestId}
		if err != nil {
			endMsg.Error = err.Error()
		}
		_ = sendFn(endMsg)

	case pb.TaskMethodSubmit:
		if c.taskSrv == nil {
			sendResp(nil, errTaskUnavailable)
			return
		}
		var r pb.SubmitSubtaskRequest
		if err := unmarshalTunnelPayload(req.Payload, &r); err != nil {
			sendResp(nil, err.Error())
			return
		}
		resp, err := c.taskSrv.SubmitSubtask(reqCtx, &r)
		if err != nil {
			sendResp(nil, err.Error())
			return
		}
		sendResp(resp, "")

	case pb.TaskMethodResolveParentTurn:
		if c.taskSrv == nil {
			sendResp(nil, errTaskUnavailable)
			return
		}
		var r pb.ActiveTurnRequest
		if err := unmarshalTunnelPayload(req.Payload, &r); err != nil {
			sendResp(nil, err.Error())
			return
		}
		resp, err := c.taskSrv.ResolveParentTurn(reqCtx, &r)
		if err != nil {
			sendResp(nil, err.Error())
			return
		}
		sendResp(resp, "")

	case pb.TaskMethodGet:
		if c.taskSrv == nil {
			sendResp(nil, errTaskUnavailable)
			return
		}
		var r pb.GetSubtaskRequest
		if err := unmarshalTunnelPayload(req.Payload, &r); err != nil {
			sendResp(nil, err.Error())
			return
		}
		resp, err := c.taskSrv.GetSubtask(reqCtx, &r)
		if err != nil {
			sendResp(nil, err.Error())
			return
		}
		sendResp(resp, "")

	case pb.TaskMethodList:
		if c.taskSrv == nil {
			sendResp(nil, errTaskUnavailable)
			return
		}
		resp, err := c.taskSrv.ListSubtasks(reqCtx, &pb.ListSubtasksRequest{})
		if err != nil {
			sendResp(nil, err.Error())
			return
		}
		sendResp(resp, "")

	case pb.TaskMethodPrune:
		if c.taskSrv == nil {
			sendResp(nil, errTaskUnavailable)
			return
		}
		var r pb.PruneSubtasksRequest
		if err := unmarshalTunnelPayload(req.Payload, &r); err != nil {
			sendResp(nil, err.Error())
			return
		}
		resp, err := c.taskSrv.PruneSubtasks(reqCtx, &r)
		if err != nil {
			sendResp(nil, err.Error())
			return
		}
		sendResp(resp, "")

	case pb.TaskMethodCancel:
		if c.taskSrv == nil {
			sendResp(nil, errTaskUnavailable)
			return
		}
		var r pb.CancelSubtaskRequest
		if err := unmarshalTunnelPayload(req.Payload, &r); err != nil {
			sendResp(nil, err.Error())
			return
		}
		resp, err := c.taskSrv.CancelSubtask(reqCtx, &r)
		if err != nil {
			sendResp(nil, err.Error())
			return
		}
		sendResp(resp, "")

	case pb.TaskMethodRespond:
		if c.taskSrv == nil {
			sendResp(nil, errTaskUnavailable)
			return
		}
		var r pb.RespondSubtaskPermissionRequest
		if err := unmarshalTunnelPayload(req.Payload, &r); err != nil {
			sendResp(nil, err.Error())
			return
		}
		resp, err := c.taskSrv.RespondSubtaskPermission(reqCtx, &r)
		if err != nil {
			sendResp(nil, err.Error())
			return
		}
		sendResp(resp, "")

	case pb.TaskMethodStream:
		if c.taskSrv == nil {
			_ = sendFn(&pb.TunnelMsg{Kind: "end", RequestId: req.RequestId, Error: errTaskUnavailable})
			return
		}
		var r pb.StreamSubtaskRequest
		if err := unmarshalTunnelPayload(req.Payload, &r); err != nil {
			_ = sendFn(&pb.TunnelMsg{Kind: "end", RequestId: req.RequestId, Error: err.Error()})
			return
		}
		fakeStream := &fakeTaskStream{ctx: reqCtx, reqID: req.RequestId, sendFn: sendFn}
		err := c.taskSrv.StreamSubtask(&r, fakeStream)
		endMsg := &pb.TunnelMsg{Kind: "end", RequestId: req.RequestId}
		if err != nil {
			endMsg.Error = err.Error()
		}
		_ = sendFn(endMsg)

	case pb.TaskMethodInstanceSend:
		if c.taskSrv == nil {
			sendResp(nil, errTaskUnavailable)
			return
		}
		var r pb.SendAgentMessageRequest
		if err := unmarshalTunnelPayload(req.Payload, &r); err != nil {
			sendResp(nil, err.Error())
			return
		}
		resp, err := c.taskSrv.SendAgentMessage(reqCtx, &r)
		if err != nil {
			sendResp(nil, err.Error())
			return
		}
		sendResp(resp, "")

	case pb.TaskMethodInstanceInterrupt:
		if c.taskSrv == nil {
			sendResp(nil, errTaskUnavailable)
			return
		}
		var r pb.InterruptAgentInstanceRequest
		if err := unmarshalTunnelPayload(req.Payload, &r); err != nil {
			sendResp(nil, err.Error())
			return
		}
		resp, err := c.taskSrv.InterruptAgentInstance(reqCtx, &r)
		if err != nil {
			sendResp(nil, err.Error())
			return
		}
		sendResp(resp, "")

	case pb.TaskMethodInstanceWait:
		if c.taskSrv == nil {
			sendResp(nil, errTaskUnavailable)
			return
		}
		var r pb.WaitAgentInstanceRequest
		if err := unmarshalTunnelPayload(req.Payload, &r); err != nil {
			sendResp(nil, err.Error())
			return
		}
		resp, err := c.taskSrv.WaitAgentInstance(reqCtx, &r)
		if err != nil {
			sendResp(nil, err.Error())
			return
		}
		sendResp(resp, "")

	case pb.TaskMethodInstanceDescribe:
		if c.taskSrv == nil {
			sendResp(nil, errTaskUnavailable)
			return
		}
		var r pb.DescribeAgentInstanceRequest
		if err := unmarshalTunnelPayload(req.Payload, &r); err != nil {
			sendResp(nil, err.Error())
			return
		}
		resp, err := c.taskSrv.DescribeAgentInstance(reqCtx, &r)
		if err != nil {
			sendResp(nil, err.Error())
			return
		}
		sendResp(resp, "")

	case pb.TaskMethodInstanceList:
		if c.taskSrv == nil {
			sendResp(nil, errTaskUnavailable)
			return
		}
		var r pb.ListAgentInstancesRequest
		if err := unmarshalTunnelPayload(req.Payload, &r); err != nil {
			sendResp(nil, err.Error())
			return
		}
		resp, err := c.taskSrv.ListAgentInstances(reqCtx, &r)
		if err != nil {
			sendResp(nil, err.Error())
			return
		}
		sendResp(resp, "")

	case pb.TaskMethodInstanceGet:
		if c.taskSrv == nil {
			sendResp(nil, errTaskUnavailable)
			return
		}
		var r pb.GetAgentInstanceRequest
		if err := unmarshalTunnelPayload(req.Payload, &r); err != nil {
			sendResp(nil, err.Error())
			return
		}
		resp, err := c.taskSrv.GetAgentInstance(reqCtx, &r)
		if err != nil {
			sendResp(nil, err.Error())
			return
		}
		sendResp(resp, "")

	case pb.TaskMethodInstanceCleanup:
		if c.taskSrv == nil {
			sendResp(nil, errTaskUnavailable)
			return
		}
		var r pb.CleanupAgentInstanceRequest
		if err := unmarshalTunnelPayload(req.Payload, &r); err != nil {
			sendResp(nil, err.Error())
			return
		}
		resp, err := c.taskSrv.CleanupAgentInstance(reqCtx, &r)
		if err != nil {
			sendResp(nil, err.Error())
			return
		}
		sendResp(resp, "")

	default:
		if c.handleAdmission(req, sendResp) {
			return
		}
		sendResp(nil, fmt.Sprintf("unknown method %q", req.Method))
	}
}

// handleAdmission answers the scheduler's reserve -> commit -> release admission
// methods against the runtime's local Admission. It returns true if it handled
// the method. Admission is rejected when no capabilities (hence no admission)
// were configured for this runtime.
func (c *Client) handleAdmission(req *pb.TunnelMsg, sendResp func(interface{}, string)) bool {
	switch req.Method {
	case "Reserve":
		if c.adm == nil {
			sendResp(nil, "admission not available on this runtime")
			return true
		}
		var r pb.ReserveRequest
		if err := unmarshalTunnelPayload(req.Payload, &r); err != nil {
			sendResp(nil, err.Error())
			return true
		}
		id, err := c.adm.Reserve(r.AgentType)
		if err != nil {
			sendResp(nil, err.Error())
			return true
		}
		sendResp(&pb.ReserveResponse{ReservationId: id}, "")
		return true
	case "Commit":
		if c.adm == nil {
			sendResp(nil, "admission not available on this runtime")
			return true
		}
		var r pb.CommitRequest
		if err := unmarshalTunnelPayload(req.Payload, &r); err != nil {
			sendResp(nil, err.Error())
			return true
		}
		if err := c.adm.Commit(r.ReservationId); err != nil {
			sendResp(nil, err.Error())
			return true
		}
		sendResp(&pb.CommitResponse{}, "")
		return true
	case "Release":
		if c.adm == nil {
			sendResp(nil, "admission not available on this runtime")
			return true
		}
		var r pb.ReleaseRequest
		if err := unmarshalTunnelPayload(req.Payload, &r); err != nil {
			sendResp(nil, err.Error())
			return true
		}
		c.adm.Release(r.ReservationId)
		sendResp(&pb.ReleaseResponse{}, "")
		return true
	}
	return false
}

// fakeAttachStream implements pb.Amuxlet_AttachServer over the tunnel.
type fakeAttachStream struct {
	ctx    context.Context
	reqID  string
	sendFn func(*pb.TunnelMsg) error
}

func (f *fakeAttachStream) Send(ev *pb.AttachEvent) error {
	payload, _ := json.Marshal(ev)
	return f.sendFn(&pb.TunnelMsg{Kind: "data", RequestId: f.reqID, Payload: payload})
}

func (f *fakeAttachStream) Context() context.Context     { return f.ctx }
func (f *fakeAttachStream) SetHeader(metadata.MD) error  { return nil }
func (f *fakeAttachStream) SendHeader(metadata.MD) error { return nil }
func (f *fakeAttachStream) SetTrailer(metadata.MD)       {}
func (f *fakeAttachStream) RecvMsg(interface{}) error    { return nil }
func (f *fakeAttachStream) SendMsg(interface{}) error    { return nil }

// fakeTaskStream implements pb.AmuxTask_StreamSubtaskServer over the tunnel: each
// SubtaskEvent is forwarded as a "data" frame and the caller emits the trailing
// "end" frame, mirroring the Attach path.
type fakeTaskStream struct {
	ctx    context.Context
	reqID  string
	sendFn func(*pb.TunnelMsg) error
}

func (f *fakeTaskStream) Send(ev *pb.SubtaskEvent) error {
	payload, _ := json.Marshal(ev)
	return f.sendFn(&pb.TunnelMsg{Kind: "data", RequestId: f.reqID, Payload: payload})
}

func (f *fakeTaskStream) Context() context.Context     { return f.ctx }
func (f *fakeTaskStream) SetHeader(metadata.MD) error  { return nil }
func (f *fakeTaskStream) SendHeader(metadata.MD) error { return nil }
func (f *fakeTaskStream) SetTrailer(metadata.MD)       {}
func (f *fakeTaskStream) RecvMsg(interface{}) error    { return nil }
func (f *fakeTaskStream) SendMsg(interface{}) error    { return nil }
