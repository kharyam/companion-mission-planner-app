package device

import (
	"io"
	"time"
)

// ConnectionType describes how we talk to the device.
type ConnectionType string

const (
	ConnADB ConnectionType = "adb"
	ConnMTP ConnectionType = "mtp"
)

// Info is the device summary returned to KAM.
type Info struct {
	ID             string         `json:"id"`
	Model          string         `json:"model"`
	ConnectionType ConnectionType `json:"connectionType"`
	Authorized     bool           `json:"authorized"`
	State          string         `json:"state"` // "online" | "offline" | "unauthorized" | "unknown"
	DJIFlyDetected bool           `json:"djiFlyDetected"`
	Hint           string         `json:"hint,omitempty"` // human-readable next step for KAM to display
}

// Slot is a single waypoint slot on a device.
type Slot struct {
	GUID             string    `json:"guid"`
	Name             string    `json:"name"`
	LastModified     time.Time `json:"lastModified"`
	FileSize         int64     `json:"fileSize"`
	PreviewAvailable bool      `json:"previewAvailable"`
	PreviewURL       string    `json:"previewUrl,omitempty"`
}

// TransferResult is returned after a successful KMZ push.
type TransferResult struct {
	Success       bool      `json:"success"`
	GUID          string    `json:"guid"`
	FileSize      int64     `json:"fileSize"`
	TransferredAt time.Time `json:"transferredAt"`
}

// PreviewMetadata is the optional payload KAM sends for map rendering.
type PreviewMetadata struct {
	Name      string      `json:"name"`
	Waypoints []Waypoint  `json:"waypoints"`
	Center    *LatLng     `json:"center,omitempty"`
	Date      time.Time   `json:"date,omitempty"`
	Extra     interface{} `json:"-"`
}

type Waypoint struct {
	Lat       float64 `json:"lat"`
	Lng       float64 `json:"lng"`
	HasAction bool    `json:"hasAction,omitempty"`
}

type LatLng struct {
	Lat float64 `json:"lat"`
	Lng float64 `json:"lng"`
}

// Controller is the per-device interface. The Registry produces one of
// these per connected device and routes API calls through it.
type Controller interface {
	Info() Info
	ListSlots() ([]Slot, error)
	ReadPreview(guid string) (io.ReadCloser, error)
	WriteKMZ(guid string, kmz io.Reader, meta *PreviewMetadata) (*TransferResult, error)
	ClearSlot(guid string) error
}
