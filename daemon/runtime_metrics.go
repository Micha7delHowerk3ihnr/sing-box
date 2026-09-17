package daemon

import (
	"sync"
	"time"
)

const minimumRuntimeSampleInterval = 250 * time.Millisecond

// StatusSampler converts the runtime's authoritative cumulative traffic
// counters into bytes-per-second samples without involving a gRPC stream.
type StatusSampler struct {
	service *StartedService
	access  sync.Mutex
	last    runtimeTrafficSample
}

// ConnectionSampler produces full connection snapshots whose Uplink and
// Downlink fields are bytes per second and whose total fields remain the
// authoritative counters owned by sing-box.
type ConnectionSampler struct {
	service *StartedService
	access  sync.Mutex
	last    map[string]runtimeTrafficSample
}

type runtimeTrafficSample struct {
	at            time.Time
	uploadTotal   int64
	downloadTotal int64
	uploadRate    int64
	downloadRate  int64
}

func (s *StartedService) NewStatusSampler() *StatusSampler {
	return &StatusSampler{service: s}
}

func (s *StartedService) NewConnectionSampler() *ConnectionSampler {
	return &ConnectionSampler{service: s, last: make(map[string]runtimeTrafficSample)}
}

func (s *StatusSampler) Read() *Status {
	status := s.service.ReadStatusSnapshot()
	now := time.Now()
	s.access.Lock()
	s.last = sampleRuntimeTraffic(s.last, now, status.UplinkTotal, status.DownlinkTotal)
	status.Uplink = s.last.uploadRate
	status.Downlink = s.last.downloadRate
	s.access.Unlock()
	return status
}

func (s *ConnectionSampler) Read() ([]*Connection, error) {
	connections, err := s.service.ReadConnectionsSnapshot()
	if err != nil {
		return nil, err
	}
	now := time.Now()
	s.access.Lock()
	active := make(map[string]struct{}, len(connections))
	for _, connection := range connections {
		active[connection.Id] = struct{}{}
		sample := sampleRuntimeTraffic(s.last[connection.Id], now, connection.UplinkTotal, connection.DownlinkTotal)
		s.last[connection.Id] = sample
		connection.Uplink = sample.uploadRate
		connection.Downlink = sample.downloadRate
	}
	for connectionID := range s.last {
		if _, loaded := active[connectionID]; !loaded {
			delete(s.last, connectionID)
		}
	}
	s.access.Unlock()
	return connections, nil
}

func sampleRuntimeTraffic(previous runtimeTrafficSample, now time.Time, uploadTotal int64, downloadTotal int64) runtimeTrafficSample {
	if previous.at.IsZero() {
		return runtimeTrafficSample{at: now, uploadTotal: uploadTotal, downloadTotal: downloadTotal}
	}
	elapsed := now.Sub(previous.at)
	if elapsed < minimumRuntimeSampleInterval {
		return previous
	}
	uploadDelta := uploadTotal - previous.uploadTotal
	downloadDelta := downloadTotal - previous.downloadTotal
	uploadRate := int64(0)
	downloadRate := int64(0)
	if uploadDelta >= 0 && downloadDelta >= 0 {
		seconds := elapsed.Seconds()
		uploadRate = int64(float64(uploadDelta) / seconds)
		downloadRate = int64(float64(downloadDelta) / seconds)
	}
	return runtimeTrafficSample{
		at:            now,
		uploadTotal:   uploadTotal,
		downloadTotal: downloadTotal,
		uploadRate:    uploadRate,
		downloadRate:  downloadRate,
	}
}
