//go:build linux

package isolation

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/amuxos/amux/internal/cleanup"
	"io"
	"io/fs"
	"net"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/google/uuid"
	"golang.org/x/sys/unix"
	_ "modernc.org/sqlite"
)

const helperVersion = "1"

var (
	accountPattern   = regexp.MustCompile(`^amux_[a-f0-9]{12,32}$`)
	executionPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
	launchPattern    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,191}$`)
)

type HelperConfig struct {
	SocketPath           string
	StateDir             string
	PrincipalRoot        string
	ServiceUID           int
	ServiceGID           int
	UIDRangeStart        int
	UIDRangeEnd          int
	PIDNamespace         bool
	MountNamespace       bool
	Seccomp              bool
	ExecWrapperPath      string
	MaterializerPath     string
	AllowUnprivileged    bool
	SkipAccountProvision bool
}

func (c HelperConfig) validate() error {
	for label, path := range map[string]string{
		"socket": c.SocketPath, "state dir": c.StateDir, "principal root": c.PrincipalRoot,
		"materializer": c.MaterializerPath,
	} {
		if !filepath.IsAbs(path) {
			return fmt.Errorf("%s path must be absolute", label)
		}
	}
	if c.ServiceUID < 0 || c.ServiceGID < 0 {
		return fmt.Errorf("service uid/gid must be non-negative")
	}
	if c.UIDRangeStart <= 0 {
		return fmt.Errorf("uid range start must be positive")
	}
	if c.UIDRangeEnd == 0 {
		c.UIDRangeEnd = 2147483000
	}
	if c.UIDRangeEnd < c.UIDRangeStart || c.UIDRangeEnd > 2147483000 {
		return fmt.Errorf("invalid uid range")
	}
	info, err := os.Stat(c.MaterializerPath)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		return fmt.Errorf("materializer must be an executable regular file")
	}
	return nil
}

type helperServer struct {
	cfg      HelperConfig
	db       *sql.DB
	listener *net.UnixListener

	mu            sync.Mutex
	launches      map[string]*helperLaunch
	draining      bool
	upgradeGuards int
	mappingMu     sync.Mutex

	hardeningMu        sync.Mutex
	hardening          map[string]bool
	degradedHardening  map[string]string
	hardeningByAccount map[string]map[string]bool
}

type helperLaunch struct {
	desc LaunchDescription
	cmd  *exec.Cmd
	done chan struct{}

	stdin  *os.File
	stdout *os.File
	stderr *os.File
}

func ServeHelper(ctx context.Context, cfg HelperConfig) error {
	if cfg.UIDRangeEnd == 0 {
		cfg.UIDRangeEnd = 2147483000
	}
	if cfg.MaterializerPath == "" {
		cfg.MaterializerPath, _ = os.Executable()
	}
	if err := cfg.validate(); err != nil {
		return err
	}
	if os.Geteuid() != 0 && !cfg.AllowUnprivileged {
		return fmt.Errorf("isolation helper must run as root")
	}
	// Order matters. StateDir and PrincipalRoot usually share an ancestor
	// (/var/lib/amux), and MkdirAll applies its mode to every directory it
	// creates. Creating the root-only state dir first leaves that shared
	// ancestor at 0700, which silently makes every principal path unreachable
	// for the amuxlet service identity. Create the traversable tree first.
	if err := os.MkdirAll(cfg.PrincipalRoot, 0o711); err != nil {
		return err
	}
	if err := os.MkdirAll(cfg.StateDir, 0o700); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(cfg.SocketPath), 0o700); err != nil {
		return err
	}
	if err := verifyServiceTraversal(cfg.PrincipalRoot, cfg.ServiceUID, cfg.ServiceGID); err != nil {
		return err
	}
	db, err := openHelperRegistry(filepath.Join(cfg.StateDir, "launches.db"))
	if err != nil {
		return err
	}
	defer db.Close()
	if _, err := db.Exec(`UPDATE launches SET state='isolation_lost', error_code='isolation_lost'
WHERE state IN ('starting','running')`); err != nil {
		return err
	}
	_ = os.Remove(cfg.SocketPath)
	listener, err := net.ListenUnix("unixpacket", &net.UnixAddr{Name: cfg.SocketPath, Net: "unixpacket"})
	if err != nil {
		return err
	}
	defer func() {
		_ = listener.Close()
		_ = os.Remove(cfg.SocketPath)
	}()
	if err := os.Chown(cfg.SocketPath, cfg.ServiceUID, cfg.ServiceGID); err != nil && !cfg.AllowUnprivileged {
		return err
	}
	if err := os.Chmod(cfg.SocketPath, 0o600); err != nil {
		return err
	}
	server := &helperServer{
		cfg: cfg, db: db, listener: listener, launches: make(map[string]*helperLaunch),
		hardening: map[string]bool{
			"pid_namespace": false, "mount_namespace": false, "seccomp": false,
		},
		degradedHardening:  make(map[string]string),
		hardeningByAccount: make(map[string]map[string]bool),
	}
	go func() {
		<-ctx.Done()
		_ = listener.Close()
		server.killAll()
	}()
	for {
		conn, err := listener.AcceptUnix()
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return nil
			}
			return err
		}
		go server.serveConn(conn)
	}
}

// verifyServiceTraversal fails helper startup when amuxlet's identity cannot
// reach the principal root. The control plane addresses principal directories
// by fixed path, so without search permission on every ancestor the first
// identified launch dies deep inside the runtime with an opaque mkdir error
// instead of here, where the cause is obvious.
func verifyServiceTraversal(principalRoot string, serviceUID, serviceGID int) error {
	if serviceUID == 0 {
		return nil // root traverses regardless of mode
	}
	for path := filepath.Clean(principalRoot); ; path = filepath.Dir(path) {
		info, err := os.Stat(path)
		if err != nil {
			return err
		}
		if !serviceCanTraverse(info, serviceUID, serviceGID) {
			return fmt.Errorf(
				"service uid %d cannot traverse %s (mode %04o); grant search permission before enabling linux-dac",
				serviceUID, path, info.Mode().Perm())
		}
		if parent := filepath.Dir(path); parent == path {
			return nil
		}
	}
}

func serviceCanTraverse(info fs.FileInfo, uid, gid int) bool {
	perm := info.Mode().Perm()
	if perm&0o001 != 0 {
		return true
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return false
	}
	if int(stat.Uid) == uid && perm&0o100 != 0 {
		return true
	}
	return int(stat.Gid) == gid && perm&0o010 != 0
}

func openHelperRegistry(path string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	// SQLite allows one writer. Without a single connection, database/sql opens
	// another for the launch reaper's row update while RemoveRun updates the
	// mapping row. busy_timeout below applies only to the connection that
	// executes it, so the other connection returns SQLITE_BUSY immediately.
	// Process-exit cleanup ignores that error and leaves the run directory.
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`PRAGMA busy_timeout=5000; PRAGMA journal_mode=WAL;
CREATE TABLE IF NOT EXISTS helper_mappings (
  principal_id TEXT PRIMARY KEY,
  account TEXT NOT NULL UNIQUE,
  uid INTEGER NOT NULL UNIQUE,
  gid INTEGER NOT NULL UNIQUE,
  created_at INTEGER NOT NULL,
  last_used_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS launches (
  launch_id TEXT PRIMARY KEY,
  principal_id TEXT NOT NULL,
  account TEXT NOT NULL,
  uid INTEGER NOT NULL,
  host_pid INTEGER NOT NULL DEFAULT 0,
  pid_start INTEGER NOT NULL DEFAULT 0,
  started_at INTEGER NOT NULL,
  state TEXT NOT NULL,
  exit_status_json TEXT NOT NULL DEFAULT '',
  error_code TEXT NOT NULL DEFAULT '',
  ack_state TEXT NOT NULL DEFAULT ''
);`); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}

func (s *helperServer) serveConn(conn *net.UnixConn) {
	defer conn.Close()
	if err := s.authorizePeer(conn); err != nil {
		_ = s.writeResponse(conn, HelperResponse{
			ProtocolVersion: HelperProtocolVersion, ErrorCode: "unauthorized", Error: err.Error(),
		}, nil)
		return
	}
	payload := make([]byte, MaxHelperFrameBytes+4)
	oob := make([]byte, unix.CmsgSpace(4))
	n, oobn, flags, _, err := conn.ReadMsgUnix(payload, oob)
	if err != nil {
		return
	}
	if flags&(unix.MSG_TRUNC|unix.MSG_CTRUNC) != 0 {
		_ = s.writeResponse(conn, HelperResponse{ProtocolVersion: HelperProtocolVersion, ErrorCode: "invalid_request", Error: "request frame truncated"}, nil)
		return
	}
	var req HelperRequest
	if err := decodeHelperFrame(payload[:n], &req); err != nil {
		_ = s.writeResponse(conn, HelperResponse{ProtocolVersion: HelperProtocolVersion, ErrorCode: "invalid_request", Error: err.Error()}, nil)
		return
	}
	requestFiles, err := receivedFiles(oob[:oobn])
	if err != nil {
		_ = s.writeResponse(conn, HelperResponse{ProtocolVersion: HelperProtocolVersion, ErrorCode: "invalid_request", Error: err.Error()}, nil)
		return
	}
	defer func() {
		for _, file := range requestFiles {
			_ = file.Close()
		}
	}()
	resp, files := s.handleWithFiles(req, requestFiles)
	resp.ProtocolVersion = HelperProtocolVersion
	resp.RequestID = req.RequestID
	resp.LaunchID = req.LaunchID
	_ = s.writeResponse(conn, resp, files)
	for _, file := range files {
		_ = file.Close()
	}
}

func (s *helperServer) authorizePeer(conn *net.UnixConn) error {
	raw, err := conn.SyscallConn()
	if err != nil {
		return err
	}
	var cred *unix.Ucred
	var controlErr error
	if err := raw.Control(func(fd uintptr) {
		cred, controlErr = unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
	}); err != nil {
		return err
	}
	if controlErr != nil {
		return controlErr
	}
	if cred == nil || cred.Pid <= 0 || int(cred.Uid) != s.cfg.ServiceUID || int(cred.Gid) != s.cfg.ServiceGID {
		return fmt.Errorf("peer credentials do not match the configured service identity")
	}
	return nil
}

func (s *helperServer) writeResponse(conn *net.UnixConn, resp HelperResponse, files []*os.File) error {
	if resp.ErrorCode == "" {
		resp.OK = true
	}
	frame, err := encodeHelperFrame(resp)
	if err != nil {
		return err
	}
	var oob []byte
	if len(files) > 0 {
		fds := make([]int, 0, len(files))
		for _, file := range files {
			fds = append(fds, int(file.Fd()))
		}
		oob = unix.UnixRights(fds...)
	}
	_, _, err = conn.WriteMsgUnix(frame, oob, nil)
	return err
}

func (s *helperServer) handle(req HelperRequest) (HelperResponse, []*os.File) {
	return s.handleWithFiles(req, nil)
}

func (s *helperServer) handleWithFiles(req HelperRequest, requestFiles []*os.File) (HelperResponse, []*os.File) {
	if req.ProtocolVersion != HelperProtocolVersion {
		return helperError("protocol_mismatch", "unsupported helper protocol version"), nil
	}
	if strings.TrimSpace(req.RequestID) == "" {
		return helperError("invalid_request", "request_id is required"), nil
	}
	switch req.Operation {
	case OpHandshake:
		s.mu.Lock()
		draining := s.draining || s.upgradeGuards > 0
		s.mu.Unlock()
		hardening, degraded := s.hardeningStatus()
		return HelperResponse{HelperVersion: helperVersion, Draining: draining, Hardening: hardening, DegradedReasons: degraded}, nil
	case OpEnsurePrincipal:
		paths, err := s.ensurePrincipal(req.Mapping)
		if err != nil {
			return helperError("invalid_principal", err.Error()), nil
		}
		return HelperResponse{Paths: &paths, Mapping: req.Mapping}, nil
	case OpPrepareRun:
		paths, err := s.prepareRun(req.Mapping, req.ExecutionID, req.LaunchID)
		if err != nil {
			return helperError("prepare_failed", err.Error()), nil
		}
		return HelperResponse{Paths: &paths, Mapping: req.Mapping}, nil
	case OpSessionFile:
		file, err := s.sessionFile(req, requestFiles)
		if err != nil {
			return helperError("session_file_failed", err.Error()), nil
		}
		return HelperResponse{}, []*os.File{file}
	case OpReadHomeFiles:
		file, err := s.readHomeFiles(req)
		if err != nil {
			return helperError("read_home_files_failed", err.Error()), nil
		}
		return HelperResponse{}, []*os.File{file}
	case OpMaterializeRun:
		if len(requestFiles) != 1 {
			return helperError("materialize_failed", "materialize_run requires exactly one bundle fd"), nil
		}
		if err := s.materializeRun(req.Mapping, req.ExecutionID, req.LaunchID, requestFiles[0]); err != nil {
			return helperError("materialize_failed", err.Error()), nil
		}
		return HelperResponse{}, nil
	case OpStart:
		desc, files, err := s.start(req)
		if err != nil {
			return helperError("start_failed", err.Error()), nil
		}
		return HelperResponse{Description: &desc}, files
	case OpDescribe:
		desc, err := s.describe(req.LaunchID)
		if err != nil {
			return responseForError(err), nil
		}
		return HelperResponse{Description: &desc}, nil
	case OpWait:
		desc, err := s.wait(req.LaunchID)
		if err != nil {
			return responseForError(err), nil
		}
		return HelperResponse{Description: &desc}, nil
	case OpSignal:
		if err := s.signal(req.LaunchID, req.Signal); err != nil {
			return responseForError(err), nil
		}
		return HelperResponse{}, nil
	case OpKill:
		if err := s.signal(req.LaunchID, "KILL"); err != nil {
			return responseForError(err), nil
		}
		return HelperResponse{}, nil
	case OpCloseHandle:
		if err := s.closeHandle(req.LaunchID); err != nil {
			return responseForError(err), nil
		}
		return HelperResponse{}, nil
	case OpDailyCleanup:
		if req.Cleanup == nil || req.Cleanup.References == nil {
			return helperError("invalid_request", "cleanup references are required"), nil
		}
		if err := s.validateMapping(req.Mapping); err != nil {
			return responseForError(err), nil
		}
		// Serialize against prepare/start while checking the helper's own live
		// launch registry in addition to the Runtime's persisted references.
		s.mu.Lock()
		defer s.mu.Unlock()
		refs := *req.Cleanup.References
		refs.Workspaces = append([]string{}, refs.Workspaces...)
		for _, launch := range s.launches {
			if launch.desc.State == "running" {
				refs.Workspaces = append(refs.Workspaces, launch.desc.Paths.Workspace)
			}
		}
		paths := s.pathsFor(*req.Mapping, "")
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		steps := []cleanup.Step{cleanup.SweepWorkspaces(cleanupCtx, paths.Workspace, refs, 1, time.Now(), req.Cleanup.DryRun)}
		steps = append(steps, cleanup.SweepHome(cleanupCtx, paths.Home, refs, time.Now(), req.Cleanup.DryRun)...)
		return HelperResponse{CleanupSteps: steps}, nil
	case OpReclaimHome:
		if err := s.reclaimHome(req.Mapping, req.HomeCaches); err != nil {
			return helperError("reclaim_home_failed", err.Error()), nil
		}
		return HelperResponse{}, nil
	case OpRemoveWorkspace:
		if err := s.removeWorkspace(req.Mapping, req.ExecutionID); err != nil {
			return helperError("remove_workspace_failed", err.Error()), nil
		}
		return HelperResponse{}, nil
	case OpRemoveRun:
		if err := s.removeRun(req.Mapping, req.ExecutionID); err != nil {
			return helperError("remove_failed", err.Error()), nil
		}
		return HelperResponse{}, nil
	case OpKillPrincipal:
		if err := s.killPrincipal(req.Mapping); err != nil {
			return helperError("kill_failed", err.Error()), nil
		}
		return HelperResponse{}, nil
	case OpUpgradeGuard:
		if len(requestFiles) != 1 {
			return helperError("invalid_request", "upgrade guard requires a lease pipe"), nil
		}
		info, err := requestFiles[0].Stat()
		if err != nil || info.Mode()&os.ModeNamedPipe == 0 {
			return helperError("invalid_request", "upgrade guard requires a lease pipe"), nil
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		active := 0
		for _, launch := range s.launches {
			if launch.desc.State == "running" {
				active++
			}
		}
		if active != 0 {
			return HelperResponse{ActiveLaunches: active}, nil
		}
		fd, err := unix.FcntlInt(requestFiles[0].Fd(), unix.F_DUPFD_CLOEXEC, 0)
		if err != nil {
			return helperError("upgrade_guard_failed", err.Error()), nil
		}
		lease := os.NewFile(uintptr(fd), "upgrade-lease")
		s.upgradeGuards++
		go func() {
			_, _ = io.Copy(io.Discard, lease)
			_ = lease.Close()
			s.mu.Lock()
			s.upgradeGuards--
			s.mu.Unlock()
		}()
		return HelperResponse{Draining: true}, nil
	case OpDrain:
		s.mu.Lock()
		active := 0
		for _, launch := range s.launches {
			if launch.desc.State == "running" {
				active++
			}
		}
		// A status drain (force=false) is a read-only probe: it reports active
		// launches without entering draining, so an install/uninstall guard that
		// aborts on active launches cannot permanently wedge the helper into
		// rejecting every new launch. Only an explicit forced drain (uninstall
		// --force) enters draining; it kills active launches so the count is 0.
		draining := false
		if req.Force {
			s.draining = true
			draining = true
			active = 0
		}
		s.mu.Unlock()
		if req.Force {
			s.killAll()
		}
		return HelperResponse{Draining: draining, ActiveLaunches: active}, nil
	case OpListAccounts:
		accounts, err := s.listAccounts()
		if err != nil {
			return helperError("registry_failed", err.Error()), nil
		}
		return HelperResponse{Accounts: accounts}, nil
	case OpRemoveAccount:
		if err := s.removeAccount(req.Mapping); err != nil {
			return helperError("remove_account_failed", err.Error()), nil
		}
		return HelperResponse{}, nil
	default:
		return helperError("unsupported_operation", "unsupported helper operation"), nil
	}

}

func helperError(code, message string) HelperResponse {
	return HelperResponse{ErrorCode: code, Error: message}
}

func responseForError(err error) HelperResponse {
	switch {
	case errors.Is(err, ErrNotFound):
		return helperError("not_found", err.Error())
	case errors.Is(err, ErrIsolationLost):
		return helperError("isolation_lost", err.Error())
	default:
		return helperError("operation_failed", err.Error())
	}
}

func (s *helperServer) validateMapping(mapping *AccountMapping) error {
	s.mappingMu.Lock()
	defer s.mappingMu.Unlock()
	if mapping == nil {
		return fmt.Errorf("account mapping is required")
	}
	id, err := uuid.Parse(mapping.PrincipalID)
	if err != nil || id.String() != mapping.PrincipalID {
		return fmt.Errorf("principal id must be a canonical UUID")
	}
	if !accountPattern.MatchString(mapping.Account) {
		return fmt.Errorf("invalid account name")
	}
	if mapping.UID < s.cfg.UIDRangeStart || mapping.UID > s.cfg.UIDRangeEnd || mapping.GID != mapping.UID {
		return fmt.Errorf("uid/gid is outside the configured principal range")
	}
	var principal, account string
	var uid, gid int
	err = s.db.QueryRow(`SELECT principal_id,account,uid,gid FROM helper_mappings
WHERE principal_id=? OR account=? OR uid=? OR gid=? LIMIT 1`,
		mapping.PrincipalID, mapping.Account, mapping.UID, mapping.GID).
		Scan(&principal, &account, &uid, &gid)
	switch err {
	case sql.ErrNoRows:
		now := time.Now().UTC().UnixNano()
		_, err = s.db.Exec(`INSERT INTO helper_mappings(principal_id,account,uid,gid,created_at,last_used_at)
VALUES(?,?,?,?,?,?)`, mapping.PrincipalID, mapping.Account, mapping.UID, mapping.GID, now, now)
		return err
	case nil:
		if principal != mapping.PrincipalID || account != mapping.Account || uid != mapping.UID || gid != mapping.GID {
			return fmt.Errorf("principal mapping conflicts with helper registry")
		}
		_, err = s.db.Exec(`UPDATE helper_mappings SET last_used_at=? WHERE principal_id=?`, time.Now().UTC().UnixNano(), mapping.PrincipalID)
		return err
	default:
		return err
	}
}

func (s *helperServer) ensurePrincipal(mapping *AccountMapping) (PrincipalPaths, error) {
	if err := s.validateMapping(mapping); err != nil {
		return PrincipalPaths{}, err
	}
	paths := s.pathsFor(*mapping, "")
	if !s.cfg.SkipAccountProvision {
		if err := ensureSystemAccount(*mapping, paths.Home); err != nil {
			return PrincipalPaths{}, err
		}
	}
	if err := s.ensurePrincipalDirs(*mapping, paths); err != nil {
		return PrincipalPaths{}, err
	}
	return paths, nil
}

func ensureSystemAccount(mapping AccountMapping, home string) error {
	existing, err := user.Lookup(mapping.Account)
	if err == nil {
		uid, uidErr := strconv.Atoi(existing.Uid)
		gid, gidErr := strconv.Atoi(existing.Gid)
		if uidErr != nil || gidErr != nil || uid != mapping.UID || gid != mapping.GID {
			return fmt.Errorf("existing account %s has unexpected uid/gid", mapping.Account)
		}
		return nil
	}
	if _, ok := err.(user.UnknownUserError); !ok {
		return err
	}
	group, groupErr := user.LookupGroup(mapping.Account)
	if groupErr == nil {
		gid, parseErr := strconv.Atoi(group.Gid)
		if parseErr != nil || gid != mapping.GID {
			return fmt.Errorf("existing group %s has unexpected gid", mapping.Account)
		}
	} else {
		if _, ok := groupErr.(user.UnknownGroupError); !ok {
			return groupErr
		}
		groupadd, lookupErr := exec.LookPath("groupadd")
		if lookupErr != nil {
			if _, statErr := os.Stat("/usr/sbin/groupadd"); statErr == nil {
				groupadd = "/usr/sbin/groupadd"
			} else {
				return fmt.Errorf("groupadd not found")
			}
		}
		cmd := exec.Command(groupadd, "--gid", strconv.Itoa(mapping.GID), mapping.Account)
		if output, runErr := cmd.CombinedOutput(); runErr != nil {
			return fmt.Errorf("groupadd %s: %w: %s", mapping.Account, runErr, strings.TrimSpace(string(output)))
		}
	}
	useradd, err := exec.LookPath("useradd")
	if err != nil {
		if _, statErr := os.Stat("/usr/sbin/useradd"); statErr == nil {
			useradd = "/usr/sbin/useradd"
		} else {
			return fmt.Errorf("useradd not found")
		}
	}
	cmd := exec.Command(useradd,
		"--uid", strconv.Itoa(mapping.UID),
		"--gid", mapping.Account,
		"--no-create-home",
		"--home-dir", home,
		"--shell", "/usr/sbin/nologin",
		mapping.Account,
	)
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("useradd %s: %w: %s", mapping.Account, err, strings.TrimSpace(string(output)))
	}
	return nil
}

func (s *helperServer) pathsFor(mapping AccountMapping, executionID string) PrincipalPaths {
	root := filepath.Join(s.cfg.PrincipalRoot, mapping.Account)
	paths := PrincipalPaths{
		Root:      root,
		Home:      filepath.Join(root, "home"),
		Workspace: filepath.Join(root, "workspace"),
	}
	if executionID != "" {
		paths.Workspace = filepath.Join(paths.Workspace, executionID)
		paths.Run = filepath.Join(root, "run", executionID)
	}
	return paths
}

func (s *helperServer) ensurePrincipalDirs(mapping AccountMapping, paths PrincipalPaths) error {
	type dirSpec struct {
		path string
		mode fs.FileMode
		uid  int
		gid  int
	}
	dirs := []dirSpec{
		{paths.Root, 0o710, mapping.UID, s.cfg.ServiceGID},
		{paths.Home, 0o700, mapping.UID, mapping.GID},
		{filepath.Join(paths.Root, "workspace"), 0o710, mapping.UID, s.cfg.ServiceGID},
		{filepath.Join(paths.Root, "run"), 0o710, mapping.UID, s.cfg.ServiceGID},
	}
	for _, dir := range dirs {
		if err := ensureOwnedDir(dir.path, dir.mode, dir.uid, dir.gid, s.cfg.AllowUnprivileged); err != nil {
			return err
		}
	}
	return nil
}

// ensureOwnedDir re-opens the directory with O_NOFOLLOW and applies mode and
// ownership through that descriptor. The principal owns the intermediate
// account/workspace/run directories, so a path-based chmod/chown after an
// Lstat check could be redirected at an arbitrary target by swapping the entry
// for a symlink in between; operating on the fd pins check and mutation to the
// same inode.
func ensureOwnedDir(path string, mode fs.FileMode, uid, gid int, allowUnprivileged bool) error {
	if err := os.Mkdir(path, mode); err != nil && !os.IsExist(err) {
		return err
	}
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return fmt.Errorf("%s is not a real directory: %w", path, err)
	}
	defer unix.Close(fd)
	if err := unix.Fchmod(fd, uint32(mode.Perm())); err != nil {
		return err
	}
	if err := unix.Fchown(fd, uid, gid); err != nil && !allowUnprivileged {
		return err
	}
	return nil
}

func (s *helperServer) prepareRun(mapping *AccountMapping, workspaceID, launchID string) (PrincipalPaths, error) {
	if !executionPattern.MatchString(workspaceID) || !launchPattern.MatchString(launchID) {
		return PrincipalPaths{}, fmt.Errorf("invalid workspace or launch id")
	}
	if _, err := s.ensurePrincipal(mapping); err != nil {
		return PrincipalPaths{}, err
	}
	paths := s.pathsFor(*mapping, workspaceID)
	paths.Run = filepath.Join(paths.Root, "run", launchID)
	for _, path := range []string{paths.Workspace, paths.Run} {
		if err := ensureOwnedDir(path, 0o700, mapping.UID, mapping.GID, s.cfg.AllowUnprivileged); err != nil {
			return PrincipalPaths{}, err
		}
	}
	return paths, nil
}

func (s *helperServer) materializeRun(mapping *AccountMapping, workspaceID, launchID string, bundle *os.File) error {
	if bundle == nil {
		return fmt.Errorf("materialization bundle fd is required")
	}
	if !executionPattern.MatchString(workspaceID) || !launchPattern.MatchString(launchID) {
		return fmt.Errorf("invalid workspace or launch id")
	}
	if err := s.validateMapping(mapping); err != nil {
		return err
	}
	paths := s.pathsFor(*mapping, workspaceID)
	paths.Run = filepath.Join(paths.Root, "run", launchID)
	for _, fixed := range []string{paths.Workspace, paths.Run, paths.Home} {
		info, err := os.Lstat(fixed)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return fmt.Errorf("materialization destination is not a real directory")
		}
	}
	cmd := exec.Command(s.cfg.MaterializerPath, "_isolation-materialize", paths.Workspace, paths.Run, paths.Home)
	cmd.Stdin = bundle
	cmd.Env = []string{
		"HOME=" + paths.Home,
		"PATH=/usr/local/bin:/usr/bin:/bin",
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL}
	if !s.cfg.AllowUnprivileged {
		cmd.SysProcAttr.Credential = &syscall.Credential{
			Uid:         uint32(mapping.UID),
			Gid:         uint32(mapping.GID),
			NoSetGroups: true,
		}
	}
	if output, err := cmd.CombinedOutput(); err != nil {
		message := strings.TrimSpace(string(output))
		if len(message) > 4096 {
			message = message[:4096]
		}
		return fmt.Errorf("target-uid materializer: %w: %s", err, message)
	}
	return nil
}

func (s *helperServer) start(req HelperRequest) (LaunchDescription, []*os.File, error) {
	if !launchPattern.MatchString(req.LaunchID) {
		return LaunchDescription{}, nil, fmt.Errorf("invalid launch id")
	}
	if err := validateLaunchSpec(req.Spec); err != nil {
		return LaunchDescription{}, nil, err
	}
	if err := s.validateMapping(req.Mapping); err != nil {
		return LaunchDescription{}, nil, err
	}
	paths := s.pathsFor(*req.Mapping, "")
	if !pathWithin(paths.Workspace, req.Spec.Dir) {
		return LaunchDescription{}, nil, fmt.Errorf("working directory is outside principal workspace")
	}
	if err := rejectSymlinkPath(paths.Workspace, req.Spec.Dir); err != nil {
		return LaunchDescription{}, nil, err
	}
	// The launch path is resolved by the service user, whose own PATH entries
	// routinely sit behind directories the principal cannot search. execve
	// would report a bare EACCES from inside the re-exec wrapper, long after
	// the caller stopped being able to attribute it.
	if denial := CheckExecAccessForUID(req.Spec.Path, req.Mapping.UID, req.Mapping.GID); denial != nil {
		return LaunchDescription{}, nil, denial
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.draining || s.upgradeGuards > 0 {
		return LaunchDescription{}, nil, fmt.Errorf("helper is draining")
	}
	if launch := s.launches[req.LaunchID]; launch != nil {
		files, err := launch.attachFiles()
		return launch.desc, files, err
	}
	if desc, found, err := s.describeDB(req.LaunchID); err != nil {
		return LaunchDescription{}, nil, err
	} else if found {
		return desc, nil, fmt.Errorf("launch id is already claimed")
	}
	if err := chownTreeSecure(req.Spec.Dir, req.Mapping.UID, req.Mapping.GID, s.cfg.AllowUnprivileged); err != nil {
		return LaunchDescription{}, nil, err
	}
	workspaceID := filepath.Base(req.Spec.Dir)
	runPath := filepath.Join(paths.Root, "run", req.LaunchID)
	if info, err := os.Lstat(runPath); err == nil && info.IsDir() {
		if err := chownTreeSecure(runPath, req.Mapping.UID, req.Mapping.GID, s.cfg.AllowUnprivileged); err != nil {
			return LaunchDescription{}, nil, err
		}
	}
	stdinR, stdinW, err := os.Pipe()
	if err != nil {
		return LaunchDescription{}, nil, err
	}
	stdoutR, stdoutW, err := os.Pipe()
	if err != nil {
		_ = stdinR.Close()
		_ = stdinW.Close()
		return LaunchDescription{}, nil, err
	}
	stderrR, stderrW, err := os.Pipe()
	if err != nil {
		_ = stdinR.Close()
		_ = stdinW.Close()
		_ = stdoutR.Close()
		_ = stdoutW.Close()
		return LaunchDescription{}, nil, err
	}
	closeAll := func() {
		for _, file := range []*os.File{stdinR, stdinW, stdoutR, stdoutW, stderrR, stderrW} {
			_ = file.Close()
		}
	}
	hardening := s.resolveHardening(*req.Mapping)
	commandPath := req.Spec.Path
	commandArgs := append([]string(nil), req.Spec.Args...)
	commandEnv := append([]string(nil), req.Spec.Env...)
	commandEnv = dropLaunchEnv(commandEnv,
		MountViewRootEnv, MountViewAccountEnv, MountViewEnabledEnv,
		PIDNamespaceEnabledEnv, SeccompEnabledEnv)
	if hardening["mount_namespace"] {
		commandEnv = setTrustedLaunchEnv(commandEnv, MountViewRootEnv, s.cfg.PrincipalRoot)
		commandEnv = setTrustedLaunchEnv(commandEnv, MountViewAccountEnv, req.Mapping.Account)
		commandEnv = setTrustedLaunchEnv(commandEnv, MountViewEnabledEnv, "1")
	}
	if hardening["pid_namespace"] {
		commandEnv = setTrustedLaunchEnv(commandEnv, PIDNamespaceEnabledEnv, "1")
	}
	if hardening["seccomp"] {
		commandEnv = setTrustedLaunchEnv(commandEnv, SeccompEnabledEnv, "1")
	}
	if hardening["pid_namespace"] || hardening["seccomp"] || hardening["mount_namespace"] {
		commandArgs = append([]string{"_exec", commandPath}, commandArgs...)
		commandPath = s.cfg.ExecWrapperPath
	}
	cmd := exec.Command(commandPath, commandArgs...)
	cmd.Dir = req.Spec.Dir
	cmd.Env = commandEnv
	cmd.Stdin, cmd.Stdout, cmd.Stderr = stdinR, stdoutW, stderrW
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Pdeathsig: syscall.SIGKILL}
	if !s.cfg.AllowUnprivileged && (hardening["pid_namespace"] || hardening["mount_namespace"]) {
		cmd.SysProcAttr.Cloneflags = syscall.CLONE_NEWUSER
		if hardening["pid_namespace"] {
			// A PID namespace needs a matching procfs mount. Give the launch a
			// private mount namespace even when the principal mount view is off.
			cmd.SysProcAttr.Cloneflags |= syscall.CLONE_NEWPID | syscall.CLONE_NEWNS
		}
		if hardening["mount_namespace"] {
			cmd.SysProcAttr.Cloneflags |= syscall.CLONE_NEWNS
		}
		cmd.SysProcAttr.UidMappings = []syscall.SysProcIDMap{{ContainerID: 0, HostID: req.Mapping.UID, Size: 1}}
		cmd.SysProcAttr.GidMappings = []syscall.SysProcIDMap{{ContainerID: 0, HostID: req.Mapping.GID, Size: 1}}
		cmd.SysProcAttr.GidMappingsEnableSetgroups = false
		cmd.SysProcAttr.Credential = &syscall.Credential{Uid: 0, Gid: 0, NoSetGroups: true}
	} else if !s.cfg.AllowUnprivileged {
		cmd.SysProcAttr.Credential = &syscall.Credential{
			Uid:         uint32(req.Mapping.UID),
			Gid:         uint32(req.Mapping.GID),
			NoSetGroups: true,
		}
	}
	started := time.Now().UTC()
	if err := cmd.Start(); err != nil {
		closeAll()
		return LaunchDescription{}, nil, err
	}
	_ = stdinR.Close()
	_ = stdoutW.Close()
	_ = stderrW.Close()
	pidStart, err := readPIDStartTime(cmd.Process.Pid)
	if err != nil {
		_ = cmd.Process.Kill()
		closeAll()
		return LaunchDescription{}, nil, err
	}
	desc := LaunchDescription{
		LaunchID:  req.LaunchID,
		Principal: req.Mapping.PrincipalID,
		Account:   req.Mapping.Account,
		UID:       req.Mapping.UID,
		PID:       cmd.Process.Pid,
		PIDStart:  pidStart,
		State:     "running",
		StartedAt: started,
		Paths:     s.pathsFor(*req.Mapping, workspaceID),
		Attached:  true,
	}
	desc.Paths.Run = runPath
	if _, err := s.db.Exec(`INSERT INTO launches(
launch_id,principal_id,account,uid,host_pid,pid_start,started_at,state,ack_state)
VALUES(?,?,?,?,?,?,?,?,?)`, desc.LaunchID, desc.Principal, desc.Account, desc.UID, desc.PID,
		desc.PIDStart, desc.StartedAt.UnixNano(), desc.State, "started"); err != nil {
		_ = cmd.Process.Kill()
		closeAll()
		return LaunchDescription{}, nil, err
	}
	launch := &helperLaunch{
		desc: desc, cmd: cmd, done: make(chan struct{}),
		stdin: stdinW, stdout: stdoutR, stderr: stderrR,
	}
	s.launches[desc.LaunchID] = launch
	files, err := launch.attachFiles()
	if err != nil {
		_ = cmd.Process.Kill()
		closeAll()
		delete(s.launches, desc.LaunchID)
		return LaunchDescription{}, nil, err
	}
	go s.reap(launch)
	return desc, files, nil
}

func (s *helperServer) resolveHardening(mapping AccountMapping) map[string]bool {
	result := map[string]bool{
		"pid_namespace":   false,
		"mount_namespace": false,
		"seccomp":         false,
	}
	if s.cfg.AllowUnprivileged {
		return result
	}
	// Probing forks several namespace/mount test processes, so cache the answer
	// per account: it is a property of the kernel and the principal's own root,
	// not of an individual launch.
	s.hardeningMu.Lock()
	cached, ok := s.hardeningByAccount[mapping.Account]
	s.hardeningMu.Unlock()
	if ok {
		return cached
	}
	if s.cfg.PIDNamespace {
		if strings.TrimSpace(s.cfg.ExecWrapperPath) == "" {
			s.recordHardening("pid_namespace", false, "exec wrapper unavailable")
		} else {
			result["pid_namespace"] = s.probePIDNamespace(mapping)
		}
	}
	if s.cfg.MountNamespace {
		if strings.TrimSpace(s.cfg.ExecWrapperPath) == "" {
			s.recordHardening("mount_namespace", false, "exec wrapper unavailable")
		} else {
			result["mount_namespace"] = s.probeMountView(mapping)
		}
	}
	if s.cfg.Seccomp {
		if strings.TrimSpace(s.cfg.ExecWrapperPath) == "" {
			s.recordHardening("seccomp", false, "exec wrapper unavailable")
		} else {
			result["seccomp"] = true
			s.recordHardening("seccomp", true, "")
		}
	}
	s.hardeningMu.Lock()
	s.hardeningByAccount[mapping.Account] = result
	s.hardeningMu.Unlock()
	return result
}

func (s *helperServer) probeMountView(mapping AccountMapping) bool {
	cmd := exec.Command(s.cfg.ExecWrapperPath, "_probe_mount")
	cmd.Env = []string{
		MountViewRootEnv + "=" + s.cfg.PrincipalRoot,
		MountViewAccountEnv + "=" + mapping.Account,
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Cloneflags:                 syscall.CLONE_NEWUSER | syscall.CLONE_NEWNS,
		UidMappings:                []syscall.SysProcIDMap{{ContainerID: 0, HostID: mapping.UID, Size: 1}},
		GidMappings:                []syscall.SysProcIDMap{{ContainerID: 0, HostID: mapping.GID, Size: 1}},
		GidMappingsEnableSetgroups: false,
		Credential:                 &syscall.Credential{Uid: 0, Gid: 0, NoSetGroups: true},
	}
	if output, err := cmd.CombinedOutput(); err != nil {
		reason := strings.TrimSpace(string(output))
		if reason != "" {
			reason += ": "
		}
		s.recordHardening("mount_namespace", false, reason+err.Error())
		return false
	}
	s.recordHardening("mount_namespace", true, "")
	return true
}

func (s *helperServer) probePIDNamespace(mapping AccountMapping) bool {
	cmd := exec.Command(s.cfg.ExecWrapperPath, "_probe_pid_namespace")
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Cloneflags:                 syscall.CLONE_NEWUSER | syscall.CLONE_NEWPID | syscall.CLONE_NEWNS,
		UidMappings:                []syscall.SysProcIDMap{{ContainerID: 0, HostID: mapping.UID, Size: 1}},
		GidMappings:                []syscall.SysProcIDMap{{ContainerID: 0, HostID: mapping.GID, Size: 1}},
		GidMappingsEnableSetgroups: false,
		Credential:                 &syscall.Credential{Uid: 0, Gid: 0, NoSetGroups: true},
	}
	if output, err := cmd.CombinedOutput(); err != nil {
		reason := strings.TrimSpace(string(output))
		if reason != "" {
			reason += ": "
		}
		s.recordHardening("pid_namespace", false, reason+err.Error())
		return false
	}
	s.recordHardening("pid_namespace", true, "")
	return true
}

func setTrustedLaunchEnv(env []string, key, value string) []string {
	prefix := key + "="
	out := env[:0]
	for _, entry := range env {
		if !strings.HasPrefix(entry, prefix) {
			out = append(out, entry)
		}
	}
	return append(out, prefix+value)
}

func (s *helperServer) recordHardening(name string, enabled bool, reason string) {
	s.hardeningMu.Lock()
	defer s.hardeningMu.Unlock()
	s.hardening[name] = enabled
	if reason == "" {
		delete(s.degradedHardening, name)
	} else {
		s.degradedHardening[name] = name + ": " + reason
	}
}

func (s *helperServer) hardeningStatus() (map[string]bool, []string) {
	s.hardeningMu.Lock()
	defer s.hardeningMu.Unlock()
	// Hardening is probed on the first launch for a principal. Reporting the
	// zero map before that would read as "every layer failed", so say plainly
	// that nothing has been measured yet.
	if len(s.hardeningByAccount) == 0 {
		return nil, nil
	}
	out := make(map[string]bool, len(s.hardening))
	for key, enabled := range s.hardening {
		out[key] = enabled
	}
	var degraded []string
	for _, reason := range s.degradedHardening {
		degraded = append(degraded, reason)
	}
	sort.Strings(degraded)
	return out, degraded
}

func (l *helperLaunch) attachFiles() ([]*os.File, error) {
	if l.stdin == nil || l.stdout == nil || l.stderr == nil {
		return nil, fmt.Errorf("launch stdio is no longer attachable")
	}
	var files []*os.File
	for _, source := range []*os.File{l.stdin, l.stdout, l.stderr} {
		fd, err := unix.Dup(int(source.Fd()))
		if err != nil {
			for _, file := range files {
				_ = file.Close()
			}
			return nil, err
		}
		unix.CloseOnExec(fd)
		files = append(files, os.NewFile(uintptr(fd), "amux-isolation-attach"))
	}
	return files, nil
}

func (s *helperServer) reap(launch *helperLaunch) {
	err := launch.cmd.Wait()
	status := exitStatus(launch.cmd.ProcessState, launch.desc.StartedAt, time.Now().UTC())
	s.mu.Lock()
	launch.desc.State = "exited"
	launch.desc.ExitStatus = &status
	launch.closeFiles()
	close(launch.done)
	s.mu.Unlock()
	statusJSON, _ := json.Marshal(status)
	errorCode := ""
	if err != nil {
		errorCode = "process_exit"
	}
	_, _ = s.db.Exec(`UPDATE launches SET state='exited',exit_status_json=?,error_code=? WHERE launch_id=?`,
		string(statusJSON), errorCode, launch.desc.LaunchID)
}

func (l *helperLaunch) closeFiles() {
	for _, file := range []*os.File{l.stdin, l.stdout, l.stderr} {
		if file != nil {
			_ = file.Close()
		}
	}
	l.stdin, l.stdout, l.stderr = nil, nil, nil
}

func (s *helperServer) describe(launchID string) (LaunchDescription, error) {
	if !launchPattern.MatchString(launchID) {
		return LaunchDescription{}, ErrNotFound
	}
	s.mu.Lock()
	if launch := s.launches[launchID]; launch != nil {
		desc := launch.desc
		s.mu.Unlock()
		return desc, nil
	}
	s.mu.Unlock()
	desc, found, err := s.describeDB(launchID)
	if err != nil {
		return LaunchDescription{}, err
	}
	if !found {
		return LaunchDescription{}, ErrNotFound
	}
	if desc.State == "isolation_lost" {
		return desc, ErrIsolationLost
	}
	return desc, nil
}

func (s *helperServer) describeDB(launchID string) (LaunchDescription, bool, error) {
	var desc LaunchDescription
	var started int64
	var exitJSON, errorCode string
	err := s.db.QueryRow(`SELECT launch_id,principal_id,account,uid,host_pid,pid_start,started_at,state,exit_status_json,error_code
FROM launches WHERE launch_id=?`, launchID).
		Scan(&desc.LaunchID, &desc.Principal, &desc.Account, &desc.UID, &desc.PID, &desc.PIDStart,
			&started, &desc.State, &exitJSON, &errorCode)
	if err == sql.ErrNoRows {
		return LaunchDescription{}, false, nil
	}
	if err != nil {
		return LaunchDescription{}, false, err
	}
	desc.StartedAt = time.Unix(0, started).UTC()
	if exitJSON != "" {
		var status ExitStatus
		if err := json.Unmarshal([]byte(exitJSON), &status); err != nil {
			return LaunchDescription{}, false, err
		}
		desc.ExitStatus = &status
	}
	return desc, true, nil
}

func (s *helperServer) wait(launchID string) (LaunchDescription, error) {
	s.mu.Lock()
	launch := s.launches[launchID]
	if launch != nil {
		done := launch.done
		s.mu.Unlock()
		<-done
		s.mu.Lock()
		desc := launch.desc
		s.mu.Unlock()
		return desc, nil
	}
	s.mu.Unlock()
	desc, err := s.describe(launchID)
	if err != nil {
		return LaunchDescription{}, err
	}
	if desc.ExitStatus == nil {
		return LaunchDescription{}, ErrIsolationLost
	}
	return desc, nil
}

func (s *helperServer) signal(launchID, name string) error {
	s.mu.Lock()
	launch := s.launches[launchID]
	if launch == nil || launch.desc.State != "running" {
		s.mu.Unlock()
		desc, err := s.describe(launchID)
		if err != nil {
			return err
		}
		if desc.State == "exited" {
			return nil
		}
		return ErrIsolationLost
	}
	desc := launch.desc
	s.mu.Unlock()
	currentStart, err := readPIDStartTime(desc.PID)
	if err != nil || currentStart != desc.PIDStart {
		return ErrIsolationLost
	}
	signal, err := parseSignal(name)
	if err != nil {
		return err
	}
	sysSignal, ok := signal.(syscall.Signal)
	if !ok {
		return fmt.Errorf("unsupported signal")
	}
	if err := unix.Kill(-desc.PID, sysSignal); err != nil && !errors.Is(err, unix.ESRCH) {
		return err
	}
	return nil
}

func (s *helperServer) closeHandle(launchID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	launch := s.launches[launchID]
	if launch == nil {
		return nil
	}
	launch.closeFiles()
	_, _ = s.db.Exec(`UPDATE launches SET ack_state='closed' WHERE launch_id=?`, launchID)
	return nil
}

func (s *helperServer) removeRun(mapping *AccountMapping, executionID string) error {
	if err := s.validateMapping(mapping); err != nil {
		return err
	}
	if !executionPattern.MatchString(executionID) {
		return fmt.Errorf("invalid execution id")
	}
	path := s.pathsFor(*mapping, executionID).Run
	if err := rejectSymlinkPath(filepath.Join(s.pathsFor(*mapping, "").Root, "run"), path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return os.RemoveAll(path)
}

// removeWorkspace reclaims one execution's workspace while leaving the account
// root and its long-lived HOME in place.
func (s *helperServer) removeWorkspace(mapping *AccountMapping, executionID string) error {
	if err := s.validateMapping(mapping); err != nil {
		return err
	}
	if !executionPattern.MatchString(executionID) {
		return fmt.Errorf("invalid execution id")
	}
	root := s.pathsFor(*mapping, "").Workspace
	path := s.pathsFor(*mapping, executionID).Workspace
	if err := rejectSymlinkPath(root, path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return os.RemoveAll(path)
}

// reclaimHome deletes oversized provider caches from a principal HOME. The tree
// is owned by the principal at 0700; the service user cannot rotate anything in
// it, so the request describes what to reclaim and the helper performs it.
func (s *helperServer) reclaimHome(mapping *AccountMapping, caches []HomeCacheSpec) error {
	if err := s.validateMapping(mapping); err != nil {
		return err
	}
	home := s.pathsFor(*mapping, "").Home
	for _, cache := range caches {
		gate, err := homeCachePath(home, cache.Path)
		if err != nil {
			return err
		}
		info, err := os.Lstat(gate)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("home cache %q is not a regular file", cache.Path)
		}
		if cache.MaxBytes > 0 && info.Size() <= cache.MaxBytes {
			continue
		}
		for _, rel := range cache.Remove {
			target, err := homeCachePath(home, rel)
			if err != nil {
				return err
			}
			if err := rejectSymlinkPath(home, target); err != nil && !os.IsNotExist(err) {
				return err
			}
			if err := os.RemoveAll(target); err != nil {
				return err
			}
		}
	}
	return nil
}

func homeCachePath(home, rel string) (string, error) {
	rel = filepath.Clean(strings.TrimSpace(rel))
	if rel == "" || rel == "." || rel == ".." || filepath.IsAbs(rel) ||
		strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("invalid home cache path %q", rel)
	}
	target := filepath.Join(home, rel)
	if !pathWithin(home, target) {
		return "", fmt.Errorf("home cache path escapes the principal home")
	}
	return target, nil
}

func (s *helperServer) killPrincipal(mapping *AccountMapping) error {
	if err := s.validateMapping(mapping); err != nil {
		return err
	}
	s.mu.Lock()
	var ids []string
	for id, launch := range s.launches {
		if launch.desc.Principal == mapping.PrincipalID && launch.desc.State == "running" {
			ids = append(ids, id)
		}
	}
	s.mu.Unlock()
	for _, id := range ids {
		if err := s.signal(id, "KILL"); err != nil && !errors.Is(err, ErrNotFound) {
			return err
		}
	}
	// Tracked launches only cover process groups this helper instance started.
	// Revocation must also reach descendants that left their process group and
	// launches inherited across a helper restart, so finish by clearing every
	// process still running as the principal's dedicated uid.
	return s.killByUID(mapping.UID)
}

// killByUID is the kernel-precise half of revocation: the dedicated uid is the
// principal's identity, so a sweep over /proc leaves nothing behind. The uid is
// re-checked against the configured range because the helper must never be
// usable to signal real user or system accounts.
func (s *helperServer) killByUID(uid int) error {
	if s.cfg.AllowUnprivileged {
		return nil
	}
	if uid < s.cfg.UIDRangeStart || uid > s.cfg.UIDRangeEnd {
		return fmt.Errorf("uid is outside the configured principal range")
	}
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return err
	}
	self := os.Getpid()
	for _, entry := range entries {
		pid, convErr := strconv.Atoi(entry.Name())
		if convErr != nil || pid <= 1 || pid == self {
			continue
		}
		var stat unix.Stat_t
		if err := unix.Stat(filepath.Join("/proc", entry.Name()), &stat); err != nil {
			continue // the process exited between readdir and stat
		}
		if int(stat.Uid) != uid {
			continue
		}
		if err := unix.Kill(pid, unix.SIGKILL); err != nil &&
			!errors.Is(err, unix.ESRCH) && !errors.Is(err, unix.EPERM) {
			return err
		}
	}
	return nil
}

func (s *helperServer) killAll() {
	s.mu.Lock()
	var ids []string
	for id, launch := range s.launches {
		if launch.desc.State == "running" {
			ids = append(ids, id)
		}
	}
	s.mu.Unlock()
	for _, id := range ids {
		_ = s.signal(id, "KILL")
	}
}

func (s *helperServer) listAccounts() ([]AccountMapping, error) {
	rows, err := s.db.Query(`SELECT principal_id,account,uid,gid,created_at,last_used_at
FROM helper_mappings ORDER BY uid`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AccountMapping
	for rows.Next() {
		var mapping AccountMapping
		var created, used int64
		if err := rows.Scan(&mapping.PrincipalID, &mapping.Account, &mapping.UID, &mapping.GID, &created, &used); err != nil {
			return nil, err
		}
		mapping.CreatedAt = time.Unix(0, created).UTC()
		mapping.LastUsedAt = time.Unix(0, used).UTC()
		out = append(out, mapping)
	}
	return out, rows.Err()
}

func (s *helperServer) removeAccount(mapping *AccountMapping) error {
	if err := s.validateMapping(mapping); err != nil {
		return err
	}
	s.mu.Lock()
	for _, launch := range s.launches {
		if launch.desc.Principal == mapping.PrincipalID && launch.desc.State == "running" {
			s.mu.Unlock()
			return fmt.Errorf("principal still has a running launch")
		}
	}
	s.mu.Unlock()
	var active int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM launches WHERE principal_id=? AND state IN ('starting','running')`,
		mapping.PrincipalID).Scan(&active); err != nil {
		return err
	}
	if active != 0 {
		return fmt.Errorf("principal still has an active registry launch")
	}
	root := s.pathsFor(*mapping, "").Root
	info, err := os.Lstat(root)
	if err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return fmt.Errorf("principal root is not a real directory")
		}
		if err := os.RemoveAll(root); err != nil {
			return err
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	if !s.cfg.SkipAccountProvision {
		if output, err := exec.Command("userdel", mapping.Account).CombinedOutput(); err != nil &&
			!strings.Contains(string(output), "does not exist") {
			return fmt.Errorf("userdel: %w: %s", err, strings.TrimSpace(string(output)))
		}
		if output, err := exec.Command("groupdel", mapping.Account).CombinedOutput(); err != nil &&
			!strings.Contains(string(output), "does not exist") {
			return fmt.Errorf("groupdel: %w: %s", err, strings.TrimSpace(string(output)))
		}
	}
	if _, err := s.db.Exec(`DELETE FROM launches WHERE principal_id=?`, mapping.PrincipalID); err != nil {
		return err
	}
	_, err = s.db.Exec(`DELETE FROM helper_mappings WHERE principal_id=?`, mapping.PrincipalID)
	return err
}

func rejectSymlinkPath(root, path string) error {
	if !pathWithin(root, path) {
		return fmt.Errorf("path escapes fixed root")
	}
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return err
	}
	current := root
	for _, part := range strings.Split(rel, string(os.PathSeparator)) {
		if part == "" || part == "." {
			continue
		}
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("symlink is not allowed in isolation path")
		}
	}
	return nil
}

// chownTreeSecure re-owns a helper-prepared tree without ever resolving a
// symlink. Each level is opened with O_NOFOLLOW relative to its parent
// descriptor, so a principal that already owns part of the tree cannot swap a
// component for a symlink and make the root helper chown a path outside it.
func chownTreeSecure(root string, uid, gid int, allowUnprivileged bool) error {
	fd, err := unix.Open(root, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return fmt.Errorf("open %s without following symlinks: %w", root, err)
	}
	defer unix.Close(fd)
	if err := unix.Fchown(fd, uid, gid); err != nil && !allowUnprivileged {
		return err
	}
	return chownDirContents(fd, uid, gid, allowUnprivileged, 0)
}

const maxIsolationTreeDepth = 128

func chownDirContents(dirFD, uid, gid int, allowUnprivileged bool, depth int) error {
	if depth > maxIsolationTreeDepth {
		return fmt.Errorf("materialized tree is nested deeper than %d levels", maxIsolationTreeDepth)
	}
	// Readdirnames consumes the directory offset, so iterate over a duplicate
	// and leave the caller's descriptor rewound for its own openat calls.
	dup, err := unix.Dup(dirFD)
	if err != nil {
		return err
	}
	unix.CloseOnExec(dup)
	dir := os.NewFile(uintptr(dup), "amux-isolation-tree")
	names, readErr := dir.Readdirnames(-1)
	if err := dir.Close(); err != nil && readErr == nil {
		readErr = err
	}
	if readErr != nil {
		return readErr
	}
	for _, name := range names {
		var stat unix.Stat_t
		if err := unix.Fstatat(dirFD, name, &stat, unix.AT_SYMLINK_NOFOLLOW); err != nil {
			return err
		}
		switch stat.Mode & unix.S_IFMT {
		// A symlink is re-owned as the link itself (Fchownat below is
		// AT_SYMLINK_NOFOLLOW) and never descended into, which is the whole
		// escape defense. Rejecting it outright was stricter without being
		// safer, and this tree is the agent's own workspace: one `npm install`
		// leaves symlinks in node_modules/.bin and made the session
		// permanently unlaunchable. Materialized bundles, whose contents amux
		// controls, are still validated strictly in ApplyMaterializeBundle.
		case unix.S_IFLNK, unix.S_IFREG, unix.S_IFDIR:
		default:
			return fmt.Errorf("special file is not allowed in materialized tree")
		}
		if err := unix.Fchownat(dirFD, name, uid, gid, unix.AT_SYMLINK_NOFOLLOW); err != nil && !allowUnprivileged {
			return err
		}
		if stat.Mode&unix.S_IFMT != unix.S_IFDIR {
			continue
		}
		childFD, err := unix.Openat(dirFD, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if err != nil {
			return err
		}
		err = chownDirContents(childFD, uid, gid, allowUnprivileged, depth+1)
		_ = unix.Close(childFD)
		if err != nil {
			return err
		}
	}
	return nil
}

func readPIDStartTime(pid int) (uint64, error) {
	data, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "stat"))
	if err != nil {
		return 0, err
	}
	end := strings.LastIndexByte(string(data), ')')
	if end < 0 || end+2 >= len(data) {
		return 0, fmt.Errorf("invalid proc stat")
	}
	fields := strings.Fields(string(data[end+2:]))
	if len(fields) <= 19 {
		return 0, fmt.Errorf("short proc stat")
	}
	return strconv.ParseUint(fields[19], 10, 64)
}
