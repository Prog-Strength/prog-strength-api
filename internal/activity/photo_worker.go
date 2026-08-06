package activity

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/Prog-Strength/prog-strength-api/internal/jpegmeta"
)

// The photo processing worker. Everything that used to happen inside
// POST /activities/{id}/photos now happens here instead, on a goroutine with
// no request deadline over it.
//
// See prog-strength-docs/sows/photo-upload-direct-to-s3.md.

// photoStripFallbackTotal counts photos whose metadata rewrite failed
// verification and fell back to the re-encode.
//
// This is a REQUIREMENT of the design, not instrumentation garnish. The
// fallback means a broken rewriter degrades silently to today's behavior —
// correct output, worse fidelity — which is indistinguishable from success
// unless something counts it. A non-zero value here is the signal to fix the
// rewriter, and the gate on extending the strip to PNG/WebP.
var photoStripFallbackTotal = prometheus.NewCounter(prometheus.CounterOpts{
	Name: "api_photo_strip_fallback_total",
	Help: "Photos whose lossless JPEG metadata strip failed verification and fell back to a full re-encode.",
})

// photoProcessFailedTotal counts photos the worker gave up on entirely.
var photoProcessFailedTotal = prometheus.NewCounter(prometheus.CounterOpts{
	Name: "api_photo_process_failed_total",
	Help: "Photos the processing worker abandoned after exhausting its attempts or hitting a terminal error.",
})

func init() {
	prometheus.MustRegister(photoStripFallbackTotal, photoProcessFailedTotal)
}

// errTerminal marks a failure that retrying cannot fix — the bytes are not a
// usable image of an allowed type. Distinguished from transient failures (S3,
// disk) so a poison upload is retired immediately rather than occupying the
// worker until its attempt cap runs out.
var errTerminal = errors.New("activity: terminal photo processing failure")

// RunPhotoWorker processes committed photo uploads until ctx is cancelled.
// One goroutine, one photo at a time: the pipeline is CPU-bound on two
// burstable vCPUs shared with five other services, so a pool would contend
// with the API rather than add throughput.
func (h *Handler) RunPhotoWorker(ctx context.Context) {
	if !h.photoStorageReady() {
		return
	}
	tick := time.Duration(h.photoCfg.ProcessTickSeconds) * time.Second
	if tick <= 0 {
		tick = 2 * time.Second
	}
	t := time.NewTicker(tick)
	defer t.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			// Drain rather than one-per-tick: a burst of uploads should not
			// take one tick each to clear. The inner loop exits as soon as
			// there is nothing claimable.
			for {
				worked, err := h.processOnePhoto(ctx)
				if err != nil {
					log.Printf("activity photo worker: %v", err)
					break
				}
				if !worked || ctx.Err() != nil {
					break
				}
			}
		}
	}
}

// processOnePhoto claims and processes a single photo. It returns worked=false
// when there was nothing to claim.
func (h *Handler) processOnePhoto(ctx context.Context) (bool, error) {
	p, ok, err := h.photoRepo.ClaimNextForProcessing(ctx, h.photoCfg.ProcessMaxAttempts)
	if err != nil {
		return false, fmt.Errorf("claim: %w", err)
	}
	if !ok {
		return false, nil
	}
	if p.UploadS3Key == nil {
		// Committed without a staged key — cannot happen through the handlers,
		// so treat it as terminal rather than retrying forever.
		h.failPhoto(ctx, p.ID, "no staged upload key")
		return true, nil
	}

	if err := h.renderPhoto(ctx, p); err != nil {
		if errors.Is(err, errTerminal) {
			h.failPhoto(ctx, p.ID, err.Error())
			return true, nil
		}
		// Transient. Leave the row in 'processing' so a later tick retries it,
		// until the attempt cap (incremented at claim time) retires it.
		if recErr := h.photoRepo.RecordAttemptError(ctx, p.ID, err.Error()); recErr != nil {
			log.Printf("activity photo worker: record attempt error: photo_id=%s err=%v", p.ID, recErr)
		}
		if p.Attempts >= h.photoCfg.ProcessMaxAttempts {
			h.failPhoto(ctx, p.ID, "attempts exhausted: "+err.Error())
		}
		return true, nil
	}
	return true, nil
}

// renderPhoto is the pipeline proper: fetch, validate, produce the full-size
// object, thumbnail, store both, publish the row, delete the staged original.
func (h *Handler) renderPhoto(ctx context.Context, p ActivityPhoto) error {
	src, err := h.photoStore.Get(ctx, *p.UploadS3Key)
	if err != nil {
		if errors.Is(err, ErrObjectNotFound) {
			return fmt.Errorf("%w: staged object is gone", errTerminal)
		}
		return fmt.Errorf("get staged object: %w", err)
	}

	// Validation does NOT move off the worker. A presigned PUT accepts
	// whatever the client sent, so everything uploadPhoto checked has to
	// happen here: sniff the real bytes, and bound the declared dimensions
	// before decoding a pixel. This is the decompression-bomb guard.
	contentType := http.DetectContentType(src)
	if !allowedPhotoContentTypes[contentType] {
		return fmt.Errorf("%w: sniffed content type %q", errTerminal, contentType)
	}

	full, thumb, err := h.buildPhotoObjects(src, contentType, p.ID)
	if err != nil {
		return err
	}

	a, err := h.repo.Get(ctx, p.UserID, p.ActivityID)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return fmt.Errorf("%w: parent activity is gone", errTerminal)
		}
		return fmt.Errorf("get parent activity: %w", err)
	}
	fullKey, err := buildPhotoKey(p.UserID, a.ActivityType, a.StartTime, p.ActivityID, p.ID, photoVariantFull)
	if err != nil {
		return fmt.Errorf("build full key: %w", errors.Join(errTerminal, err))
	}
	thumbKey, err := buildPhotoKey(p.UserID, a.ActivityType, a.StartTime, p.ActivityID, p.ID, photoVariantThumb)
	if err != nil {
		return fmt.Errorf("build thumb key: %w", errors.Join(errTerminal, err))
	}

	if err := h.photoStore.Put(ctx, fullKey, full.contentType, full.bytes); err != nil {
		return fmt.Errorf("put full photo: %w", err)
	}
	if err := h.photoStore.Put(ctx, thumbKey, "image/jpeg", thumb.Bytes); err != nil {
		// The full object is orphaned — tag it so the lifecycle rule reaps it
		// and leave the row in processing for a retry.
		if tagErr := h.photoStore.TagOrphaned(ctx, fullKey); tagErr != nil {
			log.Printf("activity photo worker: tag orphaned full after thumb failure: key=%s err=%v", fullKey, tagErr)
		}
		return fmt.Errorf("put thumb photo: %w", err)
	}

	if err := h.photoRepo.MarkReady(ctx, p.ID, fullKey, thumbKey,
		int64(len(full.bytes)), full.width, full.height); err != nil {
		return fmt.Errorf("mark ready: %w", err)
	}

	// Only now delete the staged original. Doing it earlier would destroy the
	// one copy that makes a retry possible; doing it later leaves the only
	// GPS-bearing object alive longer than it needs to be.
	if err := h.photoStore.Delete(ctx, *p.UploadS3Key); err != nil {
		log.Printf("activity photo worker: delete staged object: key=%s err=%v", *p.UploadS3Key, err)
	}
	return nil
}

// storedPhoto is the full-size object the worker is about to write.
type storedPhoto struct {
	bytes         []byte
	contentType   string
	width, height int
}

// buildPhotoObjects produces the full-size object and the thumbnail, branching
// on format.
//
// JPEG takes the lossless path: rewrite the container, verify the result, and
// fall back to the re-encode if that verification fails. PNG and WebP go
// through processPhoto unchanged — a deliberate v1 scope decision, not an
// oversight; see the SOW's "Scope: JPEG first".
func (h *Handler) buildPhotoObjects(src []byte, contentType, photoID string) (storedPhoto, processedImage, error) {
	opts := photoPipelineOpts{
		FullMaxEdge:  h.photoCfg.FullMaxEdgePx,
		FullQuality:  h.photoCfg.FullJPEGQuality,
		ThumbMaxEdge: h.photoCfg.ThumbMaxEdgePx,
		ThumbQuality: h.photoCfg.ThumbJPEGQuality,
	}

	if contentType == "image/jpeg" {
		full, thumb, err := h.stripJPEG(src, opts, photoID)
		if err == nil {
			return full, thumb, nil
		}
		if errors.Is(err, errTerminal) {
			return storedPhoto{}, processedImage{}, err
		}
		// Verification failed. Fall through to the re-encode rather than
		// failing the photo: the user gets their photo at today's quality, and
		// the counter tells the operator the rewriter met a case it cannot
		// handle.
		photoStripFallbackTotal.Inc()
		log.Printf("activity photo worker: strip verification failed, falling back to re-encode: photo_id=%s err=%v", photoID, err)
	}

	full, thumb, err := processPhoto(src, opts)
	if err != nil {
		return storedPhoto{}, processedImage{}, errors.Join(errTerminal, err)
	}
	return storedPhoto{
		bytes:       full.Bytes,
		contentType: "image/jpeg",
		width:       full.Width,
		height:      full.Height,
	}, thumb, nil
}

// stripJPEG runs the lossless path. A malformed source is terminal; a strip
// that produces something Verify rejects is NOT — that returns a plain error
// so the caller falls back.
func (h *Handler) stripJPEG(src []byte, opts photoPipelineOpts, photoID string) (storedPhoto, processedImage, error) {
	cfg, err := decodeJPEGConfig(src)
	if err != nil {
		return storedPhoto{}, processedImage{}, errors.Join(errTerminal, err)
	}
	if boundErr := boundDimensions(cfg.Width, cfg.Height); boundErr != nil {
		return storedPhoto{}, processedImage{}, errors.Join(errTerminal, boundErr)
	}

	stripped, err := jpegmeta.Strip(src)
	if err != nil {
		// A source this package refuses to rewrite is not necessarily
		// unusable — image/jpeg may still decode it — so let the fallback try.
		return storedPhoto{}, processedImage{}, fmt.Errorf("strip: %w", err)
	}

	// Verify hands back the decoded image so the thumbnail does not have to
	// decode a second time. On a burstable vCPU that saves more than the
	// re-encode this design removed.
	img, err := jpegmeta.Verify(stripped, cfg.Width, cfg.Height)
	if err != nil {
		return storedPhoto{}, processedImage{}, fmt.Errorf("verify: %w", err)
	}

	thumb, err := renderVariant(img, opts.ThumbMaxEdge, opts.ThumbQuality)
	if err != nil {
		return storedPhoto{}, processedImage{}, fmt.Errorf("render thumb: %w", err)
	}
	return storedPhoto{
		bytes:       stripped,
		contentType: "image/jpeg",
		width:       cfg.Width,
		height:      cfg.Height,
	}, thumb, nil
}

// failPhoto retires a row and counts it.
func (h *Handler) failPhoto(ctx context.Context, photoID, reason string) {
	photoProcessFailedTotal.Inc()
	if err := h.photoRepo.MarkFailed(ctx, photoID, reason); err != nil {
		log.Printf("activity photo worker: mark failed: photo_id=%s err=%v", photoID, err)
	}
}

// ReapStalePendingPhotos retires reservations whose presigned PUT expired
// unused and deletes any object staged under them. Mirrors
// ReapStalePendingVideos; run once at startup, which is enough at single-user
// volume.
func (h *Handler) ReapStalePendingPhotos(ctx context.Context) (int, error) {
	if !h.photoStorageReady() {
		return 0, nil
	}
	cutoff := h.now().UTC().Add(-time.Duration(h.photoCfg.ReapAfterMinutes) * time.Minute)
	stale, err := h.photoRepo.ExpiredPending(ctx, cutoff)
	if err != nil {
		return 0, err
	}
	for _, p := range stale {
		// Delete, not tag: a staged original carries the source's GPS, and an
		// abandoned one has no reason to outlive its reservation.
		if p.UploadS3Key != nil {
			if delErr := h.photoStore.Delete(ctx, *p.UploadS3Key); delErr != nil {
				log.Printf("activity photo: reaper delete failed: key=%s err=%v", *p.UploadS3Key, delErr)
			}
		}
		if delErr := h.photoRepo.SoftDeleteByID(ctx, p.ID); delErr != nil {
			return 0, delErr
		}
	}
	return len(stale), nil
}
