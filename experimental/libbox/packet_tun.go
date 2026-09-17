package libbox

import (
	"errors"
	"io"
	"sync"

	"github.com/sagernet/sing-box/option"
	tun "github.com/sagernet/sing-tun"
)

const (
	packetFamilyIPv4 = int32(2)
	packetFamilyIPv6 = int32(30)
)

type packetFlowTun struct {
	platform        PacketTunnel
	platformOptions option.TunPlatformOptions
	name            string
	closeOnce       sync.Once
	closeErr        error
}

func (t *packetFlowTun) Read(buffer []byte) (int, error) {
	packet, err := t.platform.ReadPacket()
	if err != nil {
		return 0, err
	}
	if packet == nil || len(packet.data) == 0 {
		return 0, errors.New("platform packet tunnel returned an empty packet")
	}
	if len(packet.data) > len(buffer) {
		return 0, io.ErrShortBuffer
	}
	copy(buffer, packet.data)
	return len(packet.data), nil
}

func (t *packetFlowTun) Write(packet []byte) (int, error) {
	if len(packet) == 0 {
		return 0, nil
	}
	var family int32
	switch packet[0] >> 4 {
	case 4:
		family = packetFamilyIPv4
	case 6:
		family = packetFamilyIPv6
	default:
		return 0, errors.New("platform packet tunnel received a non-IP packet")
	}
	if err := t.platform.WritePacket(NewPacket(packet, family)); err != nil {
		return 0, err
	}
	return len(packet), nil
}

func (t *packetFlowTun) Name() (string, error) {
	return t.name, nil
}

func (t *packetFlowTun) Start() error {
	return t.platform.Start()
}

func (t *packetFlowTun) Close() error {
	t.closeOnce.Do(func() {
		t.closeErr = t.platform.Close()
	})
	return t.closeErr
}

func (t *packetFlowTun) UpdateRouteOptions(options tun.Options) error {
	routeRanges, err := options.BuildAutoRouteRanges(true)
	if err != nil {
		return err
	}
	return t.platform.UpdateRouteOptions(&tunOptions{Options: &options, routeRanges: routeRanges, TunPlatformOptions: t.platformOptions})
}
