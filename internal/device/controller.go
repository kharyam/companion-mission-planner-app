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
	// ConnUSB is a USB Mass Storage volume — a camera/drone SD card in a
	// reader, or a drone exposing its storage as a block device.
	ConnUSB ConnectionType = "usb"
)

// Device kinds. A connected device is classified by inspecting its
// storage layout: a DJI Fly waypoint tree marks a controller; a DCIM
// folder marks a camera/drone. "unknown" covers anything else (and is
// the value reported until the background classification walk lands).
const (
	KindController = "controller"
	KindCamera     = "camera"
	KindUnknown    = "unknown"
)

// Info is the device summary returned to KAM.
type Info struct {
	ID             string         `json:"id"`
	Model          string         `json:"model"`
	ConnectionType ConnectionType `json:"connectionType"`
	Authorized     bool           `json:"authorized"`
	State          string         `json:"state"` // "online" | "offline" | "unauthorized" | "unknown"
	DJIFlyDetected bool           `json:"djiFlyDetected"`
	// Kind is "controller", "camera", or "unknown". The UI keys off it:
	// a controller shows the slots/transfer flow, a camera shows the
	// media gallery.
	Kind string `json:"kind"`
	Hint string `json:"hint,omitempty"` // human-readable next step for KAM to display
}

// Slot is a single waypoint slot on a device.
type Slot struct {
	GUID             string    `json:"guid"`
	Name             string    `json:"name"`
	LastModified     time.Time `json:"lastModified"`
	FileSize         int64     `json:"fileSize"`
	PreviewAvailable bool      `json:"previewAvailable"`
	PreviewURL       string    `json:"previewUrl,omitempty"`
	// Managed reflects the user's per-slot opt-in. Default true. When
	// false, the UI greys the card and disables write actions; batch
	// operations (regen all, push all wp images) skip it. Download
	// stays enabled regardless.
	Managed bool `json:"managed"`
}

// MediaItem is one photo or video discovered on a camera/drone.
type MediaItem struct {
	ID         string    `json:"id"`   // decimal MTP object ID
	Name       string    `json:"name"` // on-device filename
	Kind       string    `json:"kind"` // "photo" | "video"
	Size       int64     `json:"size"`
	ModifiedAt time.Time `json:"modifiedAt"`
	// HasPreview is true for videos that ship a sibling .LRF proxy —
	// the low-res clip DJI Fly plays for smooth scrubbing.
	HasPreview bool `json:"hasPreview"`
	// URLs are filled in by the API layer before the item is returned.
	ThumbnailURL string `json:"thumbnailUrl,omitempty"`
	PreviewURL   string `json:"previewUrl,omitempty"`
	DownloadURL  string `json:"downloadUrl,omitempty"`
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
