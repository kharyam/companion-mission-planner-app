package kmz

import (
	"archive/zip"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
)

// Mission is the metadata we extract from a DJI WPML KMZ to seed the
// previewMetadata payload of /api/devices/*/slots/*/transfer.
type Mission struct {
	Name      string     `json:"name,omitempty"`
	Author    string     `json:"author,omitempty"`
	Date      *time.Time `json:"date,omitempty"`
	Waypoints []Point    `json:"waypoints"`
	Source    string     `json:"source"` // which entry inside the KMZ we parsed
}

// Point is a single waypoint. Alt is meters above the mission's height
// datum (relativeToStartPoint in most DJI Fly templates).
type Point struct {
	Lat float64 `json:"lat"`
	Lng float64 `json:"lng"`
	Alt float64 `json:"alt,omitempty"`
	// Actions is the wpml:actionActuatorFunc list for this waypoint
	// (e.g. ["gimbalRotate","hover","takePhoto","startRecord"]). The
	// gimbalRotate/rotateYaw entries are routine setup; the others
	// represent intentional drone behavior the user usually cares to
	// see flagged on the map preview.
	Actions []string `json:"actions,omitempty"`
}

// HasMeaningfulAction reports whether the waypoint includes any action
// worth highlighting on a preview — currently takePhoto, startRecord,
// stopRecord, hover. Camera-setup actions like gimbalRotate are
// excluded because they accompany almost every waypoint.
func (p Point) HasMeaningfulAction() bool {
	for _, a := range p.Actions {
		switch a {
		case "takePhoto", "startRecord", "stopRecord", "hover":
			return true
		}
	}
	return false
}

// ExtractMission parses the first usable KML/WPML entry inside the
// archive and returns its waypoints. It prefers wpmz/template.kml
// (which DJI Fly itself writes), falling back to wpmz/waylines.wpml.
func ExtractMission(r io.ReaderAt, size int64) (*Mission, error) {
	if size > MaxSize {
		return nil, fmt.Errorf("kmz too large: %d > %d", size, MaxSize)
	}
	zr, err := zip.NewReader(r, size)
	if err != nil {
		return nil, fmt.Errorf("open kmz: %w", err)
	}
	// Search in priority order. Each candidate is tried; if it parses
	// but yields zero waypoints (DJI Fly's editor strips template.kml
	// down to a placeholder and moves the flight data into
	// waylines.wpml on save), we fall through to the next.
	candidates := []string{"wpmz/template.kml", "wpmz/waylines.wpml"}
	byName := make(map[string]*zip.File, len(zr.File))
	for _, f := range zr.File {
		byName[f.Name] = f
	}
	for _, name := range candidates {
		f, ok := byName[name]
		if !ok {
			continue
		}
		m, err := parseKMLFromZip(f)
		if err != nil {
			// "no waypoints" is the soft fall-through case; any other
			// parse error bubbles up.
			if !errors.Is(err, errNoWaypoints) {
				return nil, fmt.Errorf("parse %s: %w", name, err)
			}
			continue
		}
		m.Source = name
		return m, nil
	}
	// Catch-all: any other .kml/.wpml entry the spec might add later.
	for _, f := range zr.File {
		lower := strings.ToLower(f.Name)
		if !strings.HasSuffix(lower, ".kml") && !strings.HasSuffix(lower, ".wpml") {
			continue
		}
		// Skip ones we already tried.
		alreadyTried := false
		for _, c := range candidates {
			if c == f.Name {
				alreadyTried = true
				break
			}
		}
		if alreadyTried {
			continue
		}
		m, err := parseKMLFromZip(f)
		if err != nil {
			if errors.Is(err, errNoWaypoints) {
				continue
			}
			return nil, fmt.Errorf("parse %s: %w", f.Name, err)
		}
		m.Source = f.Name
		return m, nil
	}
	return nil, errors.New("no waypoints found in any kml/wpml entry of kmz")
}

// errNoWaypoints is the sentinel parseKML returns when an entry parses
// cleanly but contains zero waypoints — typically the empty
// template.kml DJI Fly leaves behind on save.
var errNoWaypoints = errors.New("no waypoints found in kml")

func parseKMLFromZip(f *zip.File) (*Mission, error) {
	rc, err := f.Open()
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	return parseKML(rc)
}

// parseKML walks the XML token stream rather than binding to a struct
// because DJI's WPML namespace mixes with the kml default namespace,
// and encoding/xml struct tags get awkward with that. The token-driven
// approach also makes it easy to skip the giant <description> CDATA
// blocks DJI Fly embeds for human-readable waypoint info.
func parseKML(r io.Reader) (*Mission, error) {
	dec := xml.NewDecoder(r)
	m := &Mission{}

	// Stack of element local names so we know whether we're inside a
	// Placemark when we encounter a <coordinates> tag (we only want
	// waypoint coordinates, not any incidental ones elsewhere).
	var stack []string
	inPlacemark := func() bool {
		for _, e := range stack {
			if e == "Placemark" {
				return true
			}
		}
		return false
	}

	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			stack = append(stack, t.Name.Local)
			switch t.Name.Local {
			case "coordinates":
				if !inPlacemark() {
					continue
				}
				var raw string
				if err := dec.DecodeElement(&raw, &t); err != nil {
					return nil, err
				}
				stack = stack[:len(stack)-1] // DecodeElement consumed the end tag
				if p, ok := parseCoordinate(raw); ok {
					m.Waypoints = append(m.Waypoints, p)
				}
			case "actionActuatorFunc":
				// wpml:action/wpml:actionActuatorFunc inside a Placemark
				// tells us what each waypoint actually *does*. We append
				// the value onto the most-recently-emitted waypoint.
				if !inPlacemark() || len(m.Waypoints) == 0 {
					continue
				}
				var raw string
				if err := dec.DecodeElement(&raw, &t); err != nil {
					return nil, err
				}
				stack = stack[:len(stack)-1]
				if v := strings.TrimSpace(raw); v != "" {
					last := &m.Waypoints[len(m.Waypoints)-1]
					last.Actions = append(last.Actions, v)
				}
			case "author":
				var raw string
				if err := dec.DecodeElement(&raw, &t); err != nil {
					return nil, err
				}
				stack = stack[:len(stack)-1]
				m.Author = strings.TrimSpace(raw)
			case "createTime":
				var raw string
				if err := dec.DecodeElement(&raw, &t); err != nil {
					return nil, err
				}
				stack = stack[:len(stack)-1]
				if ts, ok := parseUnixMillis(raw); ok {
					m.Date = &ts
				}
			case "name":
				// Only a top-level <Document><name> counts. WPML
				// doesn't usually set this on KAM-authored files,
				// but DJI Fly's own exports do, so we keep it.
				if len(stack) >= 2 && stack[len(stack)-2] == "Document" && m.Name == "" {
					var raw string
					if err := dec.DecodeElement(&raw, &t); err != nil {
						return nil, err
					}
					stack = stack[:len(stack)-1]
					m.Name = strings.TrimSpace(raw)
				}
			}
		case xml.EndElement:
			if len(stack) > 0 && stack[len(stack)-1] == t.Name.Local {
				stack = stack[:len(stack)-1]
			}
		}
	}
	if len(m.Waypoints) == 0 {
		return nil, errNoWaypoints
	}
	return m, nil
}

// parseCoordinate accepts the DJI form "lng,lat[,alt]" (single tuple per
// element — DJI never bundles multiple into one <coordinates> like raw
// KML LineStrings do). Returns ok=false on malformed input rather than
// erroring so one bad waypoint doesn't kill the whole parse.
func parseCoordinate(raw string) (Point, bool) {
	parts := strings.Split(strings.TrimSpace(raw), ",")
	if len(parts) < 2 {
		return Point{}, false
	}
	lng, err := strconv.ParseFloat(strings.TrimSpace(parts[0]), 64)
	if err != nil {
		return Point{}, false
	}
	lat, err := strconv.ParseFloat(strings.TrimSpace(parts[1]), 64)
	if err != nil {
		return Point{}, false
	}
	p := Point{Lat: lat, Lng: lng}
	if len(parts) >= 3 {
		if alt, err := strconv.ParseFloat(strings.TrimSpace(parts[2]), 64); err == nil {
			p.Alt = alt
		}
	}
	return p, true
}

// parseUnixMillis reads DJI's millisecond Unix timestamp string.
func parseUnixMillis(raw string) (time.Time, bool) {
	raw = strings.TrimSpace(raw)
	ms, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return time.Time{}, false
	}
	return time.UnixMilli(ms).UTC(), true
}
