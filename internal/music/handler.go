package music

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/dhowden/tag"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/tcolgate/mp3"

	"github.com/alyralabs/digitaletude-api/internal/httpserver"
	"github.com/alyralabs/digitaletude-api/internal/imageproc"
	"github.com/alyralabs/digitaletude-api/internal/storage"
)

const (
	maxTrackUploadBytes = 550 << 20 // 500 MB audio (any format, pre-transcode) + a 50 MB cover in one multipart request
	maxAlbumUploadBytes = 50 << 20  // cover only, same cap as photos

	// Cover art never renders larger than the album image on the music page
	// (160 CSS px, 320 device px at 2x) — a 320 px thumbnail serves every
	// display, at roughly a tenth the bytes of the 800 px photo default.
	coverThumbWidth = 320
)

type Handler struct {
	repo    *Repo
	storage *storage.Client
}

func NewHandler(repo *Repo, st *storage.Client) *Handler {
	return &Handler{repo: repo, storage: st}
}

// RegisterPublic mounts the read-only music route.
func (h *Handler) RegisterPublic(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/music", h.listMusic)
}

// RegisterAdmin mounts the write music routes. Only ever called by the
// local-only admin server — never reachable from the deployed public API.
func (h *Handler) RegisterAdmin(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/admin/albums", h.createAlbum)
	mux.HandleFunc("PATCH /api/admin/albums/{id}", h.updateAlbum)
	mux.HandleFunc("PATCH /api/admin/albums/{id}/cover", h.updateAlbumCover)
	mux.HandleFunc("DELETE /api/admin/albums/{id}", h.deleteAlbum)
	mux.HandleFunc("POST /api/admin/tracks", h.createTrack)
	mux.HandleFunc("PATCH /api/admin/tracks/{id}", h.updateTrack)
	mux.HandleFunc("PATCH /api/admin/tracks/{id}/cover", h.updateTrackCover)
	mux.HandleFunc("DELETE /api/admin/tracks/{id}", h.deleteTrack)
}

func (h *Handler) withAlbumCoverURL(a *Album) *Album {
	if a.CoverImagePath != nil {
		url := h.storage.PublicURL(Bucket, *a.CoverImagePath)
		a.CoverURL = &url
	}
	return a
}

func (h *Handler) withTrackURLs(t *Track) *Track {
	t.AudioURL = h.storage.PublicURL(Bucket, t.StoragePath)
	if t.CoverImagePath != nil {
		url := h.storage.PublicURL(Bucket, *t.CoverImagePath)
		t.CoverURL = &url
	}
	return t
}

// buildMusicPayload assembles the one payload the public page needs: albums
// (each with its tracks in track_number order) and singles (album_id = null,
// ordered by sort_order/created_at). ListTracks already returns rows ordered
// by (album_id, track_number, sort_order, created_at desc), which puts each
// album's tracks in order and nulls (singles) last in the right order too.
func (h *Handler) buildMusicPayload(ctx context.Context) (*MusicPayload, error) {
	albums, err := h.repo.ListAlbums(ctx)
	if err != nil {
		return nil, err
	}
	tracks, err := h.repo.ListTracks(ctx)
	if err != nil {
		return nil, err
	}

	albumByID := make(map[string]*Album, len(albums))
	for _, a := range albums {
		h.withAlbumCoverURL(a)
		albumByID[a.ID] = a
	}

	singles := []*Track{}
	for _, t := range tracks {
		h.withTrackURLs(t)
		if t.AlbumID != nil {
			if a, ok := albumByID[*t.AlbumID]; ok {
				a.Tracks = append(a.Tracks, t)
				continue
			}
		}
		singles = append(singles, t)
	}

	return &MusicPayload{Albums: albums, Singles: singles}, nil
}

type MusicPayload struct {
	Albums  []*Album `json:"albums"`
	Singles []*Track `json:"singles"`
}

func (h *Handler) listMusic(w http.ResponseWriter, r *http.Request) {
	payload, err := h.buildMusicPayload(r.Context())
	if err != nil {
		httpserver.Internal(w, err)
		return
	}
	httpserver.JSON(w, http.StatusOK, payload)
}

// isMP3 checks the ID3v2 header ("ID3") or an MPEG frame sync (0xFF followed
// by a byte with its top 3 bits set) — never the client-supplied Content-Type.
func isMP3(head []byte) bool {
	if len(head) >= 3 && string(head[:3]) == "ID3" {
		return true
	}
	return len(head) >= 2 && head[0] == 0xFF && head[1]&0xE0 == 0xE0
}

// transcodeToMP3 shells out to ffmpeg to re-encode any audio input as
// 192 kbps MP3, returning an open handle on a fresh temp file the caller
// must close and remove. Source tags carry into the output's ID3 header;
// embedded cover-art streams are dropped (-vn) — covers travel separately
// in this API. A missing ffmpeg binary surfaces as exec.ErrNotFound;
// any other failure means ffmpeg couldn't decode the input.
func transcodeToMP3(ctx context.Context, srcPath string) (*os.File, error) {
	out, err := os.CreateTemp("", "track-transcoded-*.mp3")
	if err != nil {
		return nil, err
	}
	out.Close()

	cmd := exec.CommandContext(ctx, "ffmpeg",
		"-y", "-i", srcPath,
		"-vn",
		"-c:a", "libmp3lame", "-b:a", "192k",
		"-f", "mp3",
		out.Name(),
	)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		os.Remove(out.Name())
		lines := strings.Split(strings.TrimSpace(stderr.String()), "\n")
		return nil, fmt.Errorf("ffmpeg: %w: %s", err, lines[len(lines)-1])
	}

	f, err := os.Open(out.Name())
	if err != nil {
		os.Remove(out.Name())
		return nil, err
	}
	return f, nil
}

// trackDuration walks MPEG frames summing their durations (correct under VBR,
// unlike bitrate arithmetic). Any parse failure returns whatever was
// accumulated so far, or nil if nothing decoded — duration is never allowed
// to fail the upload.
func trackDuration(f *os.File) *int {
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return nil
	}
	dec := mp3.NewDecoder(f)
	var total time.Duration
	var frame mp3.Frame
	var skipped int
	for {
		if err := dec.Decode(&frame, &skipped); err != nil {
			break
		}
		total += frame.Duration()
	}
	if total <= 0 {
		return nil
	}
	secs := int(total.Seconds())
	return &secs
}

func (h *Handler) createTrack(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxTrackUploadBytes)
	if err := r.ParseMultipartForm(4 << 20); err != nil {
		httpserver.Err(w, http.StatusRequestEntityTooLarge, "too_large", "upload exceeds size limit")
		return
	}

	file, _, err := r.FormFile("file")
	if err != nil {
		httpserver.Err(w, http.StatusBadRequest, "bad_request", "missing file field")
		return
	}
	defer file.Close()

	// Spool to disk: Supabase's upload endpoint needs a known Content-Length,
	// and the file has to be read twice (tags/duration, then upload) —
	// spooling keeps memory flat and lets us seek.
	tmp, err := os.CreateTemp("", "track-upload-*")
	if err != nil {
		httpserver.Internal(w, err)
		return
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()

	if _, err := io.Copy(tmp, file); err != nil {
		httpserver.Internal(w, err)
		return
	}

	// audio is what actually gets stored: the upload itself when it's
	// already MP3 (re-encoding MP3 to MP3 is pure generation loss),
	// otherwise a 192 kbps MP3 transcode of it.
	audio := tmp
	head := make([]byte, 4)
	n, _ := tmp.ReadAt(head, 0)
	if !isMP3(head[:n]) {
		transcoded, err := transcodeToMP3(r.Context(), tmp.Name())
		if errors.Is(err, exec.ErrNotFound) {
			httpserver.Internal(w, err)
			return
		}
		if err != nil {
			httpserver.Err(w, http.StatusUnsupportedMediaType, "unsupported_type", "could not decode audio — upload MP3, M4A, WAV, FLAC, or similar")
			return
		}
		defer os.Remove(transcoded.Name())
		defer transcoded.Close()
		audio = transcoded
	}

	// Tags are read from the original upload, not the transcode — dhowden/tag
	// parses MP4/FLAC/OGG containers natively, so the source is the richer
	// (and always available) side.
	title := r.FormValue("title")
	if title == "" {
		if _, err := tmp.Seek(0, io.SeekStart); err == nil {
			if m, err := tag.ReadFrom(tmp); err == nil {
				title = m.Title()
			}
		}
	}

	duration := trackDuration(audio)

	var albumID *string
	if v := r.FormValue("album_id"); v != "" {
		albumID = &v
	}
	var trackNumber *int
	if v := r.FormValue("track_number"); v != "" {
		if tn, err := strconv.Atoi(v); err == nil {
			trackNumber = &tn
		}
	}

	ctx := r.Context()
	id := uuid.NewString()
	audioPath := fmt.Sprintf("tracks/%s.mp3", id)

	if _, err := audio.Seek(0, io.SeekStart); err != nil {
		httpserver.Internal(w, err)
		return
	}
	if err := h.storage.Upload(ctx, Bucket, audioPath, "audio/mpeg", audio); err != nil {
		httpserver.Internal(w, err)
		return
	}

	var coverPath *string
	if coverFile, _, err := r.FormFile("cover"); err == nil {
		defer coverFile.Close()
		raw, err := io.ReadAll(coverFile)
		if err != nil {
			h.cleanupObjects(audioPath)
			httpserver.Internal(w, err)
			return
		}
		proc, err := imageproc.ProcessAt(raw, coverThumbWidth)
		if err != nil {
			h.cleanupObjects(audioPath)
			httpserver.Err(w, http.StatusUnprocessableEntity, "invalid_image", "could not process cover image")
			return
		}
		cp := fmt.Sprintf("covers/%s.jpg", id)
		if err := h.storage.Upload(ctx, Bucket, cp, "image/jpeg", bytes.NewReader(proc.Thumbnail)); err != nil {
			h.cleanupObjects(audioPath)
			httpserver.Internal(w, err)
			return
		}
		coverPath = &cp
	}

	track := &Track{
		Title:           title,
		Description:     r.FormValue("description"),
		StoragePath:     audioPath,
		CoverImagePath:  coverPath,
		DurationSeconds: duration,
		AlbumID:         albumID,
		TrackNumber:     trackNumber,
	}
	created, err := h.repo.InsertTrack(ctx, track)
	if err != nil {
		paths := []string{audioPath}
		if coverPath != nil {
			paths = append(paths, *coverPath)
		}
		h.cleanupObjects(paths...)
		httpserver.Internal(w, err)
		return
	}
	httpserver.JSON(w, http.StatusCreated, h.withTrackURLs(created))
}

func (h *Handler) updateTrack(w http.ResponseWriter, r *http.Request) {
	var u TrackUpdate
	if err := json.NewDecoder(r.Body).Decode(&u); err != nil {
		httpserver.Err(w, http.StatusBadRequest, "bad_request", "invalid JSON body")
		return
	}
	t, err := h.repo.UpdateTrack(r.Context(), r.PathValue("id"), u)
	if errors.Is(err, pgx.ErrNoRows) {
		httpserver.Err(w, http.StatusNotFound, "not_found", "track not found")
		return
	}
	if err != nil {
		httpserver.Internal(w, err)
		return
	}
	httpserver.JSON(w, http.StatusOK, h.withTrackURLs(t))
}

// updateTrackCover replaces a track's cover art — add it where there wasn't
// one, or swap an existing one. Cover-only upload, same 50 MB cap as an
// album cover (the audio file itself is untouched).
// receiveCover is the shared front half of the two cover-replacement
// handlers: it validates the multipart "cover" upload, thumbnails it, and
// stores it under a fresh path. On failure it writes the error response
// itself and returns ok=false. The back half (row update + cleanup of
// old/new objects) stays in each handler — that's where album and track
// genuinely differ.
func (h *Handler) receiveCover(w http.ResponseWriter, r *http.Request) (coverPath string, ok bool) {
	r.Body = http.MaxBytesReader(w, r.Body, maxAlbumUploadBytes)
	if err := r.ParseMultipartForm(4 << 20); err != nil {
		httpserver.Err(w, http.StatusRequestEntityTooLarge, "too_large", "upload exceeds size limit")
		return "", false
	}

	coverFile, _, err := r.FormFile("cover")
	if err != nil {
		httpserver.Err(w, http.StatusBadRequest, "bad_request", "missing cover field")
		return "", false
	}
	defer coverFile.Close()

	raw, err := io.ReadAll(coverFile)
	if err != nil {
		httpserver.Internal(w, err)
		return "", false
	}
	proc, err := imageproc.ProcessAt(raw, coverThumbWidth)
	if errors.Is(err, imageproc.ErrUnsupportedType) {
		httpserver.Err(w, http.StatusUnsupportedMediaType, "unsupported_type", "only JPEG and PNG are accepted")
		return "", false
	}
	if err != nil {
		httpserver.Err(w, http.StatusUnprocessableEntity, "invalid_image", "could not process cover image")
		return "", false
	}

	cp := fmt.Sprintf("covers/%s.jpg", uuid.NewString())
	if err := h.storage.Upload(r.Context(), Bucket, cp, "image/jpeg", bytes.NewReader(proc.Thumbnail)); err != nil {
		httpserver.Internal(w, err)
		return "", false
	}
	return cp, true
}

func (h *Handler) updateTrackCover(w http.ResponseWriter, r *http.Request) {
	cp, ok := h.receiveCover(w, r)
	if !ok {
		return
	}

	updated, previous, err := h.repo.UpdateTrackCover(r.Context(), r.PathValue("id"), cp)
	if errors.Is(err, pgx.ErrNoRows) {
		h.cleanupObjects(cp)
		httpserver.Err(w, http.StatusNotFound, "not_found", "track not found")
		return
	}
	if err != nil {
		h.cleanupObjects(cp)
		httpserver.Internal(w, err)
		return
	}
	if previous != nil {
		h.cleanupObjects(*previous)
	}
	httpserver.JSON(w, http.StatusOK, h.withTrackURLs(updated))
}

func (h *Handler) deleteTrack(w http.ResponseWriter, r *http.Request) {
	audioPath, coverPath, err := h.repo.DeleteTrack(r.Context(), r.PathValue("id"))
	if errors.Is(err, pgx.ErrNoRows) {
		httpserver.Err(w, http.StatusNotFound, "not_found", "track not found")
		return
	}
	if err != nil {
		httpserver.Internal(w, err)
		return
	}
	paths := []string{audioPath}
	if coverPath != nil {
		paths = append(paths, *coverPath)
	}
	h.cleanupObjects(paths...)
	httpserver.JSON(w, http.StatusNoContent, nil)
}

func (h *Handler) createAlbum(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxAlbumUploadBytes)
	if err := r.ParseMultipartForm(4 << 20); err != nil {
		httpserver.Err(w, http.StatusRequestEntityTooLarge, "too_large", "upload exceeds size limit")
		return
	}

	var metadata json.RawMessage
	if v := r.FormValue("metadata"); v != "" {
		if !json.Valid([]byte(v)) {
			httpserver.Err(w, http.StatusBadRequest, "bad_request", "metadata must be valid JSON")
			return
		}
		metadata = json.RawMessage(v)
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
		proc, err := imageproc.ProcessAt(raw, coverThumbWidth)
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

	album := &Album{
		Title:          r.FormValue("title"),
		Description:    r.FormValue("description"),
		CoverImagePath: coverPath,
		Metadata:       metadata,
	}
	created, err := h.repo.InsertAlbum(ctx, album)
	if err != nil {
		if coverPath != nil {
			h.cleanupObjects(*coverPath)
		}
		httpserver.Internal(w, err)
		return
	}
	httpserver.JSON(w, http.StatusCreated, h.withAlbumCoverURL(created))
}

func (h *Handler) updateAlbum(w http.ResponseWriter, r *http.Request) {
	var u AlbumUpdate
	if err := json.NewDecoder(r.Body).Decode(&u); err != nil {
		httpserver.Err(w, http.StatusBadRequest, "bad_request", "invalid JSON body")
		return
	}
	a, err := h.repo.UpdateAlbum(r.Context(), r.PathValue("id"), u)
	if errors.Is(err, pgx.ErrNoRows) {
		httpserver.Err(w, http.StatusNotFound, "not_found", "album not found")
		return
	}
	if err != nil {
		httpserver.Internal(w, err)
		return
	}
	httpserver.JSON(w, http.StatusOK, h.withAlbumCoverURL(a))
}

// updateAlbumCover replaces an album's cover art — add it where there wasn't
// one, or swap an existing one.
func (h *Handler) updateAlbumCover(w http.ResponseWriter, r *http.Request) {
	cp, ok := h.receiveCover(w, r)
	if !ok {
		return
	}

	updated, previous, err := h.repo.UpdateAlbumCover(r.Context(), r.PathValue("id"), cp)
	if errors.Is(err, pgx.ErrNoRows) {
		h.cleanupObjects(cp)
		httpserver.Err(w, http.StatusNotFound, "not_found", "album not found")
		return
	}
	if err != nil {
		h.cleanupObjects(cp)
		httpserver.Internal(w, err)
		return
	}
	if previous != nil {
		h.cleanupObjects(*previous)
	}
	httpserver.JSON(w, http.StatusOK, h.withAlbumCoverURL(updated))
}

// deleteAlbum removes the album row; its tracks detach to singles via the
// FK's on delete set null (see Repo.DeleteAlbum). Only the album's own cover
// is cleaned up here.
func (h *Handler) deleteAlbum(w http.ResponseWriter, r *http.Request) {
	cover, err := h.repo.DeleteAlbum(r.Context(), r.PathValue("id"))
	if errors.Is(err, pgx.ErrNoRows) {
		httpserver.Err(w, http.StatusNotFound, "not_found", "album not found")
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
