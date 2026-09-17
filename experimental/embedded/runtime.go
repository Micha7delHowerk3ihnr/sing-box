package embedded

import (
	"context"
	"errors"

	"github.com/sagernet/sing-box/daemon"
	"github.com/sagernet/sing-box/experimental/libbox"
)

type State = daemon.RuntimeState
type Snapshot = daemon.RuntimeSnapshot
type ReloadResult = daemon.ReloadResult
type OverrideOptions = daemon.OverrideOptions

const (
	StateIdle      = daemon.RuntimeStateIdle
	StateStarting  = daemon.RuntimeStateStarting
	StateRunning   = daemon.RuntimeStateRunning
	StateReloading = daemon.RuntimeStateReloading
	StateStopping  = daemon.RuntimeStateStopping
	StateFailed    = daemon.RuntimeStateFailed
	StateClosing   = daemon.RuntimeStateClosing
	StateClosed    = daemon.RuntimeStateClosed
)

type Options struct {
	Handler           libbox.CommandServerHandler
	PlatformInterface libbox.PlatformInterface
}

// Runtime is the listener-free embedding entry point. It shares the same
// daemon.Runtime used by the optional libbox gRPC adapter but never calls
// CommandServer.Start, so construction has no socket/listener side effects.
type Runtime struct {
	owner *libbox.CommandServer
	core  *daemon.Runtime
}

func New(options Options) (*Runtime, error) {
	if options.Handler == nil {
		return nil, errors.New("embedded runtime handler is required")
	}
	if options.PlatformInterface == nil {
		return nil, errors.New("embedded platform interface is required")
	}
	owner, err := libbox.NewCommandServer(options.Handler, options.PlatformInterface)
	if err != nil {
		return nil, err
	}
	return &Runtime{owner: owner, core: owner.Runtime()}, nil
}

func (r *Runtime) Snapshot() Snapshot {
	return r.core.Snapshot()
}

func (r *Runtime) Subscribe() (Snapshot, <-chan Snapshot, func()) {
	return r.core.Subscribe()
}

func (r *Runtime) Validate(ctx context.Context, config []byte) error {
	return r.core.Validate(ctx, config)
}

func (r *Runtime) Start(ctx context.Context, config []byte, options *OverrideOptions) (Snapshot, error) {
	return r.core.Start(ctx, config, options)
}

func (r *Runtime) Reload(ctx context.Context, config []byte, options *OverrideOptions) (ReloadResult, error) {
	return r.core.Reload(ctx, config, options)
}

func (r *Runtime) Restart(ctx context.Context, options *OverrideOptions) (ReloadResult, error) {
	return r.core.Restart(ctx, options)
}

func (r *Runtime) Stop(ctx context.Context) (Snapshot, error) {
	return r.core.Stop(ctx)
}

func (r *Runtime) CancelActive() bool {
	return r.core.CancelActive()
}

func (r *Runtime) Shutdown(ctx context.Context) (Snapshot, error) {
	snapshot, err := r.core.Shutdown(ctx)
	r.owner.Close()
	return snapshot, err
}

// StartedService is exposed only for compatibility adapters. New clients
// should consume Runtime methods and snapshots instead of gRPC-shaped APIs.
func (r *Runtime) StartedService() *daemon.StartedService {
	return r.core.Service()
}
