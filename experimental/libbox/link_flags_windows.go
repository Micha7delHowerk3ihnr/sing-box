//go:build windows

package libbox

import "net"

func linkFlags(rawFlags uint32) net.Flags {
	return net.Flags(rawFlags)
}
