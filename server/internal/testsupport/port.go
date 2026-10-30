package testsupport

import (
	"context"
	"net"
	"sync"
)

// handedOutPorts remembers every port FreePort returned in this process, so two
// parallel tests never get the same one even if the kernel offers it twice
// between one test's probe and its postgres binding it.
//
//nolint:gochecknoglobals // process-wide by design: it dedupes across tests
var handedOutPorts = struct {
	sync.Mutex

	ports map[int]bool
}{ports: map[int]bool{}}

// freePortAttempts bounds how many kernel-assigned ports FreePort probes
// before giving up.
const freePortAttempts = 20

// FreePort returns a TCP port nothing listens on, for a test that boots its own
// embedded PostgreSQL and runs in parallel with others.
//
// The kernel picks it from the ephemeral range (32768+ on Linux, 49152+ on
// macOS), so it never collides with the hand-numbered 14xxx/15xxx ports other
// packages still use (see TestFixedTestPortsAreUniqueAcrossPackages).
func FreePort(testingT TB) uint32 {
	testingT.Helper()

	handedOutPorts.Lock()
	defer handedOutPorts.Unlock()

	var listenConfig net.ListenConfig

	for range freePortAttempts {
		listener, err := listenConfig.Listen(context.Background(), "tcp", "127.0.0.1:0")
		if err != nil {
			testingT.Fatalf("no free TCP port: %v", err)

			return 0
		}

		addr, _ := listener.Addr().(*net.TCPAddr)
		_ = listener.Close()

		if addr == nil || handedOutPorts.ports[addr.Port] {
			continue
		}

		handedOutPorts.ports[addr.Port] = true

		return uint32(addr.Port)
	}

	testingT.Fatalf("no unused TCP port after %d attempts", freePortAttempts)

	return 0
}
