package kmz

import (
	"archive/zip"
	"bytes"
	"errors"
	"fmt"
	"io"
	"path"
	"regexp"
	"strings"
)

// MaxSize caps the KMZ size we accept (10 MB per spec).
const MaxSize = 10 * 1024 * 1024

// guidRE matches the GUID-style identifiers DJI Fly uses for waypoint slots.
// Accepts both hyphenated (8-4-4-4-12) and uppercase-only variants.
var guidRE = regexp.MustCompile(`^[0-9A-Fa-f]{8}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{12}$`)

// IsValidGUID reports whether s is a well-formed slot GUID.
func IsValidGUID(s string) bool { return guidRE.MatchString(s) }

// Info is metadata extracted from a KMZ.
type Info struct {
	Name        string
	Description string
	Waypoints   int
	HasPreview  bool
	Files       []string
}

// Inspect parses a KMZ from r without modifying it. It enforces the size cap
// and rejects archives with suspicious paths (absolute, zip-slip).
func Inspect(r io.ReaderAt, size int64) (*Info, error) {
	if size > MaxSize {
		return nil, fmt.Errorf("kmz too large: %d > %d", size, MaxSize)
	}
	zr, err := zip.NewReader(r, size)
	if err != nil {
		return nil, fmt.Errorf("open kmz: %w", err)
	}
	info := &Info{}
	for _, f := range zr.File {
		if err := safePath(f.Name); err != nil {
			return nil, err
		}
		info.Files = append(info.Files, f.Name)
		if strings.HasSuffix(strings.ToLower(f.Name), "thumbnail.png") ||
			strings.HasSuffix(strings.ToLower(f.Name), "preview.jpg") {
			info.HasPreview = true
		}
		if strings.HasSuffix(strings.ToLower(f.Name), ".kml") ||
			strings.HasSuffix(strings.ToLower(f.Name), "template.kml") ||
			strings.HasSuffix(strings.ToLower(f.Name), "waylines.wpml") {
			// TODO: parse XML to populate Name/Description/Waypoints.
		}
	}
	return info, nil
}

// safePath rejects entries that would escape the archive root.
func safePath(name string) error {
	if name == "" {
		return errors.New("empty entry name")
	}
	if path.IsAbs(name) || strings.HasPrefix(name, "/") || strings.Contains(name, "..") {
		return fmt.Errorf("unsafe path in kmz: %q", name)
	}
	return nil
}

// RewriteForGUID copies src into a new KMZ where any references to the
// original mission GUID inside text entries are replaced with newGUID.
// DJI Fly expects the embedded mission ID to match the slot filename.
//
// TODO: this currently only does naive byte-replacement of GUID-looking
// strings inside KML/WPML entries. A proper implementation would parse
// the XML and rewrite the documented fields only.
func RewriteForGUID(src io.ReaderAt, size int64, newGUID string) ([]byte, error) {
	if !IsValidGUID(newGUID) {
		return nil, fmt.Errorf("invalid GUID: %q", newGUID)
	}
	zr, err := zip.NewReader(src, size)
	if err != nil {
		return nil, err
	}
	var out bytes.Buffer
	zw := zip.NewWriter(&out)
	for _, f := range zr.File {
		if err := safePath(f.Name); err != nil {
			_ = zw.Close()
			return nil, err
		}
		rc, err := f.Open()
		if err != nil {
			_ = zw.Close()
			return nil, err
		}
		data, err := io.ReadAll(rc)
		_ = rc.Close()
		if err != nil {
			_ = zw.Close()
			return nil, err
		}
		if isTextEntry(f.Name) {
			data = guidRE.ReplaceAllFunc(data, func(b []byte) []byte { return []byte(newGUID) })
		}
		w, err := zw.CreateHeader(&zip.FileHeader{Name: f.Name, Method: f.Method})
		if err != nil {
			_ = zw.Close()
			return nil, err
		}
		if _, err := w.Write(data); err != nil {
			_ = zw.Close()
			return nil, err
		}
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

func isTextEntry(name string) bool {
	n := strings.ToLower(name)
	return strings.HasSuffix(n, ".kml") || strings.HasSuffix(n, ".wpml") || strings.HasSuffix(n, ".xml")
}
