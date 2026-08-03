package jpegmeta_test

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/jpeg"
	"math/rand"
	"testing"

	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/jpegmeta"
)

// --- fixtures ----------------------------------------------------------
//
// Every fixture is built in-process rather than checked in as a binary, so a
// reader can see exactly which segments a case does and does not carry.

const (
	mSOI   = 0xD8
	mSOS   = 0xDA
	mEOI   = 0xD9
	mAPP1  = 0xE1
	mAPP2  = 0xE2
	mAPP13 = 0xED
	mCOM   = 0xFE
)

// baseJPEG is a real, decodable JPEG carrying no application segments beyond
// whatever image/jpeg itself writes (which is none — see the ICC finding).
func baseJPEG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	rng := rand.New(rand.NewSource(7))
	for y := range h {
		for x := range w {
			img.Set(x, y, color.RGBA{uint8(rng.Intn(256)), uint8(x % 256), uint8(y % 256), 255})
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 90}); err != nil {
		t.Fatalf("encode base jpeg: %v", err)
	}
	return buf.Bytes()
}

// seg builds one marker segment: FF <marker> <2-byte length incl. itself> <payload>.
func seg(marker byte, payload []byte) []byte {
	out := []byte{0xFF, marker, 0, 0}
	binary.BigEndian.PutUint16(out[2:4], uint16(len(payload)+2))
	return append(out, payload...)
}

// inject splices segments in immediately after SOI, which is where a camera
// writes its APPn block.
func inject(base []byte, segs ...[]byte) []byte {
	out := append([]byte{}, base[:2]...)
	for _, s := range segs {
		out = append(out, s...)
	}
	return append(out, base[2:]...)
}

// exifPayload builds an "Exif\0\0" + TIFF payload carrying Orientation and,
// optionally, a GPS IFD — the shape a phone photo actually has.
func exifPayload(orientation uint16, withGPS bool) []byte {
	var tiff []byte
	tiff = append(tiff, 'M', 'M', 0, 42)          // big-endian, magic
	tiff = append(tiff, 0, 0, 0, 8)               // IFD0 at offset 8
	entries := [][]byte{orientEntry(orientation)} // tag 0x0112

	gpsIFDOffset := 8 + 2 + 12*2 + 4 // IFD0 header + 2 entries + next-offset
	if withGPS {
		e := make([]byte, 12)
		binary.BigEndian.PutUint16(e[0:2], 0x8825) // GPSInfoIFDPointer
		binary.BigEndian.PutUint16(e[2:4], 4)      // LONG
		binary.BigEndian.PutUint32(e[4:8], 1)
		binary.BigEndian.PutUint32(e[8:12], uint32(gpsIFDOffset))
		entries = append(entries, e)
	}

	ifd0 := make([]byte, 2)
	binary.BigEndian.PutUint16(ifd0, uint16(len(entries)))
	for _, e := range entries {
		ifd0 = append(ifd0, e...)
	}
	ifd0 = append(ifd0, 0, 0, 0, 0) // no next IFD
	tiff = append(tiff, ifd0...)

	if withGPS {
		// One GPS tag is enough to prove the whole IFD is dropped.
		gps := make([]byte, 2)
		binary.BigEndian.PutUint16(gps, 1)
		e := make([]byte, 12)
		binary.BigEndian.PutUint16(e[0:2], 0x0001) // GPSLatitudeRef
		binary.BigEndian.PutUint16(e[2:4], 2)      // ASCII
		binary.BigEndian.PutUint32(e[4:8], 2)
		copy(e[8:12], "N\x00")
		gps = append(gps, e...)
		gps = append(gps, 0, 0, 0, 0)
		tiff = append(tiff, gps...)
	}
	return append([]byte("Exif\x00\x00"), tiff...)
}

func orientEntry(v uint16) []byte {
	e := make([]byte, 12)
	binary.BigEndian.PutUint16(e[0:2], 0x0112) // Orientation
	binary.BigEndian.PutUint16(e[2:4], 3)      // SHORT
	binary.BigEndian.PutUint32(e[4:8], 1)
	binary.BigEndian.PutUint16(e[8:10], v) // value inline, big-endian
	return e
}

const iccMagic = "ICC_PROFILE\x00"

func iccPayload() []byte {
	p := []byte(iccMagic)
	p = append(p, 1, 1) // chunk 1 of 1
	return append(p, []byte("DISPLAY-P3-PROFILE-BODY")...)
}

func xmpPayload() []byte {
	return append([]byte("http://ns.adobe.com/xap/1.0/\x00"),
		[]byte(`<x:xmpmeta><exif:GPSLatitude>51,30.0N</exif:GPSLatitude></x:xmpmeta>`)...)
}

// --- helpers for assertions -------------------------------------------

// markers walks the segment structure and returns each marker byte, stopping
// once entropy-coded data begins.
func markers(t *testing.T, b []byte) []byte {
	t.Helper()
	var out []byte
	if len(b) < 2 || b[0] != 0xFF || b[1] != mSOI {
		t.Fatalf("not a JPEG: % x", b[:min(4, len(b))])
	}
	for i := 2; i+1 < len(b); {
		if b[i] != 0xFF {
			t.Fatalf("desynced at %d: % x", i, b[i:min(i+4, len(b))])
		}
		m := b[i+1]
		out = append(out, m)
		if m == mSOS || m == mEOI {
			return out
		}
		if i+3 >= len(b) {
			t.Fatalf("truncated length at %d", i)
		}
		i += 2 + int(binary.BigEndian.Uint16(b[i+2:i+4]))
	}
	return out
}

func hasMarker(t *testing.T, b []byte, m byte) bool {
	t.Helper()
	return bytes.IndexByte(markers(t, b), m) >= 0
}

// segPayload returns the payload of the first segment with the given marker.
func segPayload(t *testing.T, b []byte, marker byte) []byte {
	t.Helper()
	for i := 2; i+3 < len(b); {
		m := b[i+1]
		if m == mSOS || m == mEOI {
			return nil
		}
		n := int(binary.BigEndian.Uint16(b[i+2 : i+4]))
		if m == marker {
			return b[i+4 : i+2+n]
		}
		i += 2 + n
	}
	return nil
}

// scanFrom returns everything from the SOS marker to the end — the
// entropy-coded image data plus its SOS header.
func scanFrom(t *testing.T, b []byte) []byte {
	t.Helper()
	for i := 2; i+3 < len(b); {
		if b[i+1] == mSOS {
			return b[i:]
		}
		i += 2 + int(binary.BigEndian.Uint16(b[i+2:i+4]))
	}
	t.Fatal("no SOS found")
	return nil
}

// --- the tests ---------------------------------------------------------

// The privacy guarantee: nothing that can carry location or identity survives.
func TestStripRemovesEverySensitiveSegment(t *testing.T) {
	src := inject(baseJPEG(t, 48, 32),
		seg(mAPP1, exifPayload(6, true)),             // EXIF incl. GPS IFD
		seg(mAPP1, xmpPayload()),                     // XMP, which also carries GPS
		seg(mAPP13, []byte("Photoshop 3.0\x008BIM")), // IPTC
		seg(mCOM, []byte("shot on a phone at home")),
	)

	got, err := jpegmeta.Strip(src)
	if err != nil {
		t.Fatalf("Strip: %v", err)
	}

	if hasMarker(t, got, mAPP13) {
		t.Error("APP13/IPTC survived the strip")
	}
	if hasMarker(t, got, mCOM) {
		t.Error("COM comment survived the strip")
	}
	for _, needle := range []string{"GPSLatitude", "8BIM", "shot on a phone", "xmpmeta"} {
		if bytes.Contains(got, []byte(needle)) {
			t.Errorf("stripped output still contains %q", needle)
		}
	}
	// The GPS IFD tag id must not survive inside the rebuilt EXIF either.
	if app1 := segPayload(t, got, mAPP1); bytes.Contains(app1, []byte{0x88, 0x25}) {
		t.Error("rebuilt EXIF still carries a GPSInfoIFDPointer tag")
	}
}

// ICC is kept — this is the color bug the re-encode causes, fixed.
func TestStripPreservesICCProfile(t *testing.T) {
	icc := iccPayload()
	src := inject(baseJPEG(t, 32, 32), seg(mAPP2, icc), seg(mAPP1, exifPayload(1, true)))

	got, err := jpegmeta.Strip(src)
	if err != nil {
		t.Fatalf("Strip: %v", err)
	}

	kept := segPayload(t, got, mAPP2)
	if !bytes.Equal(kept, icc) {
		t.Errorf("ICC payload changed:\n got %q\nwant %q", kept, icc)
	}
}

// Orientation is kept, because dropping it would display portrait photos
// sideways once we stop baking rotation into pixels.
func TestStripPreservesOrientation(t *testing.T) {
	for _, want := range []uint16{2, 3, 4, 5, 6, 7, 8} {
		src := inject(baseJPEG(t, 24, 16), seg(mAPP1, exifPayload(want, true)))
		got, err := jpegmeta.Strip(src)
		if err != nil {
			t.Fatalf("orientation %d: Strip: %v", want, err)
		}
		if o := jpegmeta.Orientation(got); o != int(want) {
			t.Errorf("orientation %d: round-tripped as %d", want, o)
		}
	}
}

// Orientation 1 is the identity, so emitting an EXIF block for it is pure
// noise — no APP1 at all is the correct output.
func TestStripEmitsNoEXIFWhenOrientationIsIdentity(t *testing.T) {
	src := inject(baseJPEG(t, 24, 16), seg(mAPP1, exifPayload(1, true)))
	got, err := jpegmeta.Strip(src)
	if err != nil {
		t.Fatalf("Strip: %v", err)
	}
	if hasMarker(t, got, mAPP1) {
		t.Error("emitted an APP1 for orientation 1; expected none")
	}
}

// The claim the whole design rests on: the image data is untouched.
func TestStripLeavesScanDataByteIdentical(t *testing.T) {
	base := baseJPEG(t, 64, 48)
	src := inject(base,
		seg(mAPP1, exifPayload(6, true)),
		seg(mAPP2, iccPayload()),
		seg(mCOM, []byte("x")),
	)

	got, err := jpegmeta.Strip(src)
	if err != nil {
		t.Fatalf("Strip: %v", err)
	}

	if want, have := scanFrom(t, src), scanFrom(t, got); !bytes.Equal(want, have) {
		t.Errorf("scan data changed: %d bytes in, %d out", len(want), len(have))
	}
	// And against the untouched original, not just the injected fixture.
	if want, have := scanFrom(t, base), scanFrom(t, got); !bytes.Equal(want, have) {
		t.Error("scan data differs from the pre-injection original")
	}
}

func TestStripOutputStillDecodesAtSameDimensions(t *testing.T) {
	src := inject(baseJPEG(t, 70, 40), seg(mAPP1, exifPayload(8, true)), seg(mCOM, []byte("c")))

	got, err := jpegmeta.Strip(src)
	if err != nil {
		t.Fatalf("Strip: %v", err)
	}

	cfg, err := jpeg.DecodeConfig(bytes.NewReader(got))
	if err != nil {
		t.Fatalf("stripped output does not decode: %v", err)
	}
	if cfg.Width != 70 || cfg.Height != 40 {
		t.Errorf("dimensions = %dx%d, want 70x40", cfg.Width, cfg.Height)
	}
	if _, err := jpeg.Decode(bytes.NewReader(got)); err != nil {
		t.Fatalf("stripped output fails a full decode: %v", err)
	}
}

// A JPEG that never had metadata is still valid input.
func TestStripHandlesJPEGWithNoApplicationSegments(t *testing.T) {
	base := baseJPEG(t, 16, 16)
	got, err := jpegmeta.Strip(base)
	if err != nil {
		t.Fatalf("Strip: %v", err)
	}
	if !bytes.Equal(scanFrom(t, base), scanFrom(t, got)) {
		t.Error("scan data changed on a metadata-free source")
	}
	if _, err := jpeg.Decode(bytes.NewReader(got)); err != nil {
		t.Fatalf("output does not decode: %v", err)
	}
}

// Hostile / malformed input must fail cleanly, never panic.
func TestStripRejectsMalformedInputWithoutPanicking(t *testing.T) {
	base := baseJPEG(t, 24, 24)
	bogusLen := inject(base, seg(mAPP1, exifPayload(6, false)))
	// Rewrite APP1's length so it runs past EOF.
	binary.BigEndian.PutUint16(bogusLen[4:6], 0xFFF0)

	cases := map[string][]byte{
		"empty":              {},
		"soi only":           {0xFF, mSOI},
		"not a jpeg":         []byte("GIF89a and then some"),
		"truncated mid-scan": base[:len(base)/2],
		"length past EOF":    bogusLen,
		"length of zero":     inject(base, []byte{0xFF, mAPP1, 0x00, 0x00}),
	}

	for name, in := range cases {
		t.Run(name, func(t *testing.T) {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("panicked on %s: %v", name, r)
				}
			}()
			out, err := jpegmeta.Strip(in)
			if err != nil {
				return // clean terminal failure is the expected outcome
			}
			// If it claims success the output must genuinely be decodable.
			if _, derr := jpeg.Decode(bytes.NewReader(out)); derr != nil {
				t.Fatalf("returned nil error but output does not decode: %v", derr)
			}
		})
	}
}

// Two EXIF blocks: only one may survive, and it must not smuggle GPS through.
func TestStripCollapsesDuplicateEXIFSegments(t *testing.T) {
	src := inject(baseJPEG(t, 24, 24),
		seg(mAPP1, exifPayload(6, true)),
		seg(mAPP1, exifPayload(3, true)),
	)
	got, err := jpegmeta.Strip(src)
	if err != nil {
		t.Fatalf("Strip: %v", err)
	}
	if n := bytes.Count(markers(t, got), []byte{mAPP1}); n > 1 {
		t.Errorf("emitted %d APP1 segments, want at most 1", n)
	}
	if app1 := segPayload(t, got, mAPP1); bytes.Contains(app1, []byte{0x88, 0x25}) {
		t.Error("rebuilt EXIF carries a GPS pointer")
	}
}

// --- Verify ------------------------------------------------------------

func TestVerifyAcceptsAGoodStrip(t *testing.T) {
	src := inject(baseJPEG(t, 40, 28), seg(mAPP1, exifPayload(6, true)), seg(mCOM, []byte("c")))
	got, err := jpegmeta.Strip(src)
	if err != nil {
		t.Fatalf("Strip: %v", err)
	}
	if _, err := jpegmeta.Verify(got, 40, 28); err != nil {
		t.Errorf("Verify rejected a good strip: %v", err)
	}
}

func TestVerifyRejectsCorruptOrWrongOutput(t *testing.T) {
	good, err := jpegmeta.Strip(baseJPEG(t, 40, 28))
	if err != nil {
		t.Fatal(err)
	}

	// Deliberately no "corrupt the entropy data" case: Go's decoder tolerates
	// arbitrary bytes inside a scan and does not require EOI, so such a case
	// would assert a behavior the decoder does not have. Truncation is the
	// deterministic way to exercise Verify's decode check.
	cases := map[string]struct {
		in   []byte
		w, h int
	}{
		"wrong dimensions": {good, 41, 28},
		"truncated":        {good[:len(good)/3], 40, 28},
		"not a jpeg":       {[]byte("nope"), 40, 28},
		"empty":            {nil, 40, 28},
		"leftover comment": {inject(good, seg(mCOM, []byte("leaked"))), 40, 28},
		"leftover IPTC":    {inject(good, seg(mAPP13, []byte("8BIM"))), 40, 28},
	}

	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := jpegmeta.Verify(c.in, c.w, c.h); err == nil {
				t.Errorf("Verify accepted %s", name)
			}
		})
	}
}

// Metadata is conventionally written before the scan, but nothing in the
// format requires it — a segment placed after SOS is still parsed by readers.
// Strip walks only as far as SOS and copies the rest verbatim, so it will pass
// such a segment through. Verify is the backstop that must catch it, which is
// what routes the photo to the re-encode fallback where everything is dropped
// unconditionally.
//
// This matters because 0xFF inside entropy-coded data is always followed by
// 0x00 or a restart marker, so a marker byte appearing there is by definition
// not image data.
func TestVerifyRejectsMetadataHiddenAfterTheScan(t *testing.T) {
	good, err := jpegmeta.Strip(baseJPEG(t, 40, 28))
	if err != nil {
		t.Fatal(err)
	}

	// Splice each segment in just before the trailing EOI.
	spliceBeforeEOI := func(payload []byte) []byte {
		cut := len(good) - 2
		out := append([]byte{}, good[:cut]...)
		out = append(out, payload...)
		return append(out, good[cut:]...)
	}

	cases := map[string][]byte{
		"IPTC after scan":     spliceBeforeEOI(seg(mAPP13, []byte("Photoshop 3.0\x008BIM"))),
		"comment after scan":  spliceBeforeEOI(seg(mCOM, []byte("home address"))),
		"XMP after scan":      spliceBeforeEOI(seg(mAPP1, xmpPayload())),
		"EXIF+GPS after scan": spliceBeforeEOI(seg(mAPP1, exifPayload(6, true))),
	}

	for name, in := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := jpegmeta.Verify(in, 40, 28); err == nil {
				t.Errorf("Verify accepted %s — metadata would reach the bucket", name)
			}
		})
	}
}

// --- error-path coverage ----------------------------------------------
//
// The cases above prove the happy path and the headline adversarial ones.
// These target the remaining bounds checks individually, because this parser
// reads attacker-controlled bytes and its error branches are the part that
// must not be reasoned about only in the abstract.

// A JPEG whose segment structure desyncs — a byte where a marker must be.
func TestStripRejectsDesyncedSegmentStructure(t *testing.T) {
	base := baseJPEG(t, 24, 24)
	desynced := inject(base, seg(mCOM, []byte("x")))
	// Corrupt the byte that must be 0xFF at the start of the next marker.
	desynced[4+2+1+2] = 0x41

	if _, err := jpegmeta.Strip(desynced); err == nil {
		t.Error("Strip accepted a desynced segment structure")
	}
}

// EOI before any scan: structurally a JPEG, but there is no image in it.
func TestStripRejectsEOIBeforeScan(t *testing.T) {
	in := []byte{0xFF, mSOI, 0xFF, mEOI}
	if _, err := jpegmeta.Strip(in); err == nil {
		t.Error("Strip accepted a file whose EOI precedes any scan")
	}
}

// Segments that carry no length byte at all must not be treated as if they do.
func TestStripSkipsStandaloneMarkers(t *testing.T) {
	base := baseJPEG(t, 24, 24)
	// TEM (0xFF01) is standalone: two bytes, no length field. Mis-parsing it
	// as a length-bearing segment would read the following bytes as a size.
	withTEM := inject(base, []byte{0xFF, 0x01})

	got, err := jpegmeta.Strip(withTEM)
	if err != nil {
		t.Fatalf("Strip rejected a standalone marker: %v", err)
	}
	if _, err := jpeg.Decode(bytes.NewReader(got)); err != nil {
		t.Fatalf("output does not decode: %v", err)
	}
}

// APP2 that is not an ICC profile carries no color meaning and is dropped;
// APP14/Adobe is kept, because image/jpeg reads it to decide how to interpret
// CMYK/YCCK and losing it would change how the file decodes.
func TestStripKeepsAdobeButDropsNonICCAPP2(t *testing.T) {
	const mAPP14 = 0xEE
	adobe := append([]byte("Adobe"), 0, 100, 0, 0, 0, 0, 1)
	src := inject(baseJPEG(t, 24, 24),
		seg(mAPP2, []byte("NOT-AN-ICC-PROFILE-JUST-DATA")),
		seg(mAPP14, adobe),
	)

	got, err := jpegmeta.Strip(src)
	if err != nil {
		t.Fatalf("Strip: %v", err)
	}
	if bytes.Contains(got, []byte("NOT-AN-ICC-PROFILE")) {
		t.Error("a non-ICC APP2 survived; only ICC profiles are color data")
	}
	if !bytes.Contains(got, []byte("Adobe")) {
		t.Error("APP14/Adobe was dropped; image/jpeg needs it for CMYK/YCCK")
	}
}

// Orientation parsing reads attacker-controlled offsets. Every malformed
// shape must degrade to "no orientation" rather than panic or read wild.
func TestOrientationDegradesOnMalformedEXIF(t *testing.T) {
	base := baseJPEG(t, 24, 24)

	// A well-formed little-endian EXIF, to prove both byte orders are read.
	le := []byte("Exif\x00\x00")
	le = append(le, 'I', 'I', 42, 0)
	le = append(le, 8, 0, 0, 0)
	le = append(le, 1, 0) // one entry
	le = append(le, 0x12, 0x01, 3, 0, 1, 0, 0, 0, 7, 0, 0, 0)
	le = append(le, 0, 0, 0, 0)

	cases := map[string]struct {
		payload []byte
		want    int
	}{
		"little-endian is read":  {le, 7},
		"too short for a header": {[]byte("Exif\x00\x00MM"), 1},
		"unknown byte order":     {append([]byte("Exif\x00\x00"), 'X', 'Y', 0, 42, 0, 0, 0, 8), 1},
		"bad TIFF magic":         {append([]byte("Exif\x00\x00"), 'M', 'M', 0, 99, 0, 0, 0, 8), 1},
		"IFD offset past end":    {append([]byte("Exif\x00\x00"), 'M', 'M', 0, 42, 0xFF, 0xFF, 0xFF, 0xF0), 1},
		"entry count overruns":   {append([]byte("Exif\x00\x00"), 'M', 'M', 0, 42, 0, 0, 0, 8, 0xFF, 0xFF), 1},
	}

	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("panicked: %v", r)
				}
			}()
			src := inject(base, seg(mAPP1, c.payload))
			if got := jpegmeta.Orientation(src); got != c.want {
				t.Errorf("Orientation = %d, want %d", got, c.want)
			}
			// Whatever the EXIF said, the strip must still produce a valid file.
			out, err := jpegmeta.Strip(src)
			if err != nil {
				t.Fatalf("Strip: %v", err)
			}
			if _, err := jpeg.Decode(bytes.NewReader(out)); err != nil {
				t.Fatalf("output does not decode: %v", err)
			}
		})
	}
}

// An out-of-range or wrongly-typed Orientation value is not trusted.
func TestOrientationRejectsOutOfRangeAndWrongType(t *testing.T) {
	base := baseJPEG(t, 24, 24)

	outOfRange := exifPayload(99, false) // valid structure, silly value
	wrongType := append([]byte{}, exifPayload(6, false)...)
	// Flip the entry's type from SHORT (3) to LONG (4).
	idx := bytes.Index(wrongType, []byte{0x01, 0x12, 0x00, 0x03})
	if idx < 0 {
		t.Fatal("could not locate the orientation entry in the fixture")
	}
	wrongType[idx+3] = 0x04

	for name, payload := range map[string][]byte{
		"value out of range": outOfRange,
		"wrong tag type":     wrongType,
	} {
		t.Run(name, func(t *testing.T) {
			src := inject(base, seg(mAPP1, payload))
			if got := jpegmeta.Orientation(src); got != 1 {
				t.Errorf("Orientation = %d, want 1 (identity) for %s", got, name)
			}
		})
	}
}

// Orientation on input that is not a JPEG at all must be the identity, not a
// crash — it is called on bytes that have not yet been validated.
func TestOrientationOnNonJPEGIsIdentity(t *testing.T) {
	for _, in := range [][]byte{nil, {}, []byte("GIF89a"), {0xFF, mSOI}} {
		if got := jpegmeta.Orientation(in); got != 1 {
			t.Errorf("Orientation(%v) = %d, want 1", in, got)
		}
	}
}

// Verify must reject a file whose scan data never terminates, which is what
// scanAll sees when entropy data runs to EOF with no closing marker.
func TestVerifyRejectsUnterminatedScanData(t *testing.T) {
	good, err := jpegmeta.Strip(baseJPEG(t, 40, 28))
	if err != nil {
		t.Fatal(err)
	}
	// Drop the trailing EOI so the entropy walk finds no terminating marker.
	if _, err := jpegmeta.Verify(good[:len(good)-2], 40, 28); err == nil {
		t.Error("Verify accepted a file whose scan data never terminates")
	}
}
