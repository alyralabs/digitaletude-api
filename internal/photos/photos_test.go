package photos

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/alyralabs/digitaletude-api/internal/testutil"
)

func insertTestPhoto(t *testing.T, repo *Repo, title string) *Photo {
	t.Helper()
	p, err := repo.Insert(context.Background(), &Photo{
		Title:         title,
		Description:   "a description",
		StoragePath:   "originals/" + title + ".jpg",
		ThumbnailPath: "thumbnails/" + title + ".jpg",
		Width:         800,
		Height:        600,
	})
	if err != nil {
		t.Fatalf("Insert(%q) error = %v", title, err)
	}
	return p
}

func TestRepo_InsertPersistsFieldsAndDefaultsSortOrder(t *testing.T) {
	repo := NewRepo(testutil.OpenTestTx(t))

	created := insertTestPhoto(t, repo, "Insert Test")
	if created.ID == "" {
		t.Fatal("Insert() returned empty ID")
	}
	if created.SortOrder != 0 {
		t.Errorf("SortOrder = %d, want 0 (default)", created.SortOrder)
	}
	if created.Title != "Insert Test" || created.Description != "a description" {
		t.Errorf("Insert() did not persist title/description correctly: %+v", created)
	}
	if created.Width != 800 || created.Height != 600 {
		t.Errorf("Insert() did not persist width/height correctly: %+v", created)
	}

	got, err := repo.Get(context.Background(), created.ID)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if got.ID != created.ID {
		t.Errorf("Get() id = %q, want %q", got.ID, created.ID)
	}
}

func TestRepo_Get_NotFound(t *testing.T) {
	repo := NewRepo(testutil.OpenTestTx(t))

	_, err := repo.Get(context.Background(), "00000000-0000-0000-0000-000000000000")
	if !errors.Is(err, pgx.ErrNoRows) {
		t.Errorf("Get() error = %v, want pgx.ErrNoRows", err)
	}
}

func TestRepo_List_OrderedBySortOrder(t *testing.T) {
	repo := NewRepo(testutil.OpenTestTx(t))

	a := insertTestPhoto(t, repo, "A")
	b := insertTestPhoto(t, repo, "B")
	c := insertTestPhoto(t, repo, "C")

	three, two, one := 3, 2, 1
	if _, err := repo.Update(context.Background(), a.ID, PhotoUpdate{SortOrder: &three}); err != nil {
		t.Fatalf("Update(a) error = %v", err)
	}
	if _, err := repo.Update(context.Background(), b.ID, PhotoUpdate{SortOrder: &one}); err != nil {
		t.Fatalf("Update(b) error = %v", err)
	}
	if _, err := repo.Update(context.Background(), c.ID, PhotoUpdate{SortOrder: &two}); err != nil {
		t.Fatalf("Update(c) error = %v", err)
	}

	list, err := repo.List(context.Background())
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}

	var gotOrder []string
	for _, p := range list {
		if p.ID == a.ID || p.ID == b.ID || p.ID == c.ID {
			gotOrder = append(gotOrder, p.Title)
		}
	}
	want := []string{"B", "C", "A"} // sort_order 1, 2, 3
	if len(gotOrder) != len(want) {
		t.Fatalf("List() returned %d of our 3 test rows, want 3 (got titles: %v)", len(gotOrder), gotOrder)
	}
	for i := range want {
		if gotOrder[i] != want[i] {
			t.Errorf("List() order = %v, want %v", gotOrder, want)
			break
		}
	}
}

func TestRepo_Update_OnlyChangesProvidedFields(t *testing.T) {
	repo := NewRepo(testutil.OpenTestTx(t))
	created := insertTestPhoto(t, repo, "Original Title")

	newTitle := "Updated Title"
	updated, err := repo.Update(context.Background(), created.ID, PhotoUpdate{Title: &newTitle})
	if err != nil {
		t.Fatalf("Update(title only) error = %v", err)
	}
	if updated.Title != newTitle {
		t.Errorf("Title = %q, want %q", updated.Title, newTitle)
	}
	if updated.Description != created.Description {
		t.Errorf("Description = %q, want unchanged %q", updated.Description, created.Description)
	}
	if updated.SortOrder != created.SortOrder {
		t.Errorf("SortOrder = %d, want unchanged %d", updated.SortOrder, created.SortOrder)
	}

	newSortOrder := 5
	updated2, err := repo.Update(context.Background(), created.ID, PhotoUpdate{SortOrder: &newSortOrder})
	if err != nil {
		t.Fatalf("Update(sortOrder only) error = %v", err)
	}
	if updated2.SortOrder != 5 {
		t.Errorf("SortOrder = %d, want 5", updated2.SortOrder)
	}
	if updated2.Title != newTitle {
		t.Errorf("Title = %q, want unchanged %q (from previous update)", updated2.Title, newTitle)
	}
}

func TestRepo_Delete_RemovesRowAndReturnsPaths(t *testing.T) {
	repo := NewRepo(testutil.OpenTestTx(t))
	created := insertTestPhoto(t, repo, "To Delete")

	storagePath, thumbnailPath, err := repo.Delete(context.Background(), created.ID)
	if err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	if storagePath != created.StoragePath {
		t.Errorf("storagePath = %q, want %q", storagePath, created.StoragePath)
	}
	if thumbnailPath != created.ThumbnailPath {
		t.Errorf("thumbnailPath = %q, want %q", thumbnailPath, created.ThumbnailPath)
	}

	_, err = repo.Get(context.Background(), created.ID)
	if !errors.Is(err, pgx.ErrNoRows) {
		t.Errorf("Get() after Delete() error = %v, want pgx.ErrNoRows", err)
	}
}
