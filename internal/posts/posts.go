package posts

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/alyralabs/digitaletude-api/internal/db"
)

const Bucket = "blog"

type Post struct {
	ID              string     `json:"id"`
	Slug            string     `json:"slug"`
	Title           string     `json:"title"`
	Excerpt         string     `json:"excerpt"`
	ContentMarkdown string     `json:"contentMarkdown"`
	CoverImagePath  *string    `json:"-"`
	Status          string     `json:"status"`
	CreatedAt       time.Time  `json:"createdAt"`
	UpdatedAt       time.Time  `json:"updatedAt"`
	PublishedAt     *time.Time `json:"publishedAt"`

	// Full public URL, composed by the server; the frontend never builds
	// storage URLs itself.
	CoverURL *string `json:"coverUrl"`
}

type Repo struct {
	db db.Querier
}

func NewRepo(q db.Querier) *Repo {
	return &Repo{db: q}
}

const postCols = "id, slug, title, excerpt, content_markdown, cover_image_path, status, created_at, updated_at, published_at"

func scanPost(row pgx.Row) (*Post, error) {
	var p Post
	err := row.Scan(&p.ID, &p.Slug, &p.Title, &p.Excerpt, &p.ContentMarkdown, &p.CoverImagePath,
		&p.Status, &p.CreatedAt, &p.UpdatedAt, &p.PublishedAt)
	if err != nil {
		return nil, err
	}
	return &p, nil
}

func scanPosts(rows pgx.Rows) ([]*Post, error) {
	defer rows.Close()
	posts := []*Post{}
	for rows.Next() {
		p, err := scanPost(rows)
		if err != nil {
			return nil, err
		}
		posts = append(posts, p)
	}
	return posts, rows.Err()
}

// ListPublished returns published posts newest-first, matching the
// posts_published_idx query shape.
func (r *Repo) ListPublished(ctx context.Context) ([]*Post, error) {
	rows, err := r.db.Query(ctx,
		"select "+postCols+" from posts where status = 'published' order by published_at desc")
	if err != nil {
		return nil, err
	}
	return scanPosts(rows)
}

// ListAll returns every post regardless of status, most recently worked-on
// first, for the admin list.
func (r *Repo) ListAll(ctx context.Context) ([]*Post, error) {
	rows, err := r.db.Query(ctx,
		"select "+postCols+" from posts order by updated_at desc")
	if err != nil {
		return nil, err
	}
	return scanPosts(rows)
}

// Get returns a post by id regardless of status, for the admin editor.
func (r *Repo) Get(ctx context.Context, id string) (*Post, error) {
	return scanPost(r.db.QueryRow(ctx,
		"select "+postCols+" from posts where id = $1", id))
}

// GetPublishedBySlug returns a published post by slug. Drafts and unknown
// slugs both come back as pgx.ErrNoRows, so the public endpoint never
// leaks which slugs exist as drafts.
func (r *Repo) GetPublishedBySlug(ctx context.Context, slug string) (*Post, error) {
	return scanPost(r.db.QueryRow(ctx,
		"select "+postCols+" from posts where slug = $1 and status = 'published'", slug))
}

// uniqueSlug resolves base to a slug not currently in use, appending -2,
// -3, ... on collision. excludeID (optional) lets an update check
// uniqueness against every row except the one being edited.
func (r *Repo) uniqueSlug(ctx context.Context, base, excludeID string) (string, error) {
	slug := base
	for i := 2; ; i++ {
		var exists bool
		var err error
		if excludeID == "" {
			err = r.db.QueryRow(ctx,
				"select exists(select 1 from posts where slug = $1)", slug).Scan(&exists)
		} else {
			err = r.db.QueryRow(ctx,
				"select exists(select 1 from posts where slug = $1 and id != $2)", slug, excludeID).Scan(&exists)
		}
		if err != nil {
			return "", err
		}
		if !exists {
			return slug, nil
		}
		slug = fmt.Sprintf("%s-%d", base, i)
	}
}

func (r *Repo) Insert(ctx context.Context, p *Post) (*Post, error) {
	slug, err := r.uniqueSlug(ctx, slugify(p.Title), "")
	if err != nil {
		return nil, err
	}
	return scanPost(r.db.QueryRow(ctx,
		`insert into posts (slug, title, excerpt, content_markdown, cover_image_path)
		 values ($1, $2, $3, $4, $5)
		 returning `+postCols,
		slug, p.Title, p.Excerpt, p.ContentMarkdown, p.CoverImagePath))
}

type PostUpdate struct {
	Title           *string `json:"title"`
	Excerpt         *string `json:"excerpt"`
	ContentMarkdown *string `json:"contentMarkdown"`
	Slug            *string `json:"slug"`
}

// Update applies a partial edit. A submitted slug is re-slugified and
// resolved against collisions (excluding this row) before the query runs,
// but the query itself only applies it while the post is still a draft —
// once published, the slug is frozen and the submitted value is silently
// ignored rather than rejected.
func (r *Repo) Update(ctx context.Context, id string, u PostUpdate) (*Post, error) {
	var slug *string
	if u.Slug != nil {
		resolved, err := r.uniqueSlug(ctx, slugify(*u.Slug), id)
		if err != nil {
			return nil, err
		}
		slug = &resolved
	}
	return scanPost(r.db.QueryRow(ctx,
		`update posts set
		   title = coalesce($2, title),
		   excerpt = coalesce($3, excerpt),
		   content_markdown = coalesce($4, content_markdown),
		   slug = case when status = 'published' then slug else coalesce($5, slug) end,
		   updated_at = now()
		 where id = $1
		 returning `+postCols,
		id, u.Title, u.Excerpt, u.ContentMarkdown, slug))
}

// Publish sets status to published. published_at is only set the first
// time — an unpublish/republish cycle keeps the original publish date.
func (r *Repo) Publish(ctx context.Context, id string) (*Post, error) {
	return scanPost(r.db.QueryRow(ctx,
		`update posts set
		   status = 'published',
		   published_at = coalesce(published_at, now()),
		   updated_at = now()
		 where id = $1
		 returning `+postCols,
		id))
}

// Unpublish sets status back to draft without touching published_at.
func (r *Repo) Unpublish(ctx context.Context, id string) (*Post, error) {
	return scanPost(r.db.QueryRow(ctx,
		`update posts set
		   status = 'draft',
		   updated_at = now()
		 where id = $1
		 returning `+postCols,
		id))
}

// Delete removes the row and returns the cover path (may be nil) for
// best-effort cleanup. Row first, storage after — same reasoning as
// photos/tracks.
func (r *Repo) Delete(ctx context.Context, id string) (coverPath *string, err error) {
	err = r.db.QueryRow(ctx,
		"delete from posts where id = $1 returning cover_image_path", id).Scan(&coverPath)
	return coverPath, err
}
