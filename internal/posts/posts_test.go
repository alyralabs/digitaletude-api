package posts

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/alyralabs/digitaletude-api/internal/testutil"
)

func insertTestPost(t *testing.T, repo *Repo, title string) *Post {
	t.Helper()
	p, err := repo.Insert(context.Background(), &Post{
		Title:           title,
		Excerpt:         "an excerpt",
		ContentMarkdown: "# " + title + "\n\nsome body text.",
	})
	if err != nil {
		t.Fatalf("Insert(%q) error = %v", title, err)
	}
	return p
}

func TestRepo_Insert_DerivesSlugFromTitle(t *testing.T) {
	repo := NewRepo(testutil.OpenTestTx(t))

	created := insertTestPost(t, repo, "My Great Post")
	if created.Slug != "my-great-post" {
		t.Errorf("Slug = %q, want %q", created.Slug, "my-great-post")
	}
	if created.Status != "draft" {
		t.Errorf("Status = %q, want %q (default)", created.Status, "draft")
	}
	if created.PublishedAt != nil {
		t.Errorf("PublishedAt = %v, want nil for a new draft", created.PublishedAt)
	}
}

func TestRepo_Insert_ResolvesSlugCollision(t *testing.T) {
	repo := NewRepo(testutil.OpenTestTx(t))

	first := insertTestPost(t, repo, "Duplicate Title")
	second := insertTestPost(t, repo, "Duplicate Title")
	third := insertTestPost(t, repo, "Duplicate Title")

	if first.Slug != "duplicate-title" {
		t.Errorf("first Slug = %q, want %q", first.Slug, "duplicate-title")
	}
	if second.Slug != "duplicate-title-2" {
		t.Errorf("second Slug = %q, want %q", second.Slug, "duplicate-title-2")
	}
	if third.Slug != "duplicate-title-3" {
		t.Errorf("third Slug = %q, want %q", third.Slug, "duplicate-title-3")
	}
}

func TestRepo_Get_NotFound(t *testing.T) {
	repo := NewRepo(testutil.OpenTestTx(t))

	_, err := repo.Get(context.Background(), "00000000-0000-0000-0000-000000000000")
	if !errors.Is(err, pgx.ErrNoRows) {
		t.Errorf("Get() error = %v, want pgx.ErrNoRows", err)
	}
}

func TestRepo_Get_ReturnsDraftsToo(t *testing.T) {
	repo := NewRepo(testutil.OpenTestTx(t))
	created := insertTestPost(t, repo, "Still A Draft")

	got, err := repo.Get(context.Background(), created.ID)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if got.Status != "draft" {
		t.Errorf("Status = %q, want %q", got.Status, "draft")
	}
}

func TestRepo_GetPublishedBySlug_HidesDrafts(t *testing.T) {
	repo := NewRepo(testutil.OpenTestTx(t))
	created := insertTestPost(t, repo, "Hidden Draft")

	_, err := repo.GetPublishedBySlug(context.Background(), created.Slug)
	if !errors.Is(err, pgx.ErrNoRows) {
		t.Errorf("GetPublishedBySlug(draft) error = %v, want pgx.ErrNoRows", err)
	}

	if _, err := repo.Publish(context.Background(), created.ID); err != nil {
		t.Fatalf("Publish() error = %v", err)
	}

	got, err := repo.GetPublishedBySlug(context.Background(), created.Slug)
	if err != nil {
		t.Fatalf("GetPublishedBySlug(published) error = %v", err)
	}
	if got.ID != created.ID {
		t.Errorf("GetPublishedBySlug() id = %q, want %q", got.ID, created.ID)
	}
}

func TestRepo_ListPublished_ExcludesDrafts(t *testing.T) {
	repo := NewRepo(testutil.OpenTestTx(t))
	draft := insertTestPost(t, repo, "A Draft Post")
	published := insertTestPost(t, repo, "A Published Post")
	if _, err := repo.Publish(context.Background(), published.ID); err != nil {
		t.Fatalf("Publish() error = %v", err)
	}

	list, err := repo.ListPublished(context.Background())
	if err != nil {
		t.Fatalf("ListPublished() error = %v", err)
	}
	var sawDraft, sawPublished bool
	for _, p := range list {
		if p.ID == draft.ID {
			sawDraft = true
		}
		if p.ID == published.ID {
			sawPublished = true
		}
	}
	if sawDraft {
		t.Error("ListPublished() included a draft post")
	}
	if !sawPublished {
		t.Error("ListPublished() did not include the published post")
	}
}

func TestRepo_ListAll_IncludesDraftsAndPublished(t *testing.T) {
	repo := NewRepo(testutil.OpenTestTx(t))
	draft := insertTestPost(t, repo, "Draft For ListAll")
	published := insertTestPost(t, repo, "Published For ListAll")
	if _, err := repo.Publish(context.Background(), published.ID); err != nil {
		t.Fatalf("Publish() error = %v", err)
	}

	list, err := repo.ListAll(context.Background())
	if err != nil {
		t.Fatalf("ListAll() error = %v", err)
	}
	var sawDraft, sawPublished bool
	for _, p := range list {
		if p.ID == draft.ID {
			sawDraft = true
		}
		if p.ID == published.ID {
			sawPublished = true
		}
	}
	if !sawDraft || !sawPublished {
		t.Errorf("ListAll() sawDraft=%v sawPublished=%v, want both true", sawDraft, sawPublished)
	}
}

func TestRepo_Update_OnlyChangesProvidedFields(t *testing.T) {
	repo := NewRepo(testutil.OpenTestTx(t))
	created := insertTestPost(t, repo, "Original Title")

	newTitle := "Updated Title"
	updated, err := repo.Update(context.Background(), created.ID, PostUpdate{Title: &newTitle})
	if err != nil {
		t.Fatalf("Update(title only) error = %v", err)
	}
	if updated.Title != newTitle {
		t.Errorf("Title = %q, want %q", updated.Title, newTitle)
	}
	if updated.Excerpt != created.Excerpt {
		t.Errorf("Excerpt = %q, want unchanged %q", updated.Excerpt, created.Excerpt)
	}
	if updated.Slug != created.Slug {
		t.Errorf("Slug = %q, want unchanged %q (title change alone shouldn't touch slug)", updated.Slug, created.Slug)
	}
}

func TestRepo_Update_ManualSlugResolvesCollision(t *testing.T) {
	repo := NewRepo(testutil.OpenTestTx(t))
	existing := insertTestPost(t, repo, "Existing Slug Owner")
	editing := insertTestPost(t, repo, "Original")

	updated, err := repo.Update(context.Background(), editing.ID, PostUpdate{Slug: &existing.Slug})
	if err != nil {
		t.Fatalf("Update(slug) error = %v", err)
	}
	want := existing.Slug + "-2"
	if updated.Slug != want {
		t.Errorf("Slug = %q, want %q (silently resolved collision)", updated.Slug, want)
	}
}

func TestRepo_Update_SlugFrozenOncePublished(t *testing.T) {
	repo := NewRepo(testutil.OpenTestTx(t))
	created := insertTestPost(t, repo, "Freeze Me")
	if _, err := repo.Publish(context.Background(), created.ID); err != nil {
		t.Fatalf("Publish() error = %v", err)
	}

	newSlug := "attempted-new-slug"
	updated, err := repo.Update(context.Background(), created.ID, PostUpdate{Slug: &newSlug})
	if err != nil {
		t.Fatalf("Update(slug on published) error = %v", err)
	}
	if updated.Slug != created.Slug {
		t.Errorf("Slug = %q after publish, want unchanged %q (frozen, submitted value silently ignored)", updated.Slug, created.Slug)
	}
}

func TestRepo_PublishUnpublish_KeepsOriginalPublishedAt(t *testing.T) {
	repo := NewRepo(testutil.OpenTestTx(t))
	created := insertTestPost(t, repo, "Publish Cycle")

	published, err := repo.Publish(context.Background(), created.ID)
	if err != nil {
		t.Fatalf("Publish() error = %v", err)
	}
	if published.Status != "published" {
		t.Errorf("Status = %q, want %q", published.Status, "published")
	}
	if published.PublishedAt == nil {
		t.Fatal("PublishedAt is nil after Publish()")
	}
	firstPublishedAt := *published.PublishedAt

	unpublished, err := repo.Unpublish(context.Background(), created.ID)
	if err != nil {
		t.Fatalf("Unpublish() error = %v", err)
	}
	if unpublished.Status != "draft" {
		t.Errorf("Status = %q after Unpublish(), want %q", unpublished.Status, "draft")
	}
	if unpublished.PublishedAt == nil || !unpublished.PublishedAt.Equal(firstPublishedAt) {
		t.Errorf("PublishedAt = %v after Unpublish(), want unchanged %v", unpublished.PublishedAt, firstPublishedAt)
	}

	republished, err := repo.Publish(context.Background(), created.ID)
	if err != nil {
		t.Fatalf("Publish() (again) error = %v", err)
	}
	if republished.PublishedAt == nil || !republished.PublishedAt.Equal(firstPublishedAt) {
		t.Errorf("PublishedAt = %v after republish, want original %v (not reset)", republished.PublishedAt, firstPublishedAt)
	}
}

func TestRepo_Delete_RemovesRowAndReturnsCoverPath(t *testing.T) {
	repo := NewRepo(testutil.OpenTestTx(t))
	cover := "covers/original.jpg"
	created, err := repo.Insert(context.Background(), &Post{Title: "Has Cover", CoverImagePath: &cover})
	if err != nil {
		t.Fatalf("Insert() error = %v", err)
	}

	gotCover, err := repo.Delete(context.Background(), created.ID)
	if err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	if gotCover == nil || *gotCover != cover {
		t.Errorf("Delete() cover = %v, want %q", gotCover, cover)
	}

	_, err = repo.Get(context.Background(), created.ID)
	if !errors.Is(err, pgx.ErrNoRows) {
		t.Errorf("Get() after Delete() error = %v, want pgx.ErrNoRows", err)
	}
}
