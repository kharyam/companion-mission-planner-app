// Package preview renders the JPEG previews DJI Fly displays alongside
// each waypoint mission. It uses ESRI World Imagery tiles (no API key
// required) as a base layer and overlays numbered waypoints.
//
// Tile usage policy: ESRI's World Imagery service allows direct tile
// access without a key, but asks for attribution and reasonable rate
// limiting. We render at most once per mission transfer, so we're
// comfortably under any per-user rate limit. Attribution string is
// burned into the bottom of the image.
package preview

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/jpeg"
	"io"
	"math"
	"net/http"
	"time"

	"github.com/fogleman/gg"
)

// Waypoint is the lat/lng-only view this package needs.
type Waypoint struct {
	Lat float64
	Lng float64
}

// Metadata is the optional payload that drives a render. The device
// package converts its richer PreviewMetadata into this struct before
// calling Generate — keeping the preview package self-contained.
type Metadata struct {
	Name      string
	Waypoints []Waypoint
	Date      time.Time
}

// Tile server template. {z}, {y}, {x} are substituted at fetch time.
const esriTileURL = "https://server.arcgisonline.com/ArcGIS/rest/services/World_Imagery/MapServer/tile/%d/%d/%d"

// Default render settings. Override via Options.
const (
	DefaultWidth   = 1024
	DefaultHeight  = 768
	DefaultPadding = 64
)

type Options struct {
	Width  int
	Height int
	HTTP   *http.Client
}

func (o Options) withDefaults() Options {
	if o.Width == 0 {
		o.Width = DefaultWidth
	}
	if o.Height == 0 {
		o.Height = DefaultHeight
	}
	if o.HTTP == nil {
		o.HTTP = &http.Client{Timeout: 10 * time.Second}
	}
	return o
}

// Generate renders a JPEG preview. If meta has waypoints, we fetch tiles
// covering their bounding box; otherwise we render a solid backdrop with
// just the mission name.
func Generate(ctx context.Context, meta *Metadata, opts Options) ([]byte, error) {
	opts = opts.withDefaults()
	dc := gg.NewContext(opts.Width, opts.Height)

	if len(meta.Waypoints) >= 2 {
		proj, err := drawMap(ctx, dc, meta, opts)
		if err != nil {
			// fall back to solid backdrop on tile error
			drawSolid(dc)
			overlayWaypointsFallback(dc, meta)
		} else {
			overlayWaypoints(dc, meta, proj)
		}
	} else {
		drawSolid(dc)
		overlayWaypointsFallback(dc, meta)
	}

	overlayText(dc, meta)
	overlayAttribution(dc)

	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, dc.Image(), &jpeg.Options{Quality: 85}); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func drawSolid(dc *gg.Context) {
	dc.SetRGB(0.12, 0.16, 0.20)
	dc.Clear()
}

// projection records the slippy-map state needed to convert lat/lng
// to canvas pixels. canvasLeft/canvasTop are the world-pixel coords of
// the top-left of the rendered canvas.
type projection struct {
	zoom               int
	canvasLeft, canvasTop float64
}

func (p projection) latLngToXY(lat, lng float64) (x, y float64) {
	wx, wy := worldPx(lng, lat, p.zoom)
	return wx - p.canvasLeft, wy - p.canvasTop
}

// drawMap composites ESRI tiles into dc so the canvas is centered on
// the waypoint bbox at a zoom that keeps the bbox comfortably inside
// the frame. Returns the projection so waypoint overlays can use the
// exact same coordinate system.
func drawMap(ctx context.Context, dc *gg.Context, meta *Metadata, opts Options) (projection, error) {
	W, H := float64(opts.Width), float64(opts.Height)
	minLat, maxLat, minLng, maxLng := bbox(meta.Waypoints)

	// Add ~15% padding on each side so waypoints don't sit on the edge.
	dLat := maxLat - minLat
	dLng := maxLng - minLng
	if dLat == 0 {
		dLat = 0.0005
	}
	if dLng == 0 {
		dLng = 0.0005
	}
	minLat -= dLat * 0.15
	maxLat += dLat * 0.15
	minLng -= dLng * 0.15
	maxLng += dLng * 0.15

	zoom := chooseZoom(minLat, maxLat, minLng, maxLng, W, H)

	centerLat := (minLat + maxLat) / 2
	centerLng := (minLng + maxLng) / 2
	cx, cy := worldPx(centerLng, centerLat, zoom)
	left := cx - W/2
	top := cy - H/2

	tileX0 := int(math.Floor(left / 256))
	tileY0 := int(math.Floor(top / 256))
	tileX1 := int(math.Floor((left + W - 1) / 256))
	tileY1 := int(math.Floor((top + H - 1) / 256))

	for ty := tileY0; ty <= tileY1; ty++ {
		for tx := tileX0; tx <= tileX1; tx++ {
			img, err := fetchTile(ctx, opts.HTTP, zoom, tx, ty)
			if err != nil {
				return projection{}, err
			}
			dx := float64(tx*256) - left
			dy := float64(ty*256) - top
			dc.DrawImage(img, int(dx), int(dy))
		}
	}
	return projection{zoom: zoom, canvasLeft: left, canvasTop: top}, nil
}

func overlayWaypoints(dc *gg.Context, meta *Metadata, proj projection) {
	if len(meta.Waypoints) == 0 {
		return
	}
	// Draw the path first so circles sit on top.
	dc.SetRGBA(1, 0.42, 0.21, 0.85)
	dc.SetLineWidth(3)
	for i, p := range meta.Waypoints {
		x, y := proj.latLngToXY(p.Lat, p.Lng)
		if i == 0 {
			dc.MoveTo(x, y)
		} else {
			dc.LineTo(x, y)
		}
	}
	dc.Stroke()

	for i, p := range meta.Waypoints {
		x, y := proj.latLngToXY(p.Lat, p.Lng)
		// halo for legibility on busy tiles
		dc.SetRGBA(0, 0, 0, 0.45)
		dc.DrawCircle(x, y, 15)
		dc.Fill()
		dc.SetRGB(1, 0.42, 0.21)
		dc.DrawCircle(x, y, 12)
		dc.Fill()
		dc.SetRGB(1, 1, 1)
		dc.DrawStringAnchored(fmt.Sprintf("%d", i+1), x, y, 0.5, 0.4)
	}
}

// overlayWaypointsFallback draws into a non-mapped canvas: just stretch
// the bbox to a padded rectangle.
func overlayWaypointsFallback(dc *gg.Context, meta *Metadata) {
	if len(meta.Waypoints) == 0 {
		return
	}
	minLat, maxLat, minLng, maxLng := bbox(meta.Waypoints)
	dLat := maxLat - minLat
	dLng := maxLng - minLng
	if dLat == 0 {
		dLat = 1
	}
	if dLng == 0 {
		dLng = 1
	}
	w, h := dc.Width(), dc.Height()
	pad := float64(DefaultPadding)
	dc.SetRGBA(1, 0.42, 0.21, 0.85)
	dc.SetLineWidth(3)
	for i, p := range meta.Waypoints {
		x := pad + (p.Lng-minLng)/dLng*(float64(w)-2*pad)
		y := pad + (1-(p.Lat-minLat)/dLat)*(float64(h)-2*pad)
		if i == 0 {
			dc.MoveTo(x, y)
		} else {
			dc.LineTo(x, y)
		}
	}
	dc.Stroke()
	for i, p := range meta.Waypoints {
		x := pad + (p.Lng-minLng)/dLng*(float64(w)-2*pad)
		y := pad + (1-(p.Lat-minLat)/dLat)*(float64(h)-2*pad)
		dc.SetRGB(1, 0.42, 0.21)
		dc.DrawCircle(x, y, 14)
		dc.Fill()
		dc.SetRGB(1, 1, 1)
		dc.DrawStringAnchored(fmt.Sprintf("%d", i+1), x, y, 0.5, 0.4)
	}
}

func overlayText(dc *gg.Context, meta *Metadata) {
	if meta.Name == "" && meta.Date.IsZero() {
		return
	}
	dc.SetRGBA(0, 0, 0, 0.55)
	dc.DrawRectangle(0, float64(dc.Height()-80), float64(dc.Width()), 80)
	dc.Fill()
	dc.SetRGB(1, 1, 1)
	if meta.Name != "" {
		dc.DrawString(meta.Name, 24, float64(dc.Height()-44))
	}
	if !meta.Date.IsZero() {
		dc.DrawString(meta.Date.Format("2006-01-02 15:04"), 24, float64(dc.Height()-20))
	}
}

func overlayAttribution(dc *gg.Context) {
	dc.SetRGBA(0, 0, 0, 0.6)
	dc.DrawString("Imagery © Esri  ·  KAM Mission Planner", float64(dc.Width()-360), float64(dc.Height()-6))
}

// --- helpers ----------------------------------------------------------------

func bbox(pts []Waypoint) (minLat, maxLat, minLng, maxLng float64) {
	minLat, minLng = 90, 180
	maxLat, maxLng = -90, -180
	for _, p := range pts {
		if p.Lat < minLat {
			minLat = p.Lat
		}
		if p.Lat > maxLat {
			maxLat = p.Lat
		}
		if p.Lng < minLng {
			minLng = p.Lng
		}
		if p.Lng > maxLng {
			maxLng = p.Lng
		}
	}
	return
}

// chooseZoom returns the highest slippy-map zoom at which the bbox
// (already padded) fits inside the canvas with a small safety margin.
// The maximum is capped at ESRI World Imagery's reliable zoom level.
func chooseZoom(minLat, maxLat, minLng, maxLng float64, W, H float64) int {
	const minZoom, maxZoom = 1, 19
	for z := maxZoom; z >= minZoom; z-- {
		x0, y0 := worldPx(minLng, maxLat, z) // NW corner has smaller y
		x1, y1 := worldPx(maxLng, minLat, z) // SE corner has larger y
		if (x1-x0) <= W && (y1-y0) <= H {
			return z
		}
	}
	return minZoom
}

// worldPx converts lat/lng to world-pixel coordinates at the given zoom.
// At zoom z the world is 256 * 2^z pixels on each side. This is the
// projection both tile fetching and waypoint overlay share, which is
// what keeps the dots glued to the map.
func worldPx(lng, lat float64, zoom int) (x, y float64) {
	n := math.Pow(2, float64(zoom))
	x = (lng + 180.0) / 360.0 * 256.0 * n
	latRad := lat * math.Pi / 180.0
	y = (1.0 - math.Log(math.Tan(latRad)+1.0/math.Cos(latRad))/math.Pi) / 2.0 * 256.0 * n
	return
}

func fetchTile(ctx context.Context, client *http.Client, z, x, y int) (image.Image, error) {
	url := fmt.Sprintf(esriTileURL, z, y, x)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "kam-transfer/0.1 (+https://github.com/kamdynamics)")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil, fmt.Errorf("tile %d/%d/%d: HTTP %d", z, x, y, resp.StatusCode)
	}
	img, _, err := image.Decode(resp.Body)
	return img, err
}

