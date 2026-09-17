package daemon

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"sync"
	"time"
)

type RuntimeState string

const (
	RuntimeStateIdle      RuntimeState = "idle"
	RuntimeStateStarting  RuntimeState = "starting"
	RuntimeStateRunning   RuntimeState = "running"
	RuntimeStateReloading RuntimeState = "reloading"
	RuntimeStateStopping  RuntimeState = "stopping"
	RuntimeStateFailed    RuntimeState = "failed"
	RuntimeStateClosing   RuntimeState = "closing"
	RuntimeStateClosed    RuntimeState = "closed"
)

type RuntimeSnapshot struct {
	Epoch             string       `json:"runtime_epoch"`
	Revision          uint64       `json:"revision"`
	Generation        uint64       `json:"generation"`
	State             RuntimeState `json:"state"`
	ActiveConfigHash  string       `json:"active_config_hash,omitempty"`
	ResourcesReleased bool         `json:"resources_released"`
	LastError         string       `json:"last_error,omitempty"`
	StartedAt         time.Time    `json:"started_at,omitempty"`
}

type ReloadResult struct {
	Snapshot          RuntimeSnapshot `json:"snapshot"`
	RollbackSucceeded bool            `json:"rollback_succeeded"`
}

// Runtime owns one StartedService and serializes resource-changing operations.
// Cancellation is intentionally protected by a different, short-held lock so
// stop/cancel can reach an in-flight start or reload.
type Runtime struct {
	ctx     context.Context
	service *StartedService

	operationAccess      sync.Mutex
	stateAccess          sync.RWMutex
	epoch                string
	revision             uint64
	generation           uint64
	state                RuntimeState
	activeConfig         []byte
	activeHash           string
	lastSuccessfulConfig []byte
	lastSuccessfulHash   string
	resourcesReleased    bool
	lastError            string
	startedAt            time.Time
	activeCancel         context.CancelFunc
	stopRequested        bool
	closed               bool

	subscriberID uint64
	subscribers  map[uint64]chan RuntimeSnapshot
}

func NewRuntime(ctx context.Context, service *StartedService) *Runtime {
	if ctx == nil {
		ctx = context.Background()
	}
	if service == nil {
		panic("nil StartedService")
	}
	runtime := &Runtime{
		ctx:               ctx,
		service:           service,
		epoch:             newRuntimeEpoch(),
		state:             RuntimeStateIdle,
		resourcesReleased: true,
		subscribers:       make(map[uint64]chan RuntimeSnapshot),
	}
	service.serviceAccess.Lock()
	service.runtime = runtime
	service.serviceAccess.Unlock()
	return runtime
}

func newRuntimeEpoch() string {
	var value [16]byte
	if _, err := rand.Read(value[:]); err == nil {
		return hex.EncodeToString(value[:])
	}
	return hex.EncodeToString([]byte(time.Now().UTC().Format(time.RFC3339Nano)))
}

func configHash(config []byte) string {
	sum := sha256.Sum256(config)
	return hex.EncodeToString(sum[:])
}

func (r *Runtime) Service() *StartedService {
	return r.service
}

func (r *Runtime) Snapshot() RuntimeSnapshot {
	r.stateAccess.RLock()
	defer r.stateAccess.RUnlock()
	return r.snapshotLocked()
}

func (r *Runtime) snapshotLocked() RuntimeSnapshot {
	return RuntimeSnapshot{
		Epoch:             r.epoch,
		Revision:          r.revision,
		Generation:        r.generation,
		State:             r.state,
		ActiveConfigHash:  r.activeHash,
		ResourcesReleased: r.resourcesReleased,
		LastError:         r.lastError,
		StartedAt:         r.startedAt,
	}
}

func (r *Runtime) updateLocked(state RuntimeState, resourcesReleased bool, lastError string) RuntimeSnapshot {
	r.revision++
	r.state = state
	r.resourcesReleased = resourcesReleased
	r.lastError = lastError
	snapshot := r.snapshotLocked()
	for _, subscriber := range r.subscribers {
		select {
		case subscriber <- snapshot:
		default:
			select {
			case <-subscriber:
			default:
			}
			select {
			case subscriber <- snapshot:
			default:
			}
		}
	}
	return snapshot
}

// Subscribe atomically registers a bounded observer and returns the snapshot
// whose revision precedes all later values delivered on the channel.
func (r *Runtime) Subscribe() (RuntimeSnapshot, <-chan RuntimeSnapshot, func()) {
	r.stateAccess.Lock()
	r.subscriberID++
	id := r.subscriberID
	updates := make(chan RuntimeSnapshot, 1)
	r.subscribers[id] = updates
	snapshot := r.snapshotLocked()
	r.stateAccess.Unlock()
	var once sync.Once
	cancel := func() {
		once.Do(func() {
			r.stateAccess.Lock()
			if subscriber, loaded := r.subscribers[id]; loaded {
				delete(r.subscribers, id)
				close(subscriber)
			}
			r.stateAccess.Unlock()
		})
	}
	return snapshot, updates, cancel
}

func (r *Runtime) Validate(ctx context.Context, config []byte) error {
	if len(config) == 0 {
		return errors.New("empty configuration")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return r.service.CheckConfig(ctx, string(config))
}

func (r *Runtime) beginOperation(parent context.Context) (context.Context, error) {
	if parent == nil {
		parent = context.Background()
	}
	r.stateAccess.Lock()
	defer r.stateAccess.Unlock()
	if r.closed || r.state == RuntimeStateClosing || r.state == RuntimeStateClosed {
		return nil, os.ErrClosed
	}
	ctx, cancel := context.WithCancel(parent)
	r.activeCancel = cancel
	r.stopRequested = false
	return ctx, nil
}

func (r *Runtime) endOperation() {
	r.stateAccess.Lock()
	if r.activeCancel != nil {
		r.activeCancel()
		r.activeCancel = nil
	}
	r.stateAccess.Unlock()
}

func (r *Runtime) CancelActive() bool {
	r.stateAccess.Lock()
	defer r.stateAccess.Unlock()
	if r.activeCancel == nil {
		return false
	}
	r.activeCancel()
	return true
}

func (r *Runtime) Start(ctx context.Context, config []byte, options *OverrideOptions) (RuntimeSnapshot, error) {
	r.operationAccess.Lock()
	defer r.operationAccess.Unlock()
	r.stateAccess.RLock()
	state := r.state
	r.stateAccess.RUnlock()
	if state != RuntimeStateIdle && state != RuntimeStateFailed {
		return r.Snapshot(), errors.New("runtime start requires idle state")
	}
	opCtx, err := r.beginOperation(ctx)
	if err != nil {
		return r.Snapshot(), err
	}
	defer r.endOperation()
	r.stateAccess.Lock()
	r.updateLocked(RuntimeStateStarting, false, "")
	r.stateAccess.Unlock()
	if err = r.Validate(opCtx, config); err != nil {
		r.stateAccess.Lock()
		snapshot := r.updateLocked(RuntimeStateIdle, true, err.Error())
		r.stateAccess.Unlock()
		return snapshot, err
	}
	err = r.service.startOrReloadService(opCtx, string(config), options)
	if err == nil {
		err = opCtx.Err()
	}
	if err != nil {
		_ = r.service.closeService()
		r.stateAccess.Lock()
		snapshot := r.updateLocked(RuntimeStateFailed, true, err.Error())
		r.stateAccess.Unlock()
		return snapshot, err
	}
	r.stateAccess.Lock()
	r.generation++
	r.activeConfig = append(r.activeConfig[:0], config...)
	r.activeHash = configHash(config)
	r.lastSuccessfulConfig = append(r.lastSuccessfulConfig[:0], config...)
	r.lastSuccessfulHash = r.activeHash
	r.startedAt = time.Now()
	snapshot := r.updateLocked(RuntimeStateRunning, false, "")
	r.stateAccess.Unlock()
	return snapshot, nil
}

func (r *Runtime) Reload(ctx context.Context, config []byte, options *OverrideOptions) (ReloadResult, error) {
	r.operationAccess.Lock()
	defer r.operationAccess.Unlock()
	r.stateAccess.RLock()
	if r.state != RuntimeStateRunning {
		r.stateAccess.RUnlock()
		return ReloadResult{Snapshot: r.Snapshot()}, errors.New("runtime reload requires running state")
	}
	oldConfig := append([]byte(nil), r.activeConfig...)
	oldHash := r.activeHash
	r.stateAccess.RUnlock()
	opCtx, err := r.beginOperation(ctx)
	if err != nil {
		return ReloadResult{Snapshot: r.Snapshot()}, err
	}
	defer r.endOperation()
	r.stateAccess.Lock()
	r.updateLocked(RuntimeStateReloading, false, "")
	r.stateAccess.Unlock()
	if err = r.Validate(opCtx, config); err != nil {
		r.stateAccess.Lock()
		snapshot := r.updateLocked(RuntimeStateRunning, false, err.Error())
		r.stateAccess.Unlock()
		return ReloadResult{Snapshot: snapshot}, err
	}
	err = r.service.startOrReloadService(opCtx, string(config), options)
	if err == nil {
		err = opCtx.Err()
	}
	if err == nil {
		r.stateAccess.Lock()
		r.generation++
		r.activeConfig = append(r.activeConfig[:0], config...)
		r.activeHash = configHash(config)
		r.lastSuccessfulConfig = append(r.lastSuccessfulConfig[:0], config...)
		r.lastSuccessfulHash = r.activeHash
		r.startedAt = time.Now()
		snapshot := r.updateLocked(RuntimeStateRunning, false, "")
		r.stateAccess.Unlock()
		return ReloadResult{Snapshot: snapshot}, nil
	}

	r.stateAccess.RLock()
	stopRequested := r.stopRequested
	r.stateAccess.RUnlock()
	if !stopRequested && len(oldConfig) > 0 {
		rollbackErr := r.service.startOrReloadService(r.ctx, string(oldConfig), options)
		if rollbackErr == nil {
			r.stateAccess.Lock()
			r.generation++
			r.activeConfig = oldConfig
			r.activeHash = oldHash
			r.lastSuccessfulConfig = append(r.lastSuccessfulConfig[:0], oldConfig...)
			r.lastSuccessfulHash = oldHash
			r.startedAt = time.Now()
			snapshot := r.updateLocked(RuntimeStateRunning, false, err.Error())
			r.stateAccess.Unlock()
			return ReloadResult{Snapshot: snapshot, RollbackSucceeded: true}, err
		}
		err = errors.Join(err, rollbackErr)
	}
	_ = r.service.closeService()
	r.stateAccess.Lock()
	r.activeConfig = nil
	r.activeHash = ""
	r.startedAt = time.Time{}
	snapshot := r.updateLocked(RuntimeStateFailed, true, err.Error())
	r.stateAccess.Unlock()
	return ReloadResult{Snapshot: snapshot}, err
}

func (r *Runtime) Restart(ctx context.Context, options *OverrideOptions) (ReloadResult, error) {
	r.stateAccess.RLock()
	config := append([]byte(nil), r.lastSuccessfulConfig...)
	state := r.state
	r.stateAccess.RUnlock()
	if len(config) == 0 {
		return ReloadResult{Snapshot: r.Snapshot()}, errors.New("no successful configuration is available")
	}
	if state == RuntimeStateRunning {
		return r.Reload(ctx, config, options)
	}
	snapshot, err := r.Start(ctx, config, options)
	return ReloadResult{Snapshot: snapshot}, err
}

// Apply preserves the legacy start-or-reload behavior for adapters while all
// new callers use the explicit Start, Reload and Restart operations.
func (r *Runtime) Apply(ctx context.Context, config []byte, options *OverrideOptions) error {
	snapshot := r.Snapshot()
	if snapshot.State == RuntimeStateRunning {
		_, err := r.Reload(ctx, config, options)
		return err
	}
	_, err := r.Start(ctx, config, options)
	return err
}

func (r *Runtime) Stop(ctx context.Context) (RuntimeSnapshot, error) {
	r.stateAccess.Lock()
	r.stopRequested = true
	if r.activeCancel != nil {
		r.activeCancel()
	}
	r.stateAccess.Unlock()
	r.operationAccess.Lock()
	defer r.operationAccess.Unlock()
	r.stateAccess.Lock()
	if r.state == RuntimeStateClosed {
		snapshot := r.snapshotLocked()
		r.stateAccess.Unlock()
		return snapshot, nil
	}
	r.updateLocked(RuntimeStateStopping, false, "")
	r.stateAccess.Unlock()
	err := r.service.closeService()
	r.stateAccess.Lock()
	r.activeConfig = nil
	r.activeHash = ""
	r.startedAt = time.Time{}
	if err != nil {
		snapshot := r.updateLocked(RuntimeStateFailed, false, err.Error())
		r.stateAccess.Unlock()
		return snapshot, err
	}
	snapshot := r.updateLocked(RuntimeStateIdle, true, "")
	r.stateAccess.Unlock()
	return snapshot, nil
}

func (r *Runtime) Shutdown(ctx context.Context) (RuntimeSnapshot, error) {
	r.stateAccess.Lock()
	if r.closed {
		snapshot := r.snapshotLocked()
		r.stateAccess.Unlock()
		return snapshot, nil
	}
	r.updateLocked(RuntimeStateClosing, false, "")
	r.stateAccess.Unlock()
	snapshot, err := r.Stop(ctx)
	if err != nil {
		return snapshot, err
	}
	r.operationAccess.Lock()
	r.service.Close()
	r.stateAccess.Lock()
	r.closed = true
	snapshot = r.updateLocked(RuntimeStateClosed, true, "")
	for id, subscriber := range r.subscribers {
		delete(r.subscribers, id)
		close(subscriber)
	}
	r.stateAccess.Unlock()
	r.operationAccess.Unlock()
	return snapshot, nil
}
