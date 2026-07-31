package activity

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"image/draw"
	"image/jpeg"
	"image/png"
	"net/http"

	xdraw "golang.org/x/image/draw"
	"golang.org/x/image/webp"
)

// photoPipelineOpts configures the two rendered variants processPhoto emits.
// Each variant clamps the image's long edge to its MaxEdge and re-encodes as
// JPEG at its Quality (1-100).
type photoPipelineOpts struct {
	FullMaxEdge, FullQuality   int
	ThumbMaxEdge, ThumbQuality int
}

// processedImage is a single re-encoded JPEG variant plus its pixel
// dimensions. Width/Height describe the encoded bytes, not the source.
type processedImage struct {
	Bytes         []byte
	Width, Height int
}

// maxDecodeEdge bounds the dimensions of any decoded source image. A malicious
// or malformed file can advertise enormous dimensions in its header to force a
// huge allocation on decode (a "decompression bomb"). We reject anything whose
// declared width or height exceeds this before decoding pixels, so the pipeline
// never allocates an unbounded backing buffer. 20000px comfortably exceeds any
// real phone/camera photo (~8000px on the long edge today) while capping a
// single-plane RGBA allocation at ~1.6 GB in the worst case, which we further
// bound via maxDecodePixels below.
const maxDecodeEdge = 20000

// maxDecodePixels bounds total pixel count (width*height) independently of the
// per-edge cap, so a long thin image (e.g. 20000x20000) can't slip through.
// 40 megapixels is generously above any real photo.
const maxDecodePixels = 40 * 1000 * 1000

// ErrUnsupportedImage is returned when the source bytes are not one of the
// supported image formats (JPEG, PNG, WebP) or are too large to decode safely.
var ErrUnsupportedImage = errors.New("activity: unsupported or unsafe image")

// processPhoto is the server-authoritative image pipeline. It decodes src
// (JPEG/PNG/WebP), applies the source's EXIF orientation (JPEG only), resizes
// with a high-quality CatmullRom kernel (aspect preserved, long edge clamped,
// never upscaled), and re-encodes two JPEG variants.
//
// Re-encoding through image/jpeg drops ALL EXIF/GPS metadata: the output
// carries no APP1/Exif segment. That is the privacy mechanism, and it is
// unconditional — there is deliberately no fast path that copies the source
// bytes through.
func processPhoto(src []byte, opts photoPipelineOpts) (full, thumb processedImage, err error) {
	img, format, err := decodeImage(src)
	if err != nil {
		return processedImage{}, processedImage{}, err
	}

	// EXIF orientation only applies to JPEG here; PNG/WebP carry no EXIF
	// orientation in this pipeline. A missing/invalid tag is treated as
	// orientation 1 (identity).
	if format == "jpeg" {
		if o := readJPEGOrientation(src); o >= 2 && o <= 8 {
			img = applyOrientation(img, o)
		}
	}

	full, err = renderVariant(img, opts.FullMaxEdge, opts.FullQuality)
	if err != nil {
		return processedImage{}, processedImage{}, fmt.Errorf("render full variant: %w", err)
	}
	thumb, err = renderVariant(img, opts.ThumbMaxEdge, opts.ThumbQuality)
	if err != nil {
		return processedImage{}, processedImage{}, fmt.Errorf("render thumb variant: %w", err)
	}
	return full, thumb, nil
}

// decodeImage sniffs src with http.DetectContentType and decodes with the
// matching pure-Go decoder. It rejects unknown formats and images whose
// declared dimensions exceed the safety bounds before decoding pixels.
func decodeImage(src []byte) (image.Image, string, error) {
	ct := http.DetectContentType(src)

	var (
		cfg    image.Config
		decode func(*bytes.Reader) (image.Image, error)
		format string
	)
	switch ct {
	case "image/jpeg":
		format = "jpeg"
		c, err := jpeg.DecodeConfig(bytes.NewReader(src))
		if err != nil {
			return nil, "", headerError("jpeg", err)
		}
		cfg = c
		decode = func(r *bytes.Reader) (image.Image, error) { return jpeg.Decode(r) }
	case "image/png":
		format = "png"
		c, err := png.DecodeConfig(bytes.NewReader(src))
		if err != nil {
			return nil, "", headerError("png", err)
		}
		cfg = c
		decode = func(r *bytes.Reader) (image.Image, error) { return png.Decode(r) }
	case "image/webp":
		format = "webp"
		c, err := webp.DecodeConfig(bytes.NewReader(src))
		if err != nil {
			return nil, "", headerError("webp", err)
		}
		cfg = c
		decode = func(r *bytes.Reader) (image.Image, error) { return webp.Decode(r) }
	default:
		return nil, "", fmt.Errorf("%w: content-type %q", ErrUnsupportedImage, ct)
	}

	if err := boundDimensions(cfg.Width, cfg.Height); err != nil {
		return nil, "", err
	}

	img, err := decode(bytes.NewReader(src))
	if err != nil {
		return nil, "", fmt.Errorf("%s decode: %w", format, errors.Join(ErrUnsupportedImage, err))
	}
	return img, format, nil
}

// headerError wraps a decoder's header/config error with ErrUnsupportedImage so
// callers can match the sentinel via errors.Is while preserving the underlying
// cause. errors.Join keeps both errors in the chain (satisfying errorlint's
// "wrap, don't %v" rule) without collapsing them into one opaque string.
func headerError(format string, err error) error {
	return fmt.Errorf("%s header: %w", format, errors.Join(ErrUnsupportedImage, err))
}

// boundDimensions rejects images whose declared size is non-positive or
// exceeds the decompression-bomb safety bounds.
func boundDimensions(w, h int) error {
	if w <= 0 || h <= 0 {
		return fmt.Errorf("%w: non-positive dimensions %dx%d", ErrUnsupportedImage, w, h)
	}
	if w > maxDecodeEdge || h > maxDecodeEdge {
		return fmt.Errorf("%w: dimension exceeds %d (%dx%d)", ErrUnsupportedImage, maxDecodeEdge, w, h)
	}
	// int64 to avoid overflow on the multiply for large-but-in-edge-bound
	// dimensions.
	if int64(w)*int64(h) > maxDecodePixels {
		return fmt.Errorf("%w: %dx%d exceeds %d pixels", ErrUnsupportedImage, w, h, maxDecodePixels)
	}
	return nil
}

// renderVariant resizes img to fit within maxEdge (long edge), never
// upscaling, and JPEG-encodes it at the given quality.
func renderVariant(img image.Image, maxEdge, quality int) (processedImage, error) {
	dst := resize(img, maxEdge)
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, dst, &jpeg.Options{Quality: quality}); err != nil {
		return processedImage{}, err
	}
	b := dst.Bounds()
	return processedImage{Bytes: buf.Bytes(), Width: b.Dx(), Height: b.Dy()}, nil
}

// resize returns img scaled so its long edge equals maxEdge, preserving aspect
// ratio, using the CatmullRom kernel. If the image already fits (both edges <=
// maxEdge) it is returned unchanged — the pipeline never upscales.
func resize(img image.Image, maxEdge int) image.Image {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	if w <= maxEdge && h <= maxEdge {
		return img
	}

	var tw, th int
	if w >= h {
		tw = maxEdge
		th = scaleDim(h, w, maxEdge)
	} else {
		th = maxEdge
		tw = scaleDim(w, h, maxEdge)
	}

	dst := image.NewRGBA(image.Rect(0, 0, tw, th))
	xdraw.CatmullRom.Scale(dst, dst.Bounds(), img, b, xdraw.Over, nil)
	return dst
}

// scaleDim returns the short edge scaled proportionally to the long edge's new
// value (round to nearest, minimum 1). shortSrc/longSrc are the source short
// and long edges; longDst is the target long edge.
func scaleDim(shortSrc, longSrc, longDst int) int {
	if longSrc == 0 {
		return 1
	}
	// round(shortSrc * longDst / longSrc)
	v := (int64(shortSrc)*int64(longDst) + int64(longSrc)/2) / int64(longSrc)
	if v < 1 {
		return 1
	}
	return int(v)
}

// applyOrientation returns img transformed for the given EXIF orientation
// value (2-8). Value 1 (identity) is handled by the caller and never reaches
// here. The transforms follow the EXIF spec:
//
//	2 flip-horizontal   3 rotate180        4 flip-vertical
//	5 transpose         6 rotate90 CW      7 transverse       8 rotate270 CW
//
// For 5-8 the output dimensions are swapped (portrait<->landscape).
func applyOrientation(img image.Image, orientation int) image.Image {
	src := toRGBA(img)
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()

	// swapped is true for the transforms that transpose the axes.
	swapped := orientation >= 5

	dw, dh := w, h
	if swapped {
		dw, dh = h, w
	}
	dst := image.NewRGBA(image.Rect(0, 0, dw, dh))

	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			c := src.RGBAAt(b.Min.X+x, b.Min.Y+y)
			var nx, ny int
			switch orientation {
			case 2: // flip horizontal
				nx, ny = w-1-x, y
			case 3: // rotate 180
				nx, ny = w-1-x, h-1-y
			case 4: // flip vertical
				nx, ny = x, h-1-y
			case 5: // transpose (reflect over main diagonal)
				nx, ny = y, x
			case 6: // rotate 90 CW
				nx, ny = h-1-y, x
			case 7: // transverse (reflect over anti-diagonal)
				nx, ny = h-1-y, w-1-x
			case 8: // rotate 270 CW (= 90 CCW)
				nx, ny = y, w-1-x
			default:
				nx, ny = x, y
			}
			dst.SetRGBA(nx, ny, c)
		}
	}
	return dst
}

// toRGBA returns img as an *image.RGBA, copying only if necessary.
func toRGBA(img image.Image) *image.RGBA {
	if rgba, ok := img.(*image.RGBA); ok {
		return rgba
	}
	b := img.Bounds()
	dst := image.NewRGBA(b)
	draw.Draw(dst, b, img, b.Min, draw.Src)
	return dst
}

// readJPEGOrientation walks the JPEG marker segments looking for the APP1/EXIF
// block, parses its TIFF header and IFD0, and returns the Orientation tag
// (0x0112) value. It returns 0 if there is no APP1/EXIF segment or no
// Orientation entry (caller treats that as identity).
//
// This is a deliberately small, self-contained reader: it only understands
// enough of the JPEG/TIFF/EXIF structure to find one SHORT tag in IFD0. It
// never allocates based on file-declared sizes and bounds every slice access.
func readJPEGOrientation(src []byte) int {
	// JPEG must start with SOI (FFD8).
	if len(src) < 2 || src[0] != 0xFF || src[1] != 0xD8 {
		return 0
	}
	i := 2
	for i+4 <= len(src) {
		// Every marker starts with 0xFF; skip fill bytes.
		if src[i] != 0xFF {
			return 0
		}
		marker := src[i+1]
		i += 2
		// Standalone markers without length payloads.
		if marker == 0xD9 { // EOI
			return 0
		}
		if marker == 0x01 || (marker >= 0xD0 && marker <= 0xD7) {
			continue
		}
		if i+2 > len(src) {
			return 0
		}
		segLen := int(src[i])<<8 | int(src[i+1])
		if segLen < 2 || i+segLen > len(src) {
			return 0
		}
		payload := src[i+2 : i+segLen]
		if marker == 0xE1 { // APP1
			if o, ok := orientationFromEXIF(payload); ok {
				return o
			}
		}
		// SOS (FFDA): scan data follows; stop looking.
		if marker == 0xDA {
			return 0
		}
		i += segLen
	}
	return 0
}

// orientationFromEXIF parses an APP1 payload ("Exif\0\0" + TIFF) and returns
// the IFD0 Orientation value if present. ok is false if the payload is not a
// well-formed EXIF/TIFF block or has no Orientation tag.
func orientationFromEXIF(payload []byte) (int, bool) {
	const exifHeader = "Exif\x00\x00"
	if len(payload) < len(exifHeader) || string(payload[:len(exifHeader)]) != exifHeader {
		return 0, false
	}
	tiff := payload[len(exifHeader):]
	if len(tiff) < 8 {
		return 0, false
	}

	var bo binary.ByteOrder
	switch string(tiff[:2]) {
	case "II":
		bo = binary.LittleEndian
	case "MM":
		bo = binary.BigEndian
	default:
		return 0, false
	}
	if bo.Uint16(tiff[2:4]) != 0x002A {
		return 0, false
	}
	ifdOff := int(bo.Uint32(tiff[4:8]))
	if ifdOff < 8 || ifdOff+2 > len(tiff) {
		return 0, false
	}

	count := int(bo.Uint16(tiff[ifdOff : ifdOff+2]))
	entryStart := ifdOff + 2
	const entrySize = 12
	for e := 0; e < count; e++ {
		off := entryStart + e*entrySize
		if off+entrySize > len(tiff) {
			return 0, false
		}
		tag := bo.Uint16(tiff[off : off+2])
		if tag != 0x0112 { // Orientation
			continue
		}
		typ := bo.Uint16(tiff[off+2 : off+4])
		if typ != 3 { // SHORT
			return 0, false
		}
		// A SHORT value lives in the first 2 bytes of the value field.
		v := int(bo.Uint16(tiff[off+8 : off+10]))
		if v < 1 || v > 8 {
			return 0, false
		}
		return v, true
	}
	return 0, false
}
