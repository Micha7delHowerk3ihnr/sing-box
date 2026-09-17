package daemon

import (
	"fmt"
	"os"

	"github.com/gofrs/uuid/v5"
	"github.com/sagernet/sing-box/protocol/group"
)

type RuntimeControlErrorCode string

const (
	RuntimeControlNotFound    RuntimeControlErrorCode = "not_found"
	RuntimeControlInvalid     RuntimeControlErrorCode = "invalid_argument"
	RuntimeControlUnsupported RuntimeControlErrorCode = "unsupported"
)

type RuntimeControlError struct {
	Code    RuntimeControlErrorCode
	Message string
}

func (e *RuntimeControlError) Error() string {
	return e.Message
}

func newRuntimeControlError(code RuntimeControlErrorCode, format string, args ...any) error {
	return &RuntimeControlError{Code: code, Message: fmt.Sprintf(format, args...)}
}

// RuntimeClashModeStatus is the transport-independent Clash mode state.
type RuntimeClashModeStatus struct {
	Modes   []string
	Current string
}

func (s *StartedService) ReadClashMode() (RuntimeClashModeStatus, error) {
	s.serviceAccess.RLock()
	if s.serviceStatus.Status != ServiceStatus_STARTED {
		s.serviceAccess.RUnlock()
		return RuntimeClashModeStatus{}, os.ErrInvalid
	}
	clashMode := s.instance.clashMode
	s.serviceAccess.RUnlock()
	if clashMode == nil {
		return RuntimeClashModeStatus{}, newRuntimeControlError(RuntimeControlNotFound, "clash mode not available")
	}
	return RuntimeClashModeStatus{Modes: clashMode.ModeList(), Current: clashMode.Mode()}, nil
}

func (s *StartedService) SetRuntimeClashMode(mode string) error {
	s.serviceAccess.RLock()
	if s.serviceStatus.Status != ServiceStatus_STARTED {
		s.serviceAccess.RUnlock()
		return os.ErrInvalid
	}
	clashMode := s.instance.clashMode
	s.serviceAccess.RUnlock()
	if clashMode == nil {
		return newRuntimeControlError(RuntimeControlNotFound, "clash mode not available")
	}
	clashMode.SetMode(mode)
	return nil
}

func (s *StartedService) SelectRuntimeOutbound(groupTag string, outboundTag string) error {
	s.serviceAccess.RLock()
	boxService := s.instance
	s.serviceAccess.RUnlock()
	if boxService == nil {
		return os.ErrInvalid
	}
	outboundGroup, loaded := boxService.outboundManager.Outbound(groupTag)
	if !loaded {
		return newRuntimeControlError(RuntimeControlNotFound, "selector not found: %s", groupTag)
	}
	selector, isSelector := outboundGroup.(*group.Selector)
	if !isSelector {
		return newRuntimeControlError(RuntimeControlInvalid, "outbound is not a selector: %s", groupTag)
	}
	if !selector.SelectOutbound(outboundTag) {
		return newRuntimeControlError(RuntimeControlNotFound, "outbound not found in selector: %s", outboundTag)
	}
	return nil
}

func (s *StartedService) CloseRuntimeConnection(connectionID string) error {
	s.serviceAccess.RLock()
	boxService := s.instance
	s.serviceAccess.RUnlock()
	if boxService == nil {
		return os.ErrInvalid
	}
	if boxService.trafficManager == nil {
		return newRuntimeControlError(RuntimeControlUnsupported, "connection tracking not available")
	}
	targetConnection := boxService.trafficManager.Connection(uuid.FromStringOrNil(connectionID))
	if targetConnection == nil {
		return newRuntimeControlError(RuntimeControlNotFound, "connection not found: %s", connectionID)
	}
	targetConnection.Close()
	return nil
}

func (s *StartedService) CloseAllRuntimeConnections() {
	s.serviceAccess.RLock()
	boxService := s.instance
	s.serviceAccess.RUnlock()
	if boxService == nil {
		return
	}
	if boxService.connectionManager != nil {
		boxService.connectionManager.CloseAll()
	}
	if boxService.trafficManager != nil {
		boxService.trafficManager.CloseAllConnections()
	}
}
