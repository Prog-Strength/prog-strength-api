package activity

import (
	"bytes"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"math/rand"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

// jpegFixture returns a small decodable JPEG.
func jpegFixture(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	rng := rand.New(rand.NewSource(3))
	for y := range h {
		for x := range w {
			img.Set(x, y, color.RGBA{uint8(rng.Intn(256)), uint8(x % 256), uint8(y % 256), 255})
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 90}); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func pngFixture(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			img.Set(x, y, color.RGBA{uint8(x % 256), uint8(y % 256), 128, 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func workerHandler(t *testing.T) (*Handler, *FakePhotoStore) {
	t.Helper()
	h := &Handler{}
	store := NewFakePhotoStore()
	h.photoStore = store
	h.photoCfg = PhotosConfig{
		MaxPerActivity: 10, MaxUploadBytes: 32 << 20,
		FullMaxEdgePx: 20000, FullJPEGQuality: 95,
		ThumbMaxEdgePx: 800, ThumbJPEGQuality: 85,
		ProcessMaxAttempts: 3, ProcessTickSeconds: 1,
	}
	return h, store
}

// The JPEG path must not re-encode. The stored bytes being byte-identical to
// what the client uploaded (modulo metadata) is the entire point of the
// design, so it is asserted directly rather than inferred from timing.
func TestWorkerStoresJPEGWithoutReEncoding(t *testing.T) {
	h, _ := workerHandler(t)
	src := jpegFixture(t, 64, 48)

	full, thumb, err := h.buildPhotoObjects(src, "image/jpeg", "p1")
	if err != nil {
		t.Fatalf("buildPhotoObjects: %v", err)
	}

	if full.width != 64 || full.height != 48 {
		t.Errorf("dimensions = %dx%d, want 64x48", full.width, full.height)
	}
	if len(thumb.Bytes) == 0 {
		t.Error("no thumbnail produced")
	}
	// A re-encode at q95 of this fixture would come out a different size; the
	// strip changes only the metadata, and the fixture carries none.
	if len(full.bytes) != len(src) {
		t.Errorf("stored %d bytes from a %d byte source — the JPEG path re-encoded",
			len(full.bytes), len(src))
	}
	if !bytes.Equal(full.bytes, src) {
		t.Error("stored bytes differ from the source; the JPEG path is not lossless")
	}
}

// PNG is a deliberate v1 scope decision: it keeps the existing re-encode.
func TestWorkerReEncodesPNGThroughTheOldPipeline(t *testing.T) {
	h, _ := workerHandler(t)
	src := pngFixture(t, 40, 30)

	full, _, err := h.buildPhotoObjects(src, "image/png", "p1")
	if err != nil {
		t.Fatalf("buildPhotoObjects: %v", err)
	}
	if full.contentType != "image/jpeg" {
		t.Errorf("content type = %q, want image/jpeg — PNG goes through processPhoto", full.contentType)
	}
	if bytes.Equal(full.bytes, src) {
		t.Error("PNG was stored unchanged; v1 should still re-encode it")
	}
	if _, err := jpeg.Decode(bytes.NewReader(full.bytes)); err != nil {
		t.Fatalf("re-encoded PNG does not decode as JPEG: %v", err)
	}
}

// The fallback is the mitigation that makes the strip's risk acceptable, so it
// is tested by INJECTING a failure rather than waiting for a real bug: a JPEG
// carrying metadata after its scan data is something Strip cannot see and
// Verify rejects, which is exactly the shape of a rewriter blind spot.
func TestWorkerFallsBackToReEncodeWhenVerifyRejects(t *testing.T) {
	h, _ := workerHandler(t)
	src := jpegFixture(t, 48, 36)

	// Splice a comment segment in just before the trailing EOI — after the
	// scan, where Strip copies bytes through verbatim.
	cut := len(src) - 2
	tampered := append([]byte{}, src[:cut]...)
	tampered = append(tampered, 0xFF, 0xFE, 0x00, 0x08, 'l', 'e', 'a', 'k', 'e', 'd')
	tampered = append(tampered, src[cut:]...)

	before := readCounter(t, photoStripFallbackTotal)
	full, thumb, err := h.buildPhotoObjects(tampered, "image/jpeg", "p1")
	if err != nil {
		t.Fatalf("buildPhotoObjects returned an error instead of falling back: %v", err)
	}
	after := readCounter(t, photoStripFallbackTotal)

	if after != before+1 {
		t.Errorf("fallback counter = %v, want %v — a silent fallback is indistinguishable from success",
			after, before+1)
	}
	if len(thumb.Bytes) == 0 {
		t.Error("fallback produced no thumbnail")
	}
	// The re-encode drops everything unconditionally, so the smuggled comment
	// must not survive into what gets stored.
	if bytes.Contains(full.bytes, []byte("leaked")) {
		t.Error("metadata hidden after the scan survived into the stored object")
	}
	if _, err := jpeg.Decode(bytes.NewReader(full.bytes)); err != nil {
		t.Fatalf("fallback output does not decode: %v", err)
	}
}

// Bytes that are not a usable image are terminal — retrying cannot fix them,
// so they must not occupy the worker until the attempt cap runs out.
func TestWorkerTreatsUndecodableInputAsTerminal(t *testing.T) {
	h, _ := workerHandler(t)

	// A JPEG header the decoder will reject once it reads past it.
	_, _, err := h.buildPhotoObjects([]byte{0xFF, 0xD8, 0xFF, 0xDB, 0x00, 0x03, 0x00}, "image/jpeg", "p1")
	if err == nil {
		t.Fatal("accepted undecodable bytes")
	}
	if !isTerminal(err) {
		t.Errorf("err = %v, want a terminal failure so the row is retired immediately", err)
	}
}

// readCounter pulls a Prometheus counter's current value so a test can assert
// the fallback actually fired rather than trusting the code path.
func readCounter(t *testing.T, c prometheus.Counter) float64 {
	t.Helper()
	return testutil.ToFloat64(c)
}

func isTerminal(err error) bool { return errors.Is(err, errTerminal) }
