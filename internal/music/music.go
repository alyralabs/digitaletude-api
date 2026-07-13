package music

import (
	"context"
	"encoding/json"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const Bucket = "music"

type Album struct {
	ID             string          `json:"id"`
	Title          string          `json:"title"`
	Description    string          `json:"description"`
	CoverImagePath *string         `json:"-"`
	SortOrder      int             `json:"sortOrder"`
	CreatedAt      time.Time       `json:"createdAt"`
	Metadata       json.RawMessage `json:"metadata"`

	CoverURL *string  `json:"coverUrl"`
	Tracks   []*Track `json:"tracks"`
}

type Track struct {
	ID              string          `json:"id"`
	Title           string          `json:"title"`
	Description     string          `json:"description"`
	StoragePath     string          `json:"-"`
	CoverImagePath  *string         `json:"-"`
	DurationSeconds *int            `json:"durationSeconds"`
	AlbumID         *string         `json:"albumId"`
	TrackNumber     *int            `json:"trackNumber"`
	SortOrder       int             `json:"sortOrder"`
	CreatedAt       time.Time       `json:"createdAt"`
	Metadata        json.RawMessage `json:"metadata"`

	AudioURL string  `json:"audioUrl"`
	CoverURL *string `json:"coverUrl"`
}

type Repo struct {
	pool *pgxpool.Pool
}

func NewRepo(pool *pgxpool.Pool) *Repo {
	return &Repo{pool: pool}
}

const albumCols = "id, title, description, cover_image_path, sort_order, created_at, metadata"

func scanAlbum(row pgx.Row) (*Album, error) {
	var a Album
	err := row.Scan(&a.ID, &a.Title, &a.Description, &a.CoverImagePath,
		&a.SortOrder, &a.CreatedAt, &a.Metadata)
	if err != nil {
		return nil, err
	}
	a.Tracks = []*Track{}
	return &a, nil
}

const trackCols = "id, title, description, storage_path, cover_image_path, duration_seconds, album_id, track_number, sort_order, created_at, metadata"

func scanTrack(row pgx.Row) (*Track, error) {
	var t Track
	err := row.Scan(&t.ID, &t.Title, &t.Description, &t.StoragePath, &t.CoverImagePath,
		&t.DurationSeconds, &t.AlbumID, &t.TrackNumber, &t.SortOrder, &t.CreatedAt, &t.Metadata)
	if err != nil {
		return nil, err
	}
	return &t, nil
}

func (r *Repo) ListAlbums(ctx context.Context) ([]*Album, error) {
	rows, err := r.pool.Query(ctx,
		"select "+albumCols+" from albums order by sort_order, created_at desc")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	albums := []*Album{}
	for rows.Next() {
		a, err := scanAlbum(rows)
		if err != nil {
			return nil, err
		}
		albums = append(albums, a)
	}
	return albums, rows.Err()
}

// ListTracks returns every track: album tracks ordered by track number within
// their album, singles by sort_order/created_at. Grouping happens in the
// handler.
func (r *Repo) ListTracks(ctx context.Context) ([]*Track, error) {
	rows, err := r.pool.Query(ctx,
		"select "+trackCols+" from tracks order by album_id, track_number, sort_order, created_at desc")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	tracks := []*Track{}
	for rows.Next() {
		t, err := scanTrack(rows)
		if err != nil {
			return nil, err
		}
		tracks = append(tracks, t)
	}
	return tracks, rows.Err()
}

func (r *Repo) InsertAlbum(ctx context.Context, a *Album) (*Album, error) {
	return scanAlbum(r.pool.QueryRow(ctx,
		`insert into albums (title, description, cover_image_path, metadata)
		 values ($1, $2, $3, coalesce($4, '{}'::jsonb))
		 returning `+albumCols,
		a.Title, a.Description, a.CoverImagePath, a.Metadata))
}

type AlbumUpdate struct {
	Title       *string         `json:"title"`
	Description *string         `json:"description"`
	SortOrder   *int            `json:"sortOrder"`
	Metadata    json.RawMessage `json:"metadata"`
}

func (r *Repo) UpdateAlbum(ctx context.Context, id string, u AlbumUpdate) (*Album, error) {
	return scanAlbum(r.pool.QueryRow(ctx,
		`update albums set
		   title = coalesce($2, title),
		   description = coalesce($3, description),
		   sort_order = coalesce($4, sort_order),
		   metadata = coalesce($5, metadata)
		 where id = $1
		 returning `+albumCols,
		id, u.Title, u.Description, u.SortOrder, u.Metadata))
}

// DeleteAlbum removes the album row; its tracks detach to singles via the
// FK's on delete set null. Returns the cover path (may be nil) for cleanup.
func (r *Repo) DeleteAlbum(ctx context.Context, id string) (*string, error) {
	var cover *string
	err := r.pool.QueryRow(ctx,
		"delete from albums where id = $1 returning cover_image_path", id).Scan(&cover)
	return cover, err
}

func (r *Repo) InsertTrack(ctx context.Context, t *Track) (*Track, error) {
	return scanTrack(r.pool.QueryRow(ctx,
		`insert into tracks (title, description, storage_path, cover_image_path, duration_seconds, album_id, track_number)
		 values ($1, $2, $3, $4, $5, $6, $7)
		 returning `+trackCols,
		t.Title, t.Description, t.StoragePath, t.CoverImagePath, t.DurationSeconds, t.AlbumID, t.TrackNumber))
}

type TrackUpdate struct {
	Title       *string         `json:"title"`
	Description *string         `json:"description"`
	SortOrder   *int            `json:"sortOrder"`
	Metadata    json.RawMessage `json:"metadata"`
	// AlbumID: nil = unchanged, "" = detach to singles, uuid = move to album.
	AlbumID *string `json:"albumId"`
	// TrackNumber: nil = unchanged, <= 0 = clear, positive = set.
	TrackNumber *int `json:"trackNumber"`
}

func (r *Repo) UpdateTrack(ctx context.Context, id string, u TrackUpdate) (*Track, error) {
	return scanTrack(r.pool.QueryRow(ctx,
		`update tracks set
		   title = coalesce($2, title),
		   description = coalesce($3, description),
		   sort_order = coalesce($4, sort_order),
		   metadata = coalesce($5, metadata),
		   album_id = case
		     when $6::text is null then album_id
		     when $6 = '' then null
		     else $6::uuid
		   end,
		   track_number = case
		     when $7::int is null then track_number
		     when $7 <= 0 then null
		     else $7
		   end
		 where id = $1
		 returning `+trackCols,
		id, u.Title, u.Description, u.SortOrder, u.Metadata, u.AlbumID, u.TrackNumber))
}

// DeleteTrack removes the row and returns storage paths for cleanup (cover
// may be nil). Row first, storage after — same reasoning as photos.
func (r *Repo) DeleteTrack(ctx context.Context, id string) (storagePath string, coverPath *string, err error) {
	err = r.pool.QueryRow(ctx,
		"delete from tracks where id = $1 returning storage_path, cover_image_path", id).
		Scan(&storagePath, &coverPath)
	return storagePath, coverPath, err
}
