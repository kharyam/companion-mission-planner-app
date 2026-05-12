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
	"image/draw"
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
		if err := drawMap(ctx, dc, meta, opts); err != nil {
			// fall back to solid backdrop on tile error
			drawSolid(dc)
		}
	} else {
		drawSolid(dc)
	}

	overlayWaypoints(dc, meta)
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

// drawMap composites ESRI tiles covering the waypoint bbox into dc.
func drawMap(ctx context.Context, dc *gg.Context, meta *Metadata, opts Options) error {
	minLat, maxLat, minLng, maxLng := bbox(meta.Waypoints)
	zoom := pickZoom(minLat, maxLat, minLng, maxLng, opts.Width, opts.Height)

	xMin, yMax := lonLatToTile(minLng, minLat, zoom) // SW corner: min lng, min lat
	xMax, yMin := lonLatToTile(maxLng, maxLat, zoom) // NE corner: max lng, max lat (lower y)

	rows := yMax - yMin + 1
	cols := xMax - xMin + 1
	full := image.NewRGBA(image.Rect(0, 0, cols*256, rows*256))

	for ty := yMin; ty <= yMax; ty++ {
		for tx := xMin; tx <= xMax; tx++ {
			img, err := fetchTile(ctx, opts.HTTP, zoom, tx, ty)
			if err != nil {
				return err
			}
			dx := (tx - xMin) * 256
			dy := (ty - yMin) * 256
			draw.Draw(full, image.Rect(dx, dy, dx+256, dy+256), img, image.Point{}, draw.Src)
		}
	}

	// Crop/scale the composite into the gg context.
	dc.DrawImageAnchored(scaleToFit(full, opts.Width, opts.Height), opts.Width/2, opts.Height/2, 0.5, 0.5)
	return nil
}

func overlayWaypoints(dc *gg.Context, meta *Metadata) {
	if len(meta.Waypoints) == 0 {
		return
	}
	// Project waypoints into image space using the bbox we'd used to fetch tiles.
	minLat, maxLat, minLng, maxLng := bbox(meta.Waypoints)
	if minLat == maxLat || minLng == maxLng {
		return
	}
	w, h := dc.Width(), dc.Height()
	pad := float64(DefaultPadding)
	for i, p := range meta.Waypoints {
		x := pad + (p.Lng-minLng)/(maxLng-minLng)*(float64(w)-2*pad)
		y := pad + (1-(p.Lat-minLat)/(maxLat-minLat))*(float64(h)-2*pad)
		dc.SetRGB(1, 0.42, 0.21) // KAM orange placeholder
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

// pickZoom returns a slippy-map zoom level whose tile coverage roughly
// matches the requested image size for the given bbox.
func pickZoom(minLat, maxLat, minLng, maxLng float64, w, h int) int {
	// Width-in-degrees and height-in-degrees at zoom z determine tile count.
	const maxZoom = 18
	for z := maxZoom; z >= 1; z-- {
		xMin, yMax := lonLatToTile(minLng, minLat, z)
		xMax, yMin := lonLatToTile(maxLng, maxLat, z)
		if (xMax-xMin+1)*256 <= w*2 && (yMax-yMin+1)*256 <= h*2 {
			return z
		}
	}
	return 1
}

// lonLatToTile is the standard slippy-map projection.
func lonLatToTile(lng, lat float64, zoom int) (x, y int) {
	n := math.Pow(2, float64(zoom))
	x = int(math.Floor((lng + 180.0) / 360.0 * n))
	latRad := lat * math.Pi / 180.0
	y = int(math.Floor((1.0 - math.Log(math.Tan(latRad)+1.0/math.Cos(latRad))/math.Pi) / 2.0 * n))
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

// scaleToFit returns src letterboxed into a w x h RGBA image.
// We center-crop rather than letterbox to keep the preview filling the frame.
func scaleToFit(src *image.RGBA, w, h int) image.Image {
	sw := src.Bounds().Dx()
	sh := src.Bounds().Dy()
	if sw == w && sh == h {
		return src
	}
	// Simple center-crop. For a proper scale we'd want nearest/bilinear;
	// gg's DrawImageAnchored handles centering for us, so this is the
	// minimum that produces a sane output. Replace with golang.org/x/image
	// resize when we need quality.
	return src.SubImage(image.Rect(
		max((sw-w)/2, 0),
		max((sh-h)/2, 0),
		min((sw+w)/2, sw),
		min((sh+h)/2, sh),
	))
}
