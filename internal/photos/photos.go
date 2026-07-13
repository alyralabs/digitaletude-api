package photos

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/alyralabs/digitaletude-api/internal/db"
)

const Bucket = "photography"

type Photo struct {
	ID            string    `json:"id"`
	Title         string    `json:"title"`
	Description   string    `json:"description"`
	StoragePath   string    `json:"-"`
	ThumbnailPath string    `json:"-"`
	Width         int       `json:"width"`
	Height        int       `json:"height"`
	SortOrder     int       `json:"sortOrder"`
	CreatedAt     time.Time `json:"createdAt"`

	// Full public URLs, composed by the server; the frontend never builds
	// storage URLs itself.
	OriginalURL  string `json:"originalUrl"`
	ThumbnailURL string `json:"thumbnailUrl"`
}

type Repo struct {
	db db.Querier
}

func NewRepo(q db.Querier) *Repo {
	return &Repo{db: q}
}

const photoCols = "id, title, description, storage_path, thumbnail_path, width, height, sort_order, created_at"

func scanPhoto(row pgx.Row) (*Photo, error) {
	var p Photo
	err := row.Scan(&p.ID, &p.Title, &p.Description, &p.StoragePath, &p.ThumbnailPath,
		&p.Width, &p.Height, &p.SortOrder, &p.CreatedAt)
	if err != nil {
		return nil, err
	}
	return &p, nil
}

func (r *Repo) List(ctx context.Context) ([]*Photo, error) {
	rows, err := r.db.Query(ctx,
		"select "+photoCols+" from photos order by sort_order, created_at desc")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	photos := []*Photo{}
	for rows.Next() {
		p, err := scanPhoto(rows)
		if err != nil {
			return nil, err
		}
		photos = append(photos, p)
	}
	return photos, rows.Err()
}

func (r *Repo) Get(ctx context.Context, id string) (*Photo, error) {
	return scanPhoto(r.db.QueryRow(ctx,
		"select "+photoCols+" from photos where id = $1", id))
}

func (r *Repo) Insert(ctx context.Context, p *Photo) (*Photo, error) {
	return scanPhoto(r.db.QueryRow(ctx,
		`insert into photos (title, description, storage_path, thumbnail_path, width, height)
		 values ($1, $2, $3, $4, $5, $6)
		 returning `+photoCols,
		p.Title, p.Description, p.StoragePath, p.ThumbnailPath, p.Width, p.Height))
}

type PhotoUpdate struct {
	Title       *string `json:"title"`
	Description *string `json:"description"`
	SortOrder   *int    `json:"sortOrder"`
}

func (r *Repo) Update(ctx context.Context, id string, u PhotoUpdate) (*Photo, error) {
	return scanPhoto(r.db.QueryRow(ctx,
		`update photos set
		   title = coalesce($2, title),
		   description = coalesce($3, description),
		   sort_order = coalesce($4, sort_order)
		 where id = $1
		 returning `+photoCols,
		id, u.Title, u.Description, u.SortOrder))
}

// Delete removes the row and returns the storage paths for best-effort
// cleanup. Row first, storage after: a failed storage cleanup leaves only
// invisible orphaned files instead of a site serving broken images.
func (r *Repo) Delete(ctx context.Context, id string) (storagePath, thumbnailPath string, err error) {
	err = r.db.QueryRow(ctx,
		"delete from photos where id = $1 returning storage_path, thumbnail_path", id).
		Scan(&storagePath, &thumbnailPath)
	return storagePath, thumbnailPath, err
}
