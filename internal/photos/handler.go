package photos

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/alyralabs/digitaletude-api/internal/httpserver"
	"github.com/alyralabs/digitaletude-api/internal/imageproc"
	"github.com/alyralabs/digitaletude-api/internal/storage"
)

const maxUploadBytes = 15 << 20 // 15 MB request cap

type Handler struct {
	repo    *Repo
	storage *storage.Client
}

func NewHandler(repo *Repo, st *storage.Client) *Handler {
	return &Handler{repo: repo, storage: st}
}

// Register mounts the photo routes. adminWrap is the auth middleware; public
// GETs skip it entirely.
func (h *Handler) Register(mux *http.ServeMux, adminWrap func(http.Handler) http.Handler) {
	mux.HandleFunc("GET /api/photos", h.list)
	mux.HandleFunc("GET /api/photos/{id}", h.get)
	mux.Handle("POST /api/admin/photos", adminWrap(http.HandlerFunc(h.create)))
	mux.Handle("PATCH /api/admin/photos/{id}", adminWrap(http.HandlerFunc(h.update)))
	mux.Handle("DELETE /api/admin/photos/{id}", adminWrap(http.HandlerFunc(h.delete)))
}

func (h *Handler) withURLs(p *Photo) *Photo {
	p.OriginalURL = h.storage.PublicURL(Bucket, p.StoragePath)
	p.ThumbnailURL = h.storage.PublicURL(Bucket, p.ThumbnailPath)
	return p
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	photos, err := h.repo.List(r.Context())
	if err != nil {
		httpserver.Internal(w, err)
		return
	}
	for _, p := range photos {
		h.withURLs(p)
	}
	httpserver.JSON(w, http.StatusOK, photos)
}

func (h *Handler) get(w http.ResponseWriter, r *http.Request) {
	p, err := h.repo.Get(r.Context(), r.PathValue("id"))
	if errors.Is(err, pgx.ErrNoRows) {
		httpserver.Err(w, http.StatusNotFound, "not_found", "photo not found")
		return
	}
	if err != nil {
		httpserver.Internal(w, err)
		return
	}
	httpserver.JSON(w, http.StatusOK, h.withURLs(p))
}

func (h *Handler) create(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxUploadBytes)
	if err := r.ParseMultipartForm(4 << 20); err != nil {
		httpserver.Err(w, http.StatusRequestEntityTooLarge, "too_large", "upload exceeds 15 MB")
		return
	}

	file, _, err := r.FormFile("file")
	if err != nil {
		httpserver.Err(w, http.StatusBadRequest, "bad_request", "missing file field")
		return
	}
	defer file.Close()

	raw, err := io.ReadAll(file)
	if err != nil {
		httpserver.Internal(w, err)
		return
	}

	proc, err := imageproc.Process(raw)
	if errors.Is(err, imageproc.ErrUnsupportedType) {
		httpserver.Err(w, http.StatusUnsupportedMediaType, "unsupported_type", "only JPEG and PNG are accepted")
		return
	}
	if err != nil {
		httpserver.Err(w, http.StatusUnprocessableEntity, "invalid_image", "could not process image")
		slog.Warn("image processing failed", "error", err)
		return
	}

	id := uuid.NewString()
	origPath := fmt.Sprintf("originals/%s.%s", id, proc.Ext)
	thumbPath := fmt.Sprintf("thumbnails/%s.jpg", id)

	ctx := r.Context()
	if err := h.storage.Upload(ctx, Bucket, origPath, proc.MIME, bytes.NewReader(raw)); err != nil {
		httpserver.Internal(w, err)
		return
	}
	if err := h.storage.Upload(ctx, Bucket, thumbPath, "image/jpeg", bytes.NewReader(proc.Thumbnail)); err != nil {
		h.cleanupObjects(origPath)
		httpserver.Internal(w, err)
		return
	}

	photo := &Photo{
		Title:         r.FormValue("title"),
		Description:   r.FormValue("description"),
		StoragePath:   origPath,
		ThumbnailPath: thumbPath,
		Width:         proc.Width,
		Height:        proc.Height,
	}
	created, err := h.repo.Insert(ctx, photo)
	if err != nil {
		h.cleanupObjects(origPath, thumbPath)
		httpserver.Internal(w, err)
		return
	}
	httpserver.JSON(w, http.StatusCreated, h.withURLs(created))
}

func (h *Handler) update(w http.ResponseWriter, r *http.Request) {
	var u PhotoUpdate
	if err := json.NewDecoder(r.Body).Decode(&u); err != nil {
		httpserver.Err(w, http.StatusBadRequest, "bad_request", "invalid JSON body")
		return
	}
	p, err := h.repo.Update(r.Context(), r.PathValue("id"), u)
	if errors.Is(err, pgx.ErrNoRows) {
		httpserver.Err(w, http.StatusNotFound, "not_found", "photo not found")
		return
	}
	if err != nil {
		httpserver.Internal(w, err)
		return
	}
	httpserver.JSON(w, http.StatusOK, h.withURLs(p))
}

func (h *Handler) delete(w http.ResponseWriter, r *http.Request) {
	origPath, thumbPath, err := h.repo.Delete(r.Context(), r.PathValue("id"))
	if errors.Is(err, pgx.ErrNoRows) {
		httpserver.Err(w, http.StatusNotFound, "not_found", "photo not found")
		return
	}
	if err != nil {
		httpserver.Internal(w, err)
		return
	}
	h.cleanupObjects(origPath, thumbPath)
	httpserver.JSON(w, http.StatusNoContent, nil)
}

// cleanupObjects is best-effort: failures leave orphaned (invisible) files,
// which is the acceptable failure mode. Detached from the request context so
// a cancelled request doesn't abandon the cleanup mid-flight.
func (h *Handler) cleanupObjects(paths ...string) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	for _, p := range paths {
		if err := h.storage.Delete(ctx, Bucket, p); err != nil {
			slog.Warn("storage cleanup failed", "bucket", Bucket, "path", p, "error", err)
		}
	}
}
