package device

import (
	"bytes"
	"image/jpeg"
)

// extractEXIFThumbnail pulls the embedded thumbnail JPEG out of the head
// of a camera JPEG.
//
// A camera JPEG carries its EXIF thumbnail as a complete, self-contained
// JPEG (SOI…EOI) inside the APP1 segment near the start of the file. So
// in a partial read of the file's head we can find it as the first
// SOI/EOI pair *after* the outer image's own opening SOI: the outer
// image's compressed scan is far too large to have produced an EOI
// within a short head read, so any complete SOI…EOI pair we find — and
// can decode — is the thumbnail.
//
// Returns nil when no embedded thumbnail is present (or head is not a
// JPEG / is truncated before the thumbnail completes).
func extractEXIFThumbnail(head []byte) []byte {
	// Must start with the outer image's SOI (FF D8).
	if len(head) < 4 || head[0] != 0xFF || head[1] != 0xD8 {
		return nil
	}
	// Scan for an inner SOI: a JPEG always opens FF D8 FF <marker>, so
	// requiring the third byte to be FF rejects most coincidental pairs.
	for i := 2; i+3 < len(head); i++ {
		if head[i] != 0xFF || head[i+1] != 0xD8 || head[i+2] != 0xFF {
			continue
		}
		for j := i + 3; j+1 < len(head); j++ {
			if head[j] != 0xFF || head[j+1] != 0xD9 {
				continue
			}
			candidate := head[i : j+2]
			// Confirm it is genuinely a decodable JPEG before trusting it.
			if _, err := jpeg.DecodeConfig(bytes.NewReader(candidate)); err == nil {
				return candidate
			}
			break // this SOI didn't yield a valid JPEG; look for the next
		}
	}
	return nil
}
