package adb

import (
	"fmt"

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

// Dial connects to the adb-server at host:port. If a server isn't running,
// goadb will attempt to spawn one via the system `adb` binary if it can
// find it on PATH.
func Dial(host string, port int) (*Transport, error) {
	client, err := goadb.NewWithConfig(goadb.ServerConfig{Host: host, Port: port})
	if err != nil {
		return nil, fmt.Errorf("connect adb-server %s:%d: %w", host, port, err)
	}
	if err := client.StartServer(); err != nil {
		return nil, fmt.Errorf("start adb-server: %w", err)
	}
	return &Transport{adb: client}, nil
}

// Underlying exposes the raw goadb client for callers that need
// functionality not yet wrapped here. Keep usage minimal so we can
// swap the implementation later without breaking the rest of the tree.
func (t *Transport) Underlying() *goadb.Adb { return t.adb }
