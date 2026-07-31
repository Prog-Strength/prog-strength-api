package activity

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"testing"
)

// --- fixture helpers -------------------------------------------------------

// cornerImage builds a w x h image whose four corners carry distinct, fully
// saturated colors so an orientation transform is observable by inspecting a
// single corner. The center is a neutral gray. Corners:
//
//	top-left  = red    top-right    = green
//	bottom-left = blue  bottom-right = white
//
// The colors are chosen far apart in RGB space so JPEG's lossy chroma
// subsampling can't smear one corner into another; tests sample the extreme
// corner pixel and assert on a dominant channel, not an exact value.
func cornerImage(w, h int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			var c color.RGBA
			switch {
			case x < w/2 && y < h/2:
				c = color.RGBA{255, 0, 0, 255} // TL red
			case x >= w/2 && y < h/2:
				c = color.RGBA{0, 255, 0, 255} // TR green
			case x < w/2 && y >= h/2:
				c = color.RGBA{0, 0, 255, 255} // BL blue
			default:
				c = color.RGBA{255, 255, 255, 255} // BR white
			}
			img.Set(x, y, c)
		}
	}
	return img
}

func encodeJPEG(t *testing.T, img image.Image, q int) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: q}); err != nil {
		t.Fatalf("encode jpeg: %v", err)
	}
	return buf.Bytes()
}

func encodePNG(t *testing.T, img image.Image) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode png: %v", err)
	}
	return buf.Bytes()
}

// exifAPP1 builds a minimal, valid APP1/EXIF segment payload (the bytes that
// follow the FF E1 <len> marker) carrying a single IFD0 entry: Orientation
// (tag 0x0112, SHORT) with the given value. Big-endian ("MM") TIFF byte order.
//
// Layout:
//
//	"Exif\0\0"                     — APP1 identifier
//	"MM" 0x002A <IFD0 offset=8>    — TIFF header
//	<IFD0>: count=1, one 12-byte entry, next-IFD offset=0
func exifAPP1(orientation uint16) []byte {
	var b bytes.Buffer
	b.WriteString("Exif\x00\x00")

	tiff := new(bytes.Buffer)
	tiff.WriteString("MM") // big-endian
	_ = binary.Write(tiff, binary.BigEndian, uint16(0x002A))
	_ = binary.Write(tiff, binary.BigEndian, uint32(8)) // IFD0 at offset 8
	// IFD0
	_ = binary.Write(tiff, binary.BigEndian, uint16(1))      // entry count
	_ = binary.Write(tiff, binary.BigEndian, uint16(0x0112)) // tag: Orientation
	_ = binary.Write(tiff, binary.BigEndian, uint16(3))      // type: SHORT
	_ = binary.Write(tiff, binary.BigEndian, uint32(1))      // count
	// value: a SHORT occupies the first 2 bytes of the 4-byte value field.
	_ = binary.Write(tiff, binary.BigEndian, orientation)
	_ = binary.Write(tiff, binary.BigEndian, uint16(0)) // padding
	_ = binary.Write(tiff, binary.BigEndian, uint32(0)) // next IFD = none

	b.Write(tiff.Bytes())
	return b.Bytes()
}

// jpegWithOrientation encodes img as JPEG and splices in an APP1/EXIF segment
// (right after SOI) carrying the given Orientation value.
func jpegWithOrientation(t *testing.T, img image.Image, orientation uint16) []byte {
	t.Helper()
	base := encodeJPEG(t, img, 90)
	if len(base) < 2 || base[0] != 0xFF || base[1] != 0xD8 {
		t.Fatalf("base jpeg missing SOI")
	}
	payload := exifAPP1(orientation)
	seg := make([]byte, 0, 4+len(payload))
	seg = append(seg, 0xFF, 0xE1)
	// segment length covers the 2 length bytes + payload
	segLen := len(payload) + 2
	seg = append(seg, byte(segLen>>8), byte(segLen&0xFF))
	seg = append(seg, payload...)

	out := make([]byte, 0, len(base)+len(seg))
	out = append(out, base[:2]...) // SOI
	out = append(out, seg...)      // APP1/EXIF
	out = append(out, base[2:]...) // remainder
	return out
}

// jpegWithGPS builds a JPEG carrying an EXIF APP1 that includes both an
// Orientation tag in IFD0 and a GPS IFD (with a latitude tag), so a test can
// assert the pipeline strips GPS along with all other metadata.
func jpegWithGPS(t *testing.T, img image.Image) []byte {
	t.Helper()
	base := encodeJPEG(t, img, 90)

	tiff := new(bytes.Buffer)
	tiff.WriteString("MM")
	_ = binary.Write(tiff, binary.BigEndian, uint16(0x002A))
	_ = binary.Write(tiff, binary.BigEndian, uint32(8)) // IFD0 at offset 8

	// IFD0: 2 entries (Orientation + GPS IFD pointer), next=0.
	_ = binary.Write(tiff, binary.BigEndian, uint16(2))
	// Orientation = 1
	_ = binary.Write(tiff, binary.BigEndian, uint16(0x0112))
	_ = binary.Write(tiff, binary.BigEndian, uint16(3))
	_ = binary.Write(tiff, binary.BigEndian, uint32(1))
	_ = binary.Write(tiff, binary.BigEndian, uint16(1))
	_ = binary.Write(tiff, binary.BigEndian, uint16(0))
	// GPSInfo IFD pointer (tag 0x8825, LONG) -> offset computed below.
	// IFD0 occupies: 2 (count) + 2*12 (entries) + 4 (next) = 30 bytes,
	// starting at TIFF offset 8, so GPS IFD begins at 8+30 = 38.
	const gpsIFDOffset = 38
	_ = binary.Write(tiff, binary.BigEndian, uint16(0x8825))
	_ = binary.Write(tiff, binary.BigEndian, uint16(4)) // LONG
	_ = binary.Write(tiff, binary.BigEndian, uint32(1))
	_ = binary.Write(tiff, binary.BigEndian, uint32(gpsIFDOffset))
	_ = binary.Write(tiff, binary.BigEndian, uint32(0)) // next IFD

	// GPS IFD: 1 entry: GPSLatitudeRef (0x0001, ASCII "N\0").
	_ = binary.Write(tiff, binary.BigEndian, uint16(1))
	_ = binary.Write(tiff, binary.BigEndian, uint16(0x0001))
	_ = binary.Write(tiff, binary.BigEndian, uint16(2)) // ASCII
	_ = binary.Write(tiff, binary.BigEndian, uint32(2)) // count
	tiff.WriteString("N\x00")                           // fits inline (2 bytes)
	_ = binary.Write(tiff, binary.BigEndian, uint16(0)) // pad to 4
	_ = binary.Write(tiff, binary.BigEndian, uint32(0)) // next IFD

	var payload bytes.Buffer
	payload.WriteString("Exif\x00\x00")
	payload.Write(tiff.Bytes())

	seg := make([]byte, 0, 4+payload.Len())
	seg = append(seg, 0xFF, 0xE1)
	segLen := payload.Len() + 2
	seg = append(seg, byte(segLen>>8), byte(segLen&0xFF))
	seg = append(seg, payload.Bytes()...)

	out := make([]byte, 0, len(base)+len(seg))
	out = append(out, base[:2]...)
	out = append(out, seg...)
	out = append(out, base[2:]...)
	return out
}

// containsEXIF reports whether the JPEG carries an APP1 "Exif\0\0" segment.
func containsEXIF(jpg []byte) bool {
	return bytes.Contains(jpg, []byte("Exif\x00\x00"))
}

// dominantChannel returns 'r','g','b','w','k' for the loudest channel of the
// corner pixel at (x,y) of the decoded JPEG bytes.
func cornerColor(t *testing.T, jpg []byte, x, y int) string {
	t.Helper()
	img, _, err := image.Decode(bytes.NewReader(jpg))
	if err != nil {
		t.Fatalf("decode for corner check: %v", err)
	}
	r, g, b, _ := img.At(x, y).RGBA()
	r, g, b = r>>8, g>>8, b>>8
	const hi, lo = 160, 96
	switch {
	case r > hi && g > hi && b > hi:
		return "w"
	case r < lo && g < lo && b < lo:
		return "k"
	case r >= g && r >= b:
		return "r"
	case g >= r && g >= b:
		return "g"
	default:
		return "b"
	}
}

func mustDecodeCfg(t *testing.T, jpg []byte) (int, int) {
	t.Helper()
	cfg, _, err := image.DecodeConfig(bytes.NewReader(jpg))
	if err != nil {
		t.Fatalf("decode config: %v", err)
	}
	return cfg.Width, cfg.Height
}

func defaultOpts() photoPipelineOpts {
	return photoPipelineOpts{
		FullMaxEdge: 100, FullQuality: 85,
		ThumbMaxEdge: 40, ThumbQuality: 70,
	}
}

// --- tests -----------------------------------------------------------------

// TestProcessPhotoPipeline_DecodesAllSourceFormats confirms JPEG, PNG, and a
// real WebP fixture all decode and produce two JPEG variants.
func TestProcessPhotoPipeline_DecodesAllSourceFormats(t *testing.T) {
	t.Parallel()

	webpBytes, err := os.ReadFile(filepath.Join("testdata", "photos", "gopher.webp"))
	if err != nil {
		t.Fatalf("read webp fixture: %v", err)
	}

	sources := map[string][]byte{
		"jpeg": encodeJPEG(t, cornerImage(200, 160), 90),
		"png":  encodePNG(t, cornerImage(200, 160)),
		"webp": webpBytes,
	}

	for name, src := range sources {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			full, thumb, err := processPhoto(src, defaultOpts())
			if err != nil {
				t.Fatalf("processPhoto(%s): %v", name, err)
			}
			for _, v := range []processedImage{full, thumb} {
				if len(v.Bytes) < 3 || v.Bytes[0] != 0xFF || v.Bytes[1] != 0xD8 || v.Bytes[2] != 0xFF {
					t.Fatalf("%s: output not JPEG (magic %x)", name, v.Bytes[:min(3, len(v.Bytes))])
				}
				w, h := mustDecodeCfg(t, v.Bytes)
				if w != v.Width || h != v.Height {
					t.Fatalf("%s: reported dims %dx%d != decoded %dx%d", name, v.Width, v.Height, w, h)
				}
			}
			if long(full.Width, full.Height) > 100 {
				t.Fatalf("%s: full long edge %d > 100", name, long(full.Width, full.Height))
			}
			if long(thumb.Width, thumb.Height) > 40 {
				t.Fatalf("%s: thumb long edge %d > 40", name, long(thumb.Width, thumb.Height))
			}
		})
	}
}

// TestProcessPhotoPipeline_ClampsLongEdgeAndPreservesAspect checks the resize
// math: long edge clamped, aspect preserved within rounding.
func TestProcessPhotoPipeline_ClampsLongEdgeAndPreservesAspect(t *testing.T) {
	t.Parallel()

	// 400x300 landscape (4:3). Full max 100 -> 100x75. Thumb max 40 -> 40x30.
	src := encodeJPEG(t, cornerImage(400, 300), 90)
	full, thumb, err := processPhoto(src, defaultOpts())
	if err != nil {
		t.Fatalf("processPhoto: %v", err)
	}
	if full.Width != 100 || full.Height != 75 {
		t.Fatalf("full = %dx%d, want 100x75", full.Width, full.Height)
	}
	if thumb.Width != 40 || thumb.Height != 30 {
		t.Fatalf("thumb = %dx%d, want 40x30", thumb.Width, thumb.Height)
	}
}

// TestProcessPhotoPipeline_NeverUpscales confirms an under-size source is
// returned at its native dimensions.
func TestProcessPhotoPipeline_NeverUpscales(t *testing.T) {
	t.Parallel()

	// 30x20, both dims under both maxes -> unchanged in both variants.
	src := encodeJPEG(t, cornerImage(30, 20), 90)
	full, thumb, err := processPhoto(src, defaultOpts())
	if err != nil {
		t.Fatalf("processPhoto: %v", err)
	}
	if full.Width != 30 || full.Height != 20 {
		t.Fatalf("full = %dx%d, want 30x20 (no upscale)", full.Width, full.Height)
	}
	if thumb.Width != 30 || thumb.Height != 20 {
		t.Fatalf("thumb = %dx%d, want 30x20 (no upscale)", thumb.Width, thumb.Height)
	}
}

// TestProcessPhotoPipeline_ShippedConfigKeepsFullAtNativeResolution pins the
// fidelity policy the committed [photos] config encodes: because the `full`
// variant is the ARCHIVAL copy (no original bytes are retained anywhere), its
// max edge is set to maxDecodeEdge so nothing the decoder accepts is ever
// downscaled. A regression here — someone reinstating a 2048-style clamp —
// silently and permanently degrades every photo uploaded after it.
//
// The thumb still resizes; only the full variant is native.
func TestProcessPhotoPipeline_ShippedConfigKeepsFullAtNativeResolution(t *testing.T) {
	t.Parallel()

	// The shipped values (config.toml [photos]). Kept as literals rather than
	// loading the file so this test states the contract it's defending.
	shipped := photoPipelineOpts{
		FullMaxEdge: maxDecodeEdge, FullQuality: 95,
		ThumbMaxEdge: 800, ThumbQuality: 85,
	}

	// Stand-in for a 12MP phone photo's aspect and scale relationship: well
	// past the old 2048 clamp, cheap to synthesize.
	const w, h = 3000, 2250
	src := encodeJPEG(t, cornerImage(w, h), 95)
	full, thumb, err := processPhoto(src, shipped)
	if err != nil {
		t.Fatalf("processPhoto: %v", err)
	}
	if full.Width != w || full.Height != h {
		t.Errorf("full = %dx%d, want %dx%d (native resolution, no downscale)", full.Width, full.Height, w, h)
	}
	// The old clamp would have produced 2048x1536; assert we're not there.
	if full.Width == 2048 {
		t.Errorf("full was clamped to 2048 — the archival variant must not downscale")
	}
	// Thumb still resizes: 3000x2250 at max edge 800 -> 800x600.
	if thumb.Width != 800 || thumb.Height != 600 {
		t.Errorf("thumb = %dx%d, want 800x600", thumb.Width, thumb.Height)
	}
	// Near-lossless full should be substantially heavier than the thumb; a
	// collapse toward thumb size would mean the quality knob got lost.
	if len(full.Bytes) <= len(thumb.Bytes) {
		t.Errorf("full (%d B) should exceed thumb (%d B)", len(full.Bytes), len(thumb.Bytes))
	}
}

// TestProcessPhotoPipeline_Orientation covers all 8 EXIF orientation values.
// For each, we assert the output dimensions (landscape vs portrait swap for
// the 90/270 family) and the resulting top-left corner color, which the
// transform relocates predictably.
func TestProcessPhotoPipeline_Orientation(t *testing.T) {
	t.Parallel()

	// A 120x80 landscape. TL red, TR green, BL blue, BR white.
	const w, h = 120, 80
	base := cornerImage(w, h)

	// Expected TL-corner color after each transform, and whether the
	// output is portrait (w/h swapped). Derived from the source corners:
	//   TL=red TR=green BL=blue BR=white
	cases := []struct {
		orient   uint16
		wantTL   string
		portrait bool
	}{
		{1, "r", false}, // identity: TL stays red
		{2, "g", false}, // flip-horizontal: TR(green) -> TL
		{3, "w", false}, // rotate180: BR(white) -> TL
		{4, "b", false}, // flip-vertical: BL(blue) -> TL
		{5, "r", true},  // transpose (flip along main diagonal): TL stays
		{6, "b", true},  // rotate90 CW: BL(blue) -> TL
		{7, "w", true},  // transverse: BR(white) -> TL
		{8, "g", true},  // rotate270 CW: TR(green) -> TL
	}

	// Use a large max so no resize happens and corner colors stay crisp.
	opts := photoPipelineOpts{FullMaxEdge: 1000, FullQuality: 95, ThumbMaxEdge: 1000, ThumbQuality: 95}

	for _, tc := range cases {
		tc := tc
		t.Run(orientName(tc.orient), func(t *testing.T) {
			t.Parallel()
			src := jpegWithOrientation(t, base, tc.orient)
			full, _, err := processPhoto(src, opts)
			if err != nil {
				t.Fatalf("processPhoto orient=%d: %v", tc.orient, err)
			}
			wantW, wantH := w, h
			if tc.portrait {
				wantW, wantH = h, w
			}
			if full.Width != wantW || full.Height != wantH {
				t.Fatalf("orient=%d dims=%dx%d want %dx%d", tc.orient, full.Width, full.Height, wantW, wantH)
			}
			got := cornerColor(t, full.Bytes, 2, 2)
			if got != tc.wantTL {
				t.Fatalf("orient=%d TL corner=%q want %q", tc.orient, got, tc.wantTL)
			}
		})
	}
}

// TestProcessPhoto_StripsEXIFAndGPS is the privacy guarantee: a source JPEG
// carrying EXIF + a GPS IFD must yield outputs with no APP1/Exif marker at all.
func TestProcessPhoto_StripsEXIFAndGPS(t *testing.T) {
	t.Parallel()

	src := jpegWithGPS(t, cornerImage(200, 150))
	if !containsEXIF(src) {
		t.Fatalf("test fixture bug: source JPEG has no EXIF to strip")
	}

	full, thumb, err := processPhoto(src, defaultOpts())
	if err != nil {
		t.Fatalf("processPhoto: %v", err)
	}
	for name, out := range map[string]processedImage{"full": full, "thumb": thumb} {
		if containsEXIF(out.Bytes) {
			t.Fatalf("%s output still carries EXIF/GPS metadata", name)
		}
	}
}

// TestProcessPhotoPipeline_UnknownFormatErrors ensures non-image bytes error.
func TestProcessPhotoPipeline_UnknownFormatErrors(t *testing.T) {
	t.Parallel()
	if _, _, err := processPhoto([]byte("not an image at all, just text"), defaultOpts()); err == nil {
		t.Fatalf("expected error for unknown format, got nil")
	}
}

func orientName(o uint16) string {
	return "orient" + string(rune('0'+o))
}

func long(w, h int) int {
	if w > h {
		return w
	}
	return h
}
