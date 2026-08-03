// Package jpegmeta rewrites a JPEG's metadata without touching its image data.
//
// The activity-photo pipeline used to re-encode every upload through
// image/jpeg, which stripped EXIF (the privacy mechanism) as a side effect of
// throwing the original away. That cost ~300 ms per photo, produced a file
// ~28% LARGER than the source, discarded the ICC profile — image/jpeg emits no
// APP2 — and, being a re-encode of already-lossy data, could only ever be a
// worse image than the bytes it replaced.
//
// None of that is necessary. A JPEG is a sequence of marker segments followed
// by entropy-coded scan data; metadata lives in the APPn segments, and the
// scan data can be copied through byte-for-byte. Strip does exactly that.
//
// The rule is a WHITELIST, not a blocklist, because location does not only
// live in EXIF GPS tags — XMP (APP1, different namespace) and IPTC (APP13)
// both carry coordinates, and MakerNotes carry camera serial numbers. Only
// these survive:
//
//   - APP2 / ICC profile — color, not metadata. Keeping it fixes the
//     Display P3 shift the re-encode introduced.
//   - APP14 / Adobe — the color-transform flag. image/jpeg reads it to
//     decide how to interpret CMYK/YCCK, so dropping it would change how
//     some files decode. Carries no identifying information.
//   - EXIF Orientation — re-emitted as a freshly built, minimal APP1. The
//     source's own APP1 is never copied through, so nothing can ride along
//     inside it.
//
// Everything else goes: GPS, timestamps, Make/Model, MakerNote, XMP, IPTC,
// JFIF/JFXX (which can carry a thumbnail), and comments.
//
// See prog-strength-docs/sows/photo-upload-direct-to-s3.md. v1 is JPEG-only by
// deliberate scope decision; PNG and WebP still go through the re-encode.
package jpegmeta

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
)

// ErrMalformed means the input is not a JPEG this package is willing to
// rewrite. It is terminal: the caller should not retry, though the photo
// pipeline treats it as "fall back to the re-encode" rather than a failure.
var ErrMalformed = errors.New("jpegmeta: malformed JPEG")

// JPEG markers. A marker is 0xFF followed by one of these.
const (
	markerSOI   = 0xD8 // start of image
	markerEOI   = 0xD9 // end of image
	markerSOS   = 0xDA // start of scan — entropy-coded data follows
	markerAPP0  = 0xE0
	markerAPP1  = 0xE1 // EXIF or XMP
	markerAPP2  = 0xE2 // ICC profile
	markerAPP14 = 0xEE // Adobe color transform
	markerAPP15 = 0xEF
	markerCOM   = 0xFE // comment
	markerTEM   = 0x01
	markerRST0  = 0xD0
	markerRST7  = 0xD7
)

var (
	exifPrefix  = []byte("Exif\x00\x00")
	iccPrefix   = []byte("ICC_PROFILE\x00")
	adobePrefix = []byte("Adobe")
)

// tagOrientation is the EXIF IFD0 tag holding the display orientation (1-8).
const tagOrientation = 0x0112

// Strip returns src rewritten with only the whitelisted metadata. The
// entropy-coded scan data is copied through byte-for-byte, so the returned
// image is pixel-identical to the input — this is a metadata edit, not a
// re-encode.
//
// A source whose orientation is absent or 1 (the identity) gets no APP1 at
// all: emitting one would be pure noise.
func Strip(src []byte) ([]byte, error) {
	segs, tail, err := scan(src)
	if err != nil {
		return nil, err
	}

	// Read the orientation off the FIRST EXIF block only. A file with two
	// APP1s is malformed in practice; taking the first and discarding the
	// rest is what every decoder does, and it means the second cannot
	// smuggle anything through.
	orientation := 1
	for _, s := range segs {
		if s.marker == markerAPP1 && bytes.HasPrefix(s.payload, exifPrefix) {
			if o := orientationFromEXIF(s.payload); o != 0 {
				orientation = o
			}
			break
		}
	}

	out := make([]byte, 0, len(src))
	out = append(out, 0xFF, markerSOI)

	// EXIF goes first, per convention, and is built from scratch rather than
	// edited in place.
	if orientation > 1 && orientation <= 8 {
		out = append(out, buildEXIF(orientation)...)
	}

	for _, s := range segs {
		if !keep(s) {
			continue
		}
		out = append(out, 0xFF, s.marker)
		out = binary.BigEndian.AppendUint16(out, uint16(len(s.payload)+2))
		out = append(out, s.payload...)
	}

	// Everything from SOS onward, verbatim. This is the whole point.
	return append(out, tail...), nil
}

// segment is one parsed marker segment, excluding its 0xFF, marker byte and
// two length bytes.
type segment struct {
	marker  byte
	payload []byte
}

// scan walks src's segment structure up to SOS and returns the segments plus
// the untouched remainder (the SOS segment and all entropy-coded data after
// it).
func scan(src []byte) ([]segment, []byte, error) {
	if len(src) < 4 || src[0] != 0xFF || src[1] != markerSOI {
		return nil, nil, fmt.Errorf("%w: missing SOI", ErrMalformed)
	}

	var segs []segment
	i := 2
	for {
		if i+1 >= len(src) {
			return nil, nil, fmt.Errorf("%w: ran out of input before SOS", ErrMalformed)
		}
		if src[i] != 0xFF {
			return nil, nil, fmt.Errorf("%w: expected a marker at offset %d", ErrMalformed, i)
		}
		m := src[i+1]

		// Standalone markers carry no length payload.
		if m == markerTEM || (m >= markerRST0 && m <= markerRST7) || m == markerSOI {
			i += 2
			continue
		}
		if m == markerSOS {
			// A complete JPEG ends with EOI, and EOI cannot occur inside
			// entropy-coded data (0xFF there is always followed by 0x00 or a
			// restart marker). Its absence means the file is truncated, which
			// we must not pass off as a successful strip.
			if bytes.LastIndex(src[i:], []byte{0xFF, markerEOI}) < 0 {
				return nil, nil, fmt.Errorf("%w: truncated, no EOI after SOS", ErrMalformed)
			}
			return segs, src[i:], nil
		}
		if m == markerEOI {
			return nil, nil, fmt.Errorf("%w: EOI before SOS", ErrMalformed)
		}

		if i+3 >= len(src) {
			return nil, nil, fmt.Errorf("%w: truncated segment length at %d", ErrMalformed, i)
		}
		n := int(binary.BigEndian.Uint16(src[i+2 : i+4]))
		if n < 2 || i+2+n > len(src) {
			return nil, nil, fmt.Errorf("%w: bad segment length %d at offset %d", ErrMalformed, n, i)
		}
		segs = append(segs, segment{marker: m, payload: src[i+4 : i+2+n]})
		i += 2 + n
	}
}

// scanAll walks the ENTIRE file rather than stopping at the first scan,
// stepping over entropy-coded data to find the segments between and after
// scans. A conventional JPEG has no metadata there, but nothing in the format
// forbids it, and Strip — which copies everything from SOS onward verbatim —
// would pass such a segment straight through. This is what lets Verify catch
// that and route the photo to the re-encode fallback.
//
// Progressive JPEGs legitimately have several scans, so this is also what
// makes them walkable at all.
func scanAll(src []byte) ([]segment, error) {
	if len(src) < 4 || src[0] != 0xFF || src[1] != markerSOI {
		return nil, fmt.Errorf("%w: missing SOI", ErrMalformed)
	}

	var segs []segment
	for i := 2; i+1 < len(src); {
		if src[i] != 0xFF {
			return nil, fmt.Errorf("%w: expected a marker at offset %d", ErrMalformed, i)
		}
		m := src[i+1]
		if m == markerEOI {
			return segs, nil
		}
		if m == markerTEM || (m >= markerRST0 && m <= markerRST7) || m == markerSOI {
			i += 2
			continue
		}
		if i+3 >= len(src) {
			return nil, fmt.Errorf("%w: truncated segment length at %d", ErrMalformed, i)
		}
		n := int(binary.BigEndian.Uint16(src[i+2 : i+4]))
		if n < 2 || i+2+n > len(src) {
			return nil, fmt.Errorf("%w: bad segment length %d at offset %d", ErrMalformed, n, i)
		}
		segs = append(segs, segment{marker: m, payload: src[i+4 : i+2+n]})
		i += 2 + n

		if m == markerSOS {
			next := skipEntropy(src, i)
			if next < 0 {
				return nil, fmt.Errorf("%w: scan data has no terminating marker", ErrMalformed)
			}
			i = next
		}
	}
	return segs, nil
}

// skipEntropy advances past entropy-coded data and returns the offset of the
// next real marker's 0xFF, or -1 if the data simply ends.
//
// Inside a scan, 0xFF is always followed by 0x00 (byte stuffing), a restart
// marker, or another 0xFF (fill). Anything else terminates the scan. This is
// exactly why a marker byte cannot occur by accident in image data, and why
// walking past a scan is well-defined rather than a guess.
func skipEntropy(src []byte, i int) int {
	for i < len(src)-1 {
		if src[i] != 0xFF {
			i++
			continue
		}
		switch next := src[i+1]; {
		case next == 0xFF: // fill byte; re-examine at the next one
			i++
		case next == 0x00 || (next >= markerRST0 && next <= markerRST7):
			i += 2
		default:
			return i
		}
	}
	return -1
}

// keep reports whether a parsed segment survives the rewrite. Structural
// segments (quantisation/Huffman tables, frame headers, restart interval) are
// always kept — they are the image, not metadata.
func keep(s segment) bool {
	switch {
	case s.marker == markerCOM:
		return false
	case s.marker == markerAPP2:
		return bytes.HasPrefix(s.payload, iccPrefix)
	case s.marker == markerAPP14:
		return bytes.HasPrefix(s.payload, adobePrefix)
	case s.marker >= markerAPP0 && s.marker <= markerAPP15:
		// Every other APPn — including the source's own APP1, JFIF/JFXX,
		// and IPTC — is dropped. Orientation is re-emitted separately.
		return false
	default:
		return true
	}
}

// buildEXIF returns a complete APP1 segment carrying a minimal big-endian TIFF
// structure whose IFD0 holds exactly one tag: Orientation.
func buildEXIF(orientation int) []byte {
	tiff := make([]byte, 0, 26)
	tiff = append(tiff, 'M', 'M', 0, 42)          // big-endian, TIFF magic
	tiff = append(tiff, 0, 0, 0, 8)               // IFD0 begins immediately after
	tiff = binary.BigEndian.AppendUint16(tiff, 1) // one entry
	tiff = binary.BigEndian.AppendUint16(tiff, tagOrientation)
	tiff = binary.BigEndian.AppendUint16(tiff, 3) // SHORT
	tiff = binary.BigEndian.AppendUint32(tiff, 1) // count
	tiff = binary.BigEndian.AppendUint16(tiff, uint16(orientation))
	tiff = append(tiff, 0, 0)       // pad the 4-byte value field
	tiff = append(tiff, 0, 0, 0, 0) // no next IFD

	payload := append(append([]byte{}, exifPrefix...), tiff...)
	out := []byte{0xFF, markerAPP1}
	out = binary.BigEndian.AppendUint16(out, uint16(len(payload)+2))
	return append(out, payload...)
}

// Orientation returns the EXIF display orientation (1-8) of a JPEG, or 1 when
// it carries none or the block is unreadable. It never fails: an unreadable
// orientation is indistinguishable from the identity for display purposes.
func Orientation(src []byte) int {
	segs, _, err := scan(src)
	if err != nil {
		return 1
	}
	for _, s := range segs {
		if s.marker == markerAPP1 && bytes.HasPrefix(s.payload, exifPrefix) {
			if o := orientationFromEXIF(s.payload); o != 0 {
				return o
			}
			return 1
		}
	}
	return 1
}

// orientationFromEXIF reads IFD0's Orientation tag out of an "Exif\0\0"
// payload. It returns 0 when the tag is absent or the structure is unreadable
// — every offset is bounds-checked, because this parses attacker-controlled
// bytes.
func orientationFromEXIF(payload []byte) int {
	tiff := payload[len(exifPrefix):]
	if len(tiff) < 8 {
		return 0
	}

	var bo binary.ByteOrder
	switch {
	case tiff[0] == 'M' && tiff[1] == 'M':
		bo = binary.BigEndian
	case tiff[0] == 'I' && tiff[1] == 'I':
		bo = binary.LittleEndian
	default:
		return 0
	}
	if bo.Uint16(tiff[2:4]) != 42 {
		return 0
	}

	off := int(bo.Uint32(tiff[4:8]))
	if off < 8 || off+2 > len(tiff) {
		return 0
	}
	count := int(bo.Uint16(tiff[off : off+2]))
	entries := tiff[off+2:]
	if count < 0 || count*12 > len(entries) {
		return 0
	}
	for i := range count {
		e := entries[i*12 : i*12+12]
		if bo.Uint16(e[0:2]) != tagOrientation {
			continue
		}
		if bo.Uint16(e[2:4]) != 3 { // SHORT
			return 0
		}
		v := int(bo.Uint16(e[8:10]))
		if v < 1 || v > 8 {
			return 0
		}
		return v
	}
	return 0
}

// Verify checks that a stripped JPEG is safe to store: it decodes, it has the
// dimensions the caller expected, and no disallowed segment survived. It
// returns the decoded image so the caller does not have to decode again.
//
// Handing the image back is not a convenience — it is the difference between
// one full decode per photo and two. Verify must decode to prove the output is
// readable, and the worker must decode to build the thumbnail; on a burstable
// two-vCPU host a 12 MP decode is the single most expensive step in the
// pipeline (~250 ms locally, more there), so doing it twice would cost more
// than the re-encode this whole design exists to remove.
//
// The photo worker calls this before writing, and falls back to the re-encode
// when it fails. That is what makes the rewrite's risk acceptable — a bug here
// costs a photo its fidelity, not its readability.
func Verify(stripped []byte, wantWidth, wantHeight int) (image.Image, error) {
	cfg, err := jpeg.DecodeConfig(bytes.NewReader(stripped))
	if err != nil {
		return nil, fmt.Errorf("jpegmeta: stripped output has no readable header: %w", err)
	}
	if cfg.Width != wantWidth || cfg.Height != wantHeight {
		return nil, fmt.Errorf("jpegmeta: stripped output is %dx%d, want %dx%d",
			cfg.Width, cfg.Height, wantWidth, wantHeight)
	}
	img, decErr := jpeg.Decode(bytes.NewReader(stripped))
	if decErr != nil {
		return nil, fmt.Errorf("jpegmeta: stripped output does not decode: %w", decErr)
	}

	// scanAll, not scan: metadata placed after the scan data is exactly what
	// Strip cannot see, so verifying with the same limited walk would be
	// checking our own blind spot.
	segs, err := scanAll(stripped)
	if err != nil {
		return nil, fmt.Errorf("jpegmeta: stripped output is not walkable: %w", err)
	}
	seenEXIF := false
	for _, s := range segs {
		if !keep(s) && s.marker != markerAPP1 {
			return nil, fmt.Errorf("jpegmeta: disallowed segment 0x%02X survived", s.marker)
		}
		if s.marker != markerAPP1 {
			continue
		}
		if seenEXIF {
			return nil, errors.New("jpegmeta: more than one APP1 survived")
		}
		seenEXIF = true
		// The only APP1 permitted is one this package built. Comparing
		// against a freshly built block means anything else — a copied-through
		// EXIF, an XMP packet — is rejected without having to enumerate what
		// might be hiding in it.
		want := buildEXIF(orientationFromEXIF(s.payload))
		if !bytes.Equal(s.payload, want[4:]) {
			return nil, errors.New("jpegmeta: APP1 is not a freshly built minimal EXIF block")
		}
	}
	return img, nil
}
