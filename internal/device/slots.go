package device

import (
	"fmt"
	"path"
	"strconv"
	"strings"
	"time"
)

// WaypointDir is where DJI Fly stores waypoint slots on the controller.
const WaypointDir = "/sdcard/Android/data/dji.go.v5/files/waypoint"

// PreviewDir is the per-mission preview-image directory.
const PreviewDir = WaypointDir + "/map_preview"

// SlotPaths returns the canonical on-device paths for a given GUID.
type SlotPaths struct {
	Dir     string // .../waypoint/<GUID>
	KMZ     string // .../waypoint/<GUID>/<GUID>.kmz
	Preview string // .../waypoint/map_preview/<GUID>.jpg
}

func PathsFor(guid string) SlotPaths {
	return SlotPaths{
		Dir:     path.Join(WaypointDir, guid),
		KMZ:     path.Join(WaypointDir, guid, guid+".kmz"),
		Preview: path.Join(PreviewDir, guid+".jpg"),
	}
}

// ParseLsLine extracts metadata from a single `ls -l` line on Android.
// Android's toybox ls output looks like:
//
//	-rw-rw---- 1 u0_a123 sdcard_rw 4321 2026-05-22 10:30 550E8400-....kmz
//
// We just need size and mtime; anything else is best-effort.
func ParseLsLine(line string) (size int64, mtime time.Time, name string, err error) {
	fields := strings.Fields(line)
	if len(fields) < 8 {
		return 0, time.Time{}, "", fmt.Errorf("unexpected ls output: %q", line)
	}
	size, err = strconv.ParseInt(fields[4], 10, 64)
	if err != nil {
		return 0, time.Time{}, "", fmt.Errorf("parse size: %w", err)
	}
	// fields[5] = date, fields[6] = time
	t, terr := time.Parse("2006-01-02 15:04", fields[5]+" "+fields[6])
	if terr == nil {
		mtime = t
	}
	name = strings.Join(fields[7:], " ")
	return size, mtime, name, nil
}
