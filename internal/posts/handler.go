package posts

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

const (
	maxPostUploadBytes = 15 << 20 // cover only, same cap as photos/albums
	excerptLen         = 200
)

type Handler struct {
	repo    *Repo
	storage *storage.Client
}

func NewHandler(repo *Repo, st *storage.Client) *Handler {
	return &Handler{repo: repo, storage: st}
}

// Register mounts the post routes. adminWrap is the auth middleware; public
// GETs skip it entirely.
func (h *Handler) Register(mux *http.ServeMux, adminWrap func(http.Handler) http.Handler) {
	mux.HandleFunc("GET /api/posts", h.listPublic)
	mux.HandleFunc("GET /api/posts/{slug}", h.getBySlug)
	mux.Handle("GET /api/admin/posts", adminWrap(http.HandlerFunc(h.listAdmin)))
	mux.Handle("GET /api/admin/posts/{id}", adminWrap(http.HandlerFunc(h.get)))
	mux.Handle("POST /api/admin/posts", adminWrap(http.HandlerFunc(h.create)))
	mux.Handle("PUT /api/admin/posts/{id}", adminWrap(http.HandlerFunc(h.update)))
	mux.Handle("PATCH /api/admin/posts/{id}/publish", adminWrap(http.HandlerFunc(h.publish)))
	mux.Handle("PATCH /api/admin/posts/{id}/unpublish", adminWrap(http.HandlerFunc(h.unpublish)))
	mux.Handle("DELETE /api/admin/posts/{id}", adminWrap(http.HandlerFunc(h.delete)))
}

func (h *Handler) withCoverURL(p *Post) *Post {
	if p.CoverImagePath != nil {
		url := h.storage.PublicURL(Bucket, *p.CoverImagePath)
		p.CoverURL = &url
	}
	return p
}

// postSummary is the thin shape the public list returns — no
// contentMarkdown, so a card grid doesn't ship every post body over the
// wire.
type postSummary struct {
	Title       string     `json:"title"`
	Slug        string     `json:"slug"`
	Excerpt     string     `json:"excerpt"`
	CoverURL    *string    `json:"coverUrl"`
	PublishedAt *time.Time `json:"publishedAt"`
}

func (h *Handler) listPublic(w http.ResponseWriter, r *http.Request) {
	posts, err := h.repo.ListPublished(r.Context())
	if err != nil {
		httpserver.Internal(w, err)
		return
	}
	summaries := make([]postSummary, len(posts))
	for i, p := range posts {
		h.withCoverURL(p)
		excerpt := p.Excerpt
		if excerpt == "" {
			excerpt = deriveExcerpt(p.ContentMarkdown, excerptLen)
		}
		summaries[i] = postSummary{
			Title:       p.Title,
			Slug:        p.Slug,
			Excerpt:     excerpt,
			CoverURL:    p.CoverURL,
			PublishedAt: p.PublishedAt,
		}
	}
	httpserver.JSON(w, http.StatusOK, summaries)
}

func (h *Handler) getBySlug(w http.ResponseWriter, r *http.Request) {
	p, err := h.repo.GetPublishedBySlug(r.Context(), r.PathValue("slug"))
	if errors.Is(err, pgx.ErrNoRows) {
		httpserver.Err(w, http.StatusNotFound, "not_found", "post not found")
		return
	}
	if err != nil {
		httpserver.Internal(w, err)
		return
	}
	httpserver.JSON(w, http.StatusOK, h.withCoverURL(p))
}

func (h *Handler) listAdmin(w http.ResponseWriter, r *http.Request) {
	posts, err := h.repo.ListAll(r.Context())
	if err != nil {
		httpserver.Internal(w, err)
		return
	}
	for _, p := range posts {
		h.withCoverURL(p)
	}
	httpserver.JSON(w, http.StatusOK, posts)
}

func (h *Handler) get(w http.ResponseWriter, r *http.Request) {
	p, err := h.repo.Get(r.Context(), r.PathValue("id"))
	if errors.Is(err, pgx.ErrNoRows) {
		httpserver.Err(w, http.StatusNotFound, "not_found", "post not found")
		return
	}
	if err != nil {
		httpserver.Internal(w, err)
		return
	}
	httpserver.JSON(w, http.StatusOK, h.withCoverURL(p))
}

func (h *Handler) create(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxPostUploadBytes)
	if err := r.ParseMultipartForm(4 << 20); err != nil {
		httpserver.Err(w, http.StatusRequestEntityTooLarge, "too_large", "upload exceeds size limit")
		return
	}

	ctx := r.Context()
	var coverPath *string
	if coverFile, _, err := r.FormFile("cover"); err == nil {
		defer coverFile.Close()
		raw, err := io.ReadAll(coverFile)
		if err != nil {
			httpserver.Internal(w, err)
			return
		}
		proc, err := imageproc.Process(raw)
		if err != nil {
			httpserver.Err(w, http.StatusUnprocessableEntity, "invalid_image", "could not process cover image")
			return
		}
		cp := fmt.Sprintf("covers/%s.jpg", uuid.NewString())
		if err := h.storage.Upload(ctx, Bucket, cp, "image/jpeg", bytes.NewReader(proc.Thumbnail)); err != nil {
			httpserver.Internal(w, err)
			return
		}
		coverPath = &cp
	}

	post := &Post{
		Title:           r.FormValue("title"),
		Excerpt:         r.FormValue("excerpt"),
		ContentMarkdown: r.FormValue("contentMarkdown"),
		CoverImagePath:  coverPath,
	}
	created, err := h.repo.Insert(ctx, post)
	if err != nil {
		if coverPath != nil {
			h.cleanupObjects(*coverPath)
		}
		httpserver.Internal(w, err)
		return
	}
	httpserver.JSON(w, http.StatusCreated, h.withCoverURL(created))
}

func (h *Handler) update(w http.ResponseWriter, r *http.Request) {
	var u PostUpdate
	if err := json.NewDecoder(r.Body).Decode(&u); err != nil {
		httpserver.Err(w, http.StatusBadRequest, "bad_request", "invalid JSON body")
		return
	}
	p, err := h.repo.Update(r.Context(), r.PathValue("id"), u)
	if errors.Is(err, pgx.ErrNoRows) {
		httpserver.Err(w, http.StatusNotFound, "not_found", "post not found")
		return
	}
	if err != nil {
		httpserver.Internal(w, err)
		return
	}
	httpserver.JSON(w, http.StatusOK, h.withCoverURL(p))
}

func (h *Handler) publish(w http.ResponseWriter, r *http.Request) {
	p, err := h.repo.Publish(r.Context(), r.PathValue("id"))
	if errors.Is(err, pgx.ErrNoRows) {
		httpserver.Err(w, http.StatusNotFound, "not_found", "post not found")
		return
	}
	if err != nil {
		httpserver.Internal(w, err)
		return
	}
	httpserver.JSON(w, http.StatusOK, h.withCoverURL(p))
}

func (h *Handler) unpublish(w http.ResponseWriter, r *http.Request) {
	p, err := h.repo.Unpublish(r.Context(), r.PathValue("id"))
	if errors.Is(err, pgx.ErrNoRows) {
		httpserver.Err(w, http.StatusNotFound, "not_found", "post not found")
		return
	}
	if err != nil {
		httpserver.Internal(w, err)
		return
	}
	httpserver.JSON(w, http.StatusOK, h.withCoverURL(p))
}

func (h *Handler) delete(w http.ResponseWriter, r *http.Request) {
	cover, err := h.repo.Delete(r.Context(), r.PathValue("id"))
	if errors.Is(err, pgx.ErrNoRows) {
		httpserver.Err(w, http.StatusNotFound, "not_found", "post not found")
		return
	}
	if err != nil {
		httpserver.Internal(w, err)
		return
	}
	if cover != nil {
		h.cleanupObjects(*cover)
	}
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
