package scheduler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"

	"aegisvision/backend/internal/domain"
	"aegisvision/backend/internal/platform/mediamtx"
	platformredis "aegisvision/backend/internal/platform/redis"
)

// Snapshot contains all state needed for one reconciliation cycle.
type Snapshot struct {
	Cameras  []CameraInfo
	Versions []VersionInfo
	ROIs     []ROIInfo
	Rules    []RuleInfo
	Nodes    []NodeInfo
	Tasks    []ExistingTaskInfo
}

// StateStore abstracts PostgreSQL reads and writes used by aegis-scheduler.
type StateStore interface {
	LoadSnapshot(ctx context.Context) (Snapshot, error)
	MarkNodeOffline(ctx context.Context, nodeID string) error
	SaveTaskAssignment(ctx context.Context, task ExistingTaskInfo) error
	StopTask(ctx context.Context, taskID uuid.UUID) error
}

// CommandBus routes commands to the ingest instance holding a target Agent's gRPC stream.
type CommandBus interface {
	PublishAgentCommand(ctx context.Context, ingestInstanceID string, body []byte) error
}

// MediaMTXSyncer synchronizes camera paths and builds authorized RTSP URLs.
type MediaMTXSyncer interface {
	UpsertPath(ctx context.Context, spec mediamtx.PathSpec) error
	DeletePath(ctx context.Context, streamPath string) error
	BuildAuthorizedRTSPURL(streamPath, token string) string
}

// RedisCoordinator abstracts leader election, agent routing, and pull token storage.
type RedisCoordinator interface {
	AcquireSchedulerLeader(ctx context.Context, instanceID string, ttl time.Duration) (bool, error)
	RenewSchedulerLeader(ctx context.Context, instanceID string, ttl time.Duration) (bool, error)
	ReleaseSchedulerLeader(ctx context.Context, instanceID string) error
	GetAgentRoute(ctx context.Context, nodeID string) (string, error)
	SetStreamToken(ctx context.Context, kind, token string, payload platformredis.StreamTokenPayload, ttl time.Duration) error
}

// ReconcilerDeps holds explicit dependencies for Reconciler.
type ReconcilerDeps struct {
	InstanceID        string
	State             StateStore
	Redis             RedisCoordinator
	Bus               CommandBus
	MediaMTX          MediaMTXSyncer
	Logger            *slog.Logger
	LeaderTTL         time.Duration
	LeaderRenewEvery  time.Duration
	ReconcileInterval time.Duration
	LeaseTTL          time.Duration
	NodeTimeout       time.Duration
	NowFunc           func() time.Time
}

// Reconciler runs the leader-elected global task reconciliation, failover, and lease loop.
type Reconciler struct {
	instanceID        string
	state             StateStore
	redis             RedisCoordinator
	bus               CommandBus
	mediamtx          MediaMTXSyncer
	log               *slog.Logger
	leaderTTL         time.Duration
	leaderRenewEvery  time.Duration
	reconcileInterval time.Duration
	leaseTTL          time.Duration
	nodeTimeout       time.Duration
	now               func() time.Time

	isLeader  atomic.Bool
	triggerCh chan struct{}

	specMu       sync.Mutex
	lastSpecHash map[uuid.UUID]string
}

// NewReconciler creates a Reconciler with sensible defaults for omitted timings.
func NewReconciler(deps ReconcilerDeps) *Reconciler {
	if deps.Logger == nil {
		deps.Logger = slog.Default()
	}
	if deps.LeaderTTL <= 0 {
		deps.LeaderTTL = 10 * time.Second
	}
	if deps.LeaderRenewEvery <= 0 {
		deps.LeaderRenewEvery = 3 * time.Second
	}
	if deps.ReconcileInterval <= 0 {
		deps.ReconcileInterval = 5 * time.Second
	}
	if deps.LeaseTTL <= 0 {
		deps.LeaseTTL = 30 * time.Second
	}
	if deps.NodeTimeout <= 0 {
		deps.NodeTimeout = 15 * time.Second
	}
	if deps.NowFunc == nil {
		deps.NowFunc = func() time.Time { return time.Now().UTC() }
	}
	return &Reconciler{
		instanceID:        deps.InstanceID,
		state:             deps.State,
		redis:             deps.Redis,
		bus:               deps.Bus,
		mediamtx:          deps.MediaMTX,
		log:               deps.Logger,
		leaderTTL:         deps.LeaderTTL,
		leaderRenewEvery:  deps.LeaderRenewEvery,
		reconcileInterval: deps.ReconcileInterval,
		leaseTTL:          deps.LeaseTTL,
		nodeTimeout:       deps.NodeTimeout,
		now:               deps.NowFunc,
		triggerCh:         make(chan struct{}, 1),
		lastSpecHash:      make(map[uuid.UUID]string),
	}
}

// IsLeader reports whether this instance currently holds the scheduler leader lock.
func (r *Reconciler) IsLeader() bool {
	return r.isLeader.Load()
}

// TriggerReconcile requests an immediate out-of-band reconciliation cycle (e.g. on config event).
func (r *Reconciler) TriggerReconcile() {
	select {
	case r.triggerCh <- struct{}{}:
	default:
	}
}

// Run executes leader election and, while leader, runs reconciliation and lease renewal until ctx ends.
func (r *Reconciler) Run(ctx context.Context) error {
	defer func() {
		if r.isLeader.Swap(false) {
			relCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			if err := r.redis.ReleaseSchedulerLeader(relCtx, r.instanceID); err != nil {
				r.log.Warn("failed to release scheduler leader lock", "err", err)
			}
		}
	}()

	leaseRenewInterval := r.leaseTTL / 3
	if leaseRenewInterval < 200*time.Millisecond {
		leaseRenewInterval = 200 * time.Millisecond
	}

	leaderTick := time.NewTicker(r.leaderRenewEvery)
	defer leaderTick.Stop()
	reconcileTick := time.NewTicker(r.reconcileInterval)
	defer reconcileTick.Stop()
	leaseTick := time.NewTicker(leaseRenewInterval)
	defer leaseTick.Stop()

	r.tryAcquireOrRenew(ctx)
	if r.IsLeader() {
		if err := r.ReconcileOnce(ctx); err != nil {
			r.log.Warn("initial reconcile failed", "err", err)
		}
	}

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-leaderTick.C:
			wasLeader := r.IsLeader()
			r.tryAcquireOrRenew(ctx)
			if !wasLeader && r.IsLeader() {
				if err := r.ReconcileOnce(ctx); err != nil {
					r.log.Warn("reconcile on leader takeover failed", "err", err)
				}
			}
		case <-r.triggerCh:
			if r.IsLeader() {
				if err := r.ReconcileOnce(ctx); err != nil {
					r.log.Warn("triggered reconcile failed", "err", err)
				}
			}
		case <-reconcileTick.C:
			if r.IsLeader() {
				if err := r.ReconcileOnce(ctx); err != nil {
					r.log.Warn("periodic reconcile failed", "err", err)
				}
			}
		case <-leaseTick.C:
			if r.IsLeader() {
				if err := r.RenewLeasesOnce(ctx); err != nil {
					r.log.Warn("lease renewal tick failed", "err", err)
				}
			}
		}
	}
}

func (r *Reconciler) tryAcquireOrRenew(ctx context.Context) {
	if r.IsLeader() {
		ok, err := r.redis.RenewSchedulerLeader(ctx, r.instanceID, r.leaderTTL)
		if err != nil || !ok {
			r.isLeader.Store(false)
			r.log.Warn("lost scheduler leadership", "err", err)
		}
		return
	}

	ok, err := r.redis.AcquireSchedulerLeader(ctx, r.instanceID, r.leaderTTL)
	if err != nil {
		r.log.Warn("acquire scheduler leader lock error", "err", err)
		return
	}
	if ok {
		r.isLeader.Store(true)
		r.log.Info("acquired scheduler leadership", "instance_id", r.instanceID)
	}
}

// ReconcileOnce performs a single deterministic pass:
// 1. Mark timed-out nodes offline
// 2. Sync MediaMTX camera paths (with RecordVideo derived from enabled rules)
// 3. Compute deduplicated desired tasks and placements
// 4. Stop/revoke obsolete tasks and assign/migrate active tasks with authorized RTSP URLs
func (r *Reconciler) ReconcileOnce(ctx context.Context) error {
	snap, err := r.state.LoadSnapshot(ctx)
	if err != nil {
		return fmt.Errorf("load scheduler snapshot: %w", err)
	}

	now := r.now()

	// 1. Mark timed-out online nodes offline before planning
	for i := range snap.Nodes {
		n := &snap.Nodes[i]
		if n.Status == domain.NodeStatusOnline && !IsNodeHealthy(*n, now, r.nodeTimeout) {
			if err := r.state.MarkNodeOffline(ctx, n.NodeID); err != nil {
				r.log.Warn("mark node offline failed", "node_id", n.NodeID, "err", err)
			}
			n.Status = domain.NodeStatusOffline
			r.log.Warn("node heartbeat timed out; marked offline for failover", "node_id", n.NodeID)
		}
	}

	camMap := make(map[uuid.UUID]CameraInfo, len(snap.Cameras))
	for _, c := range snap.Cameras {
		camMap[c.ID] = c
	}
	verMap := make(map[uuid.UUID]VersionInfo, len(snap.Versions))
	for _, v := range snap.Versions {
		verMap[v.ID] = v
	}
	roiMap := make(map[uuid.UUID]ROIInfo, len(snap.ROIs))
	for _, roi := range snap.ROIs {
		roiMap[roi.ID] = roi
	}

	// 2. Compute desired deduplicated tasks
	desiredList := ComputeDesiredTasks(PlanInput{
		Now:           now,
		NodeTimeout:   r.nodeTimeout,
		Cameras:       camMap,
		Versions:      verMap,
		ROIs:          roiMap,
		Rules:         snap.Rules,
		Nodes:         snap.Nodes,
		ExistingTasks: snap.Tasks,
	})

	// Determine per-camera record_video requirement for MediaMTX short-term ring buffer
	camRecordVideo := make(map[uuid.UUID]bool)
	desiredByKey := make(map[TaskKey]DesiredTask, len(desiredList))
	for _, dt := range desiredList {
		desiredByKey[TaskKey{CameraID: dt.CameraID, AlgorithmVersionID: dt.AlgorithmVersionID}] = dt
		if dt.RecordVideo {
			camRecordVideo[dt.CameraID] = true
		}
	}

	// 3. Sync MediaMTX main & optional sub-stream paths
	if r.mediamtx != nil {
		for _, cam := range snap.Cameras {
			subPath := cam.StreamPath + "_sub"
			if cam.Enabled {
				if err := r.mediamtx.UpsertPath(ctx, mediamtx.PathSpec{
					StreamPath:  cam.StreamPath,
					SourceURL:   cam.SourceURL,
					RecordVideo: camRecordVideo[cam.ID],
				}); err != nil {
					r.log.Warn("mediamtx upsert path failed", "camera_id", cam.ID, "stream_path", cam.StreamPath, "err", err)
				}
				if cam.SubStreamURL != "" {
					if err := r.mediamtx.UpsertPath(ctx, mediamtx.PathSpec{
						StreamPath:  subPath,
						SourceURL:   cam.SubStreamURL,
						RecordVideo: false,
					}); err != nil {
						r.log.Warn("mediamtx upsert sub-stream path failed", "camera_id", cam.ID, "stream_path", subPath, "err", err)
					}
				}
			} else {
				if err := r.mediamtx.DeletePath(ctx, cam.StreamPath); err != nil {
					r.log.Warn("mediamtx delete path failed", "camera_id", cam.ID, "stream_path", cam.StreamPath, "err", err)
				}
				if cam.SubStreamURL != "" {
					if err := r.mediamtx.DeletePath(ctx, subPath); err != nil {
						r.log.Warn("mediamtx delete sub-stream path failed", "camera_id", cam.ID, "stream_path", subPath, "err", err)
					}
				}
			}
		}
	}

	// Index existing running tasks
	existingRunning := make(map[TaskKey]ExistingTaskInfo)
	for _, tk := range snap.Tasks {
		if tk.DesiredState == domain.TaskDesiredRunning {
			existingRunning[TaskKey{CameraID: tk.CameraID, AlgorithmVersionID: tk.AlgorithmVersionID}] = tk
		}
	}

	// 4. Stop/revoke tasks no longer desired
	for key, oldTask := range existingRunning {
		if _, stillDesired := desiredByKey[key]; !stillDesired {
			if err := r.state.StopTask(ctx, oldTask.ID); err != nil {
				r.log.Warn("stop obsolete task failed", "task_id", oldTask.ID, "err", err)
			}
			r.deleteSpecHash(oldTask.ID)
			r.sendRevoke(ctx, oldTask, "rule disabled or removed")
		}
	}

	// 5. Assign new tasks or migrate/update existing tasks
	leaseTTLSec := int32(r.leaseTTL.Seconds()) // #nosec G115 -- lease TTL is seconds-scale
	for _, dt := range desiredList {
		key := TaskKey{CameraID: dt.CameraID, AlgorithmVersionID: dt.AlgorithmVersionID}
		existing, exists := existingRunning[key]

		if dt.AssignedNodeID == "" {
			if exists && existing.AssignedNodeID != "" {
				r.sendRevoke(ctx, existing, "assigned node unavailable")
			}
			r.log.Warn("desired task unassigned", "camera_id", dt.CameraID, "algorithm_version_id", dt.AlgorithmVersionID, "reason", dt.UnassignedReason)
			continue
		}

		// Migrate if previously assigned to a different node (e.g., node failover or drain)
		nodeChanged := !exists || existing.AssignedNodeID != dt.AssignedNodeID
		if exists && existing.AssignedNodeID != "" && existing.AssignedNodeID != dt.AssignedNodeID {
			r.sendRevoke(ctx, existing, "migrated to node "+dt.AssignedNodeID)
		}

		ingestID, routeErr := r.redis.GetAgentRoute(ctx, dt.AssignedNodeID)
		if routeErr != nil && !errors.Is(routeErr, platformredis.ErrKeyNotFound) {
			r.log.Warn("lookup agent route failed", "node_id", dt.AssignedNodeID, "err", routeErr)
		}

		taskID := existing.ID
		configVer := existing.ConfigVersion
		if !exists {
			taskID = uuid.Must(uuid.NewV7())
			configVer = 1
		} else if nodeChanged {
			configVer++
		}

		specSig := taskSpecSignature(dt)
		prevSig := r.getSpecHash(taskID)
		needsAssign := nodeChanged || existing.ActualState == domain.TaskActualPending
		needsUpdate := !needsAssign && prevSig != "" && prevSig != specSig
		if needsUpdate {
			configVer++
		}

		leaseID := uuid.Must(uuid.NewV7())
		leaseExp := now.Add(r.leaseTTL)
		if !nodeChanged && existing.LeaseID.Valid {
			leaseID = existing.LeaseID.UUID
		}

		updatedTask := ExistingTaskInfo{
			ID:                 taskID,
			CameraID:           dt.CameraID,
			AlgorithmVersionID: dt.AlgorithmVersionID,
			RuntimeKey:         dt.RuntimeKey,
			AssignedNodeID:     dt.AssignedNodeID,
			IngestInstanceID:   ingestID,
			DesiredState:       domain.TaskDesiredRunning,
			ActualState:        domain.TaskActualPending,
			LeaseID:            uuid.NullUUID{UUID: leaseID, Valid: true},
			LeaseExpiresAt:     &leaseExp,
			ConfigVersion:      configVer,
		}
		if !nodeChanged && existing.ActualState == domain.TaskActualRunning {
			updatedTask.ActualState = domain.TaskActualRunning
		}

		if err := r.state.SaveTaskAssignment(ctx, updatedTask); err != nil {
			r.log.Error("save task assignment failed", "task_id", taskID, "err", err)
			continue
		}

		if needsAssign && ingestID != "" {
			streamURL, err := r.issuePullStreamURL(ctx, taskID, dt.StreamPath)
			if err != nil {
				r.log.Error("issue pull stream url failed", "task_id", taskID, "err", err)
				continue
			}
			if err := r.publishCommand(ctx, ingestID, AgentCommandEnvelope{
				NodeID:      dt.AssignedNodeID,
				CommandType: CmdAssignTask,
				AssignTask: &AssignTaskPayload{
					TaskID:             taskID.String(),
					CameraID:           dt.CameraID.String(),
					AlgorithmVersionID: dt.AlgorithmVersionID.String(),
					RuntimeKey:         dt.RuntimeKey,
					StreamURL:          streamURL,
					StreamProtocol:     "rtsp",
					ROIs:               buildCommandROIs(dt.ROIs),
					AlgoParamsJSON:     dt.AlgoParamsJSON,
					LeaseID:            leaseID.String(),
					LeaseTTLSeconds:    leaseTTLSec,
					ConfigVersion:      configVer,
				},
			}); err != nil {
				r.log.Warn("publish assign task command failed", "task_id", taskID, "err", err)
			} else {
				r.setSpecHash(taskID, specSig)
			}
		} else if needsUpdate && ingestID != "" {
			if err := r.publishCommand(ctx, ingestID, AgentCommandEnvelope{
				NodeID:      dt.AssignedNodeID,
				CommandType: CmdUpdateTask,
				UpdateTask: &UpdateTaskPayload{
					TaskID:         taskID.String(),
					ROIs:           buildCommandROIs(dt.ROIs),
					AlgoParamsJSON: dt.AlgoParamsJSON,
					ConfigVersion:  configVer,
				},
			}); err != nil {
				r.log.Warn("publish update task command failed", "task_id", taskID, "err", err)
			} else {
				r.setSpecHash(taskID, specSig)
			}
		} else if !needsAssign && prevSig == "" {
			r.setSpecHash(taskID, specSig)
		}
	}

	return nil
}

// RenewLeasesOnce extends leases and refreshes authorized RTSP pull URLs for all active assigned tasks.
func (r *Reconciler) RenewLeasesOnce(ctx context.Context) error {
	snap, err := r.state.LoadSnapshot(ctx)
	if err != nil {
		return fmt.Errorf("load snapshot for lease renewal: %w", err)
	}

	camMap := make(map[uuid.UUID]CameraInfo, len(snap.Cameras))
	for _, c := range snap.Cameras {
		camMap[c.ID] = c
	}
	now := r.now()
	leaseTTLSec := int32(r.leaseTTL.Seconds()) // #nosec G115 -- lease TTL is seconds-scale

	for _, tk := range snap.Tasks {
		if tk.DesiredState != domain.TaskDesiredRunning || tk.AssignedNodeID == "" || !tk.LeaseID.Valid {
			continue
		}
		cam, ok := camMap[tk.CameraID]
		if !ok {
			continue
		}

		ingestID := tk.IngestInstanceID
		if route, err := r.redis.GetAgentRoute(ctx, tk.AssignedNodeID); err == nil && route != "" {
			ingestID = route
		}
		if ingestID == "" {
			continue
		}

		refreshedURL, err := r.issuePullStreamURL(ctx, tk.ID, cam.StreamPath)
		if err != nil {
			r.log.Warn("refresh pull stream url failed", "task_id", tk.ID, "err", err)
			continue
		}

		exp := now.Add(r.leaseTTL)
		tk.IngestInstanceID = ingestID
		tk.LeaseExpiresAt = &exp
		if err := r.state.SaveTaskAssignment(ctx, tk); err != nil {
			r.log.Warn("persist renewed lease failed", "task_id", tk.ID, "err", err)
			continue
		}

		if err := r.publishCommand(ctx, ingestID, AgentCommandEnvelope{
			NodeID:      tk.AssignedNodeID,
			CommandType: CmdRenewLease,
			RenewLease: &RenewLeasePayload{
				TaskID:             tk.ID.String(),
				LeaseID:            tk.LeaseID.UUID.String(),
				LeaseTTLSeconds:    leaseTTLSec,
				RefreshedStreamURL: refreshedURL,
			},
		}); err != nil {
			r.log.Warn("publish renew lease command failed", "task_id", tk.ID, "err", err)
		}
	}
	return nil
}

func (r *Reconciler) sendRevoke(ctx context.Context, task ExistingTaskInfo, reason string) {
	ingestID := task.IngestInstanceID
	if ingestID == "" && task.AssignedNodeID != "" {
		if route, err := r.redis.GetAgentRoute(ctx, task.AssignedNodeID); err == nil {
			ingestID = route
		}
	}
	if ingestID == "" {
		return
	}
	if err := r.publishCommand(ctx, ingestID, AgentCommandEnvelope{
		NodeID:      task.AssignedNodeID,
		CommandType: CmdRevokeTask,
		RevokeTask: &RevokeTaskPayload{
			TaskID: task.ID.String(),
			Reason: reason,
		},
	}); err != nil {
		r.log.Warn("publish revoke task command failed", "task_id", task.ID, "err", err)
	}
}

func (r *Reconciler) getSpecHash(taskID uuid.UUID) string {
	r.specMu.Lock()
	defer r.specMu.Unlock()
	return r.lastSpecHash[taskID]
}

func (r *Reconciler) setSpecHash(taskID uuid.UUID, sig string) {
	r.specMu.Lock()
	defer r.specMu.Unlock()
	r.lastSpecHash[taskID] = sig
}

func (r *Reconciler) deleteSpecHash(taskID uuid.UUID) {
	r.specMu.Lock()
	defer r.specMu.Unlock()
	delete(r.lastSpecHash, taskID)
}

func taskSpecSignature(dt DesiredTask) string {
	raw, _ := json.Marshal(struct {
		ROIs           []CommandROI `json:"rois"`
		AlgoParamsJSON string       `json:"algo_params_json"`
	}{
		ROIs:           buildCommandROIs(dt.ROIs),
		AlgoParamsJSON: dt.AlgoParamsJSON,
	})
	return string(raw)
}
