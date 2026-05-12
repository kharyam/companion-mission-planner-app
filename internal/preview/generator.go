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
	_ "embed"
	"fmt"
	"image"
	"image/jpeg"
	"image/png"
	"io"
	"math"
	"net/http"
	"sync"
	"time"

	"github.com/fogleman/gg"
	"github.com/golang/freetype/truetype"
	"golang.org/x/image/font"
	"golang.org/x/image/font/gofont/goregular"
)

//go:embed kam-logo.png
var kamLogoPNG []byte

var (
	logoOnce sync.Once
	logoImg  image.Image
)

// kamLogo returns the rasterized KAM logo as a Go image.Image. Decoded
// once on first call and cached for the life of the process.
func kamLogo() image.Image {
	logoOnce.Do(func() {
		img, err := png.Decode(bytes.NewReader(kamLogoPNG))
		if err == nil {
			logoImg = img
		}
	})
	return logoImg
}

var (
	fontOnce sync.Once
	fontTT   *truetype.Font
)

// loadFont returns the embedded goregular font, parsed once and shared.
func loadFont() *truetype.Font {
	fontOnce.Do(func() {
		f, err := truetype.Parse(goregular.TTF)
		if err == nil {
			fontTT = f
		}
	})
	return fontTT
}

// setFontSize switches the gg context's font face to goregular at the
// requested size. Safe to call many times in one render — each face is
// constructed fresh; goregular parsing is cached.
func setFontSize(dc *gg.Context, sizePx float64) {
	f := loadFont()
	if f == nil {
		return
	}
	face := truetype.NewFace(f, &truetype.Options{Size: sizePx, DPI: 72})
	dc.SetFontFace(face)
	_ = font.Face(face)
}

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

// Default render settings. Match DJI Fly's own slot-preview JPEGs:
// 500x300, aspect 5:3. We tested 1024x768 first and DJI Fly silently
// ignored it (either size mismatch rejection or aggressive caching).
// Sticking to Fly's native dimensions is the safe path.
const (
	DefaultWidth   = 500
	DefaultHeight  = 300
	DefaultPadding = 30
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
	// Connect the path first so markers draw on top.
	dc.SetRGBA(1, 0.10, 0.10, 0.85)
	dc.SetLineWidth(2.5)
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
		drawMarker(dc, x, y, i+1)
	}
}

// drawMarker renders one waypoint marker: an outer white halo for
// contrast against busy/dark map tiles, a thin red outline, a black
// filled center, the waypoint number large and centered, and a tiny
// KAM logo tucked into the upper-right corner.
func drawMarker(dc *gg.Context, cx, cy float64, num int) {
	const radius = 20.0
	// White halo (always visible against any background).
	dc.SetRGBA(1, 1, 1, 0.95)
	dc.DrawCircle(cx, cy, radius+3)
	dc.Fill()
	// Red ring for KAM branding.
	dc.SetRGBA(1, 0.10, 0.10, 1)
	dc.DrawCircle(cx, cy, radius+1.5)
	dc.Fill()
	// Black filled center.
	dc.SetRGB(0, 0, 0)
	dc.DrawCircle(cx, cy, radius)
	dc.Fill()

	// Logo in the upper-right of the circle.
	if img := kamLogo(); img != nil {
		const logoH = 12.0
		ratio := float64(img.Bounds().Dx()) / float64(img.Bounds().Dy())
		logoW := logoH * ratio
		offX := radius * 0.35
		offY := -radius * 0.55
		scaled := scaleImage(img, int(logoW), int(logoH))
		dc.DrawImageAnchored(scaled, int(cx+offX), int(cy+offY), 0.5, 0.5)
	}

	// Number, large and centered.
	setFontSize(dc, 22)
	dc.SetRGB(1, 1, 1)
	dc.DrawStringAnchored(fmt.Sprintf("%d", num), cx, cy+2, 0.5, 0.45)
}

// scaleImage is a tiny nearest-neighbor scaler that's good enough for
// the small badges we draw. The source PNG is rasterized once at high
// resolution; we just downscale here. Avoids pulling in the
// golang.org/x/image draw package for one-off resampling.
func scaleImage(src image.Image, w, h int) image.Image {
	if w <= 0 || h <= 0 {
		return src
	}
	sb := src.Bounds()
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	xRatio := float64(sb.Dx()) / float64(w)
	yRatio := float64(sb.Dy()) / float64(h)
	for y := 0; y < h; y++ {
		sy := sb.Min.Y + int(float64(y)*yRatio)
		for x := 0; x < w; x++ {
			sx := sb.Min.X + int(float64(x)*xRatio)
			dst.Set(x, y, src.At(sx, sy))
		}
	}
	return dst
}

// overlayWaypointsFallback draws into a non-mapped canvas: stretch
// the bbox to a padded rectangle so markers are visible without map
// tiles. Same marker style as overlayWaypoints.
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
	dc.SetRGBA(1, 0.10, 0.10, 0.85)
	dc.SetLineWidth(2.5)
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
		drawMarker(dc, x, y, i+1)
	}
}

func overlayText(dc *gg.Context, meta *Metadata) {
	if meta.Name == "" && meta.Date.IsZero() {
		return
	}
	// Strip + fonts sized for at-a-glance legibility on the controller.
	// 50px name, 22px date; strip auto-sized to fit. Capped at 65% of
	// canvas height so a custom small render doesn't get drowned.
	nameSize := 50.0
	dateSize := 22.0
	stripH := nameSize + dateSize + 28
	if h := float64(dc.Height()); stripH > h*0.65 {
		stripH = h * 0.65
		nameSize = stripH * 0.46
		dateSize = stripH * 0.22
	}
	dc.SetRGBA(0, 0, 0, 0.7)
	dc.DrawRectangle(0, float64(dc.Height())-stripH, float64(dc.Width()), stripH)
	dc.Fill()
	dc.SetRGB(1, 1, 1)
	const leftPad = 14.0
	maxTextWidth := float64(dc.Width()) - 2*leftPad
	if meta.Name != "" {
		// Shrink font until the name fits horizontally. We never want
		// to truncate the slot name — a smaller render is still
		// readable; "Verificatio" with a cut-off "n" isn't.
		size := nameSize
		setFontSize(dc, size)
		for w, _ := dc.MeasureString(meta.Name); w > maxTextWidth && size > 14; w, _ = dc.MeasureString(meta.Name) {
			size *= 0.92
			setFontSize(dc, size)
		}
		dc.DrawString(meta.Name, leftPad, float64(dc.Height())-stripH+size+6)
	}
	if !meta.Date.IsZero() {
		setFontSize(dc, dateSize)
		dc.SetRGBA(1, 1, 1, 0.85)
		dc.DrawString(meta.Date.Format("2006-01-02 15:04"), leftPad, float64(dc.Height())-12)
	}
}

func overlayAttribution(dc *gg.Context) {
	setFontSize(dc, 11)
	dc.SetRGBA(0, 0, 0, 0.65)
	dc.DrawString("© Esri", float64(dc.Width()-44), float64(dc.Height()-4))
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

