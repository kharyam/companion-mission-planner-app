package adb

import (
	"fmt"
	"net"
	"os/exec"
	"strconv"
	"strings"
	"time"

	goadb "github.com/zach-klippenstein/goadb"
)

// Transport wraps goadb's Adb client and the per-device descriptor.
//
// goadb talks to a locally-running adb-server over TCP (default :5037).
// We don't ship adb-server with this binary; users supply it via the
// platform-tools package. config.adb.serverHost/Port control which
// adb-server we connect to.
type Transport struct {
	adb *goadb.Adb
}

// Dial connects to the adb-server at host:port. If no server is reachable
// and we're targeting localhost, we spawn one ourselves.
//
// goadb's own StartServer invokes `adb -L tcp:<host>:<port> start-server`,
// which modern adb (>=34) rejects with "cannot start server on remote
// host" whenever an explicit host is present. We pre-spawn with the
// host-less `-L tcp:<port>` form so goadb only ever has to dial.
func Dial(host string, port int) (*Transport, error) {
	if isLocal(host) {
		if err := ensureLocalServer(port); err != nil {
			return nil, fmt.Errorf("start adb-server on :%d: %w", port, err)
		}
	}
	client, err := goadb.NewWithConfig(goadb.ServerConfig{Host: host, Port: port})
	if err != nil {
		return nil, fmt.Errorf("connect adb-server %s:%d: %w", host, port, err)
	}
	if _, err := client.ServerVersion(); err != nil {
		return nil, fmt.Errorf("probe adb-server %s:%d: %w", host, port, err)
	}
	return &Transport{adb: client}, nil
}

func isLocal(host string) bool {
	switch strings.ToLower(host) {
	case "", "127.0.0.1", "::1", "localhost":
		return true
	}
	return false
}

// ensureLocalServer makes sure something is listening on 127.0.0.1:port,
// spawning `adb` if not. Idempotent.
func ensureLocalServer(port int) error {
	if dialable(port) {
		return nil
	}
	adbBin, err := exec.LookPath("adb")
	if err != nil {
		return fmt.Errorf("adb not found in PATH (install android-platform-tools): %w", err)
	}
	// `-L tcp:<port>` only — modern adb refuses an explicit host here.
	cmd := exec.Command(adbBin, "-L", "tcp:"+strconv.Itoa(port), "start-server")
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("%s: %s", err, strings.TrimSpace(string(out)))
	}
	// adb forks the server and returns immediately; give it a beat to bind.
	for i := 0; i < 20; i++ {
		if dialable(port) {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("adb-server did not bind :%d within 2s", port)
}

func dialable(port int) bool {
	c, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), 200*time.Millisecond)
	if err != nil {
		return false
	}
	_ = c.Close()
	return true
}

// Underlying exposes the raw goadb client for callers that need
// functionality not yet wrapped here. Keep usage minimal so we can
// swap the implementation later without breaking the rest of the tree.
func (t *Transport) Underlying() *goadb.Adb { return t.adb }
