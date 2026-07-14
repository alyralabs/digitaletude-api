package music

import (
	"context"
	"testing"

	"github.com/alyralabs/digitaletude-api/internal/testutil"
)

func insertTestAlbum(t *testing.T, repo *Repo, title string) *Album {
	t.Helper()
	a, err := repo.InsertAlbum(context.Background(), &Album{
		Title:       title,
		Description: "an album description",
	})
	if err != nil {
		t.Fatalf("InsertAlbum(%q) error = %v", title, err)
	}
	return a
}

func insertTestTrack(t *testing.T, repo *Repo, title string, albumID *string, trackNumber *int) *Track {
	t.Helper()
	tr, err := repo.InsertTrack(context.Background(), &Track{
		Title:       title,
		Description: "a track description",
		StoragePath: "tracks/" + title + ".mp3",
		AlbumID:     albumID,
		TrackNumber: trackNumber,
	})
	if err != nil {
		t.Fatalf("InsertTrack(%q) error = %v", title, err)
	}
	return tr
}

func TestRepo_InsertAlbum_PersistsFieldsAndDefaultsSortOrder(t *testing.T) {
	repo := NewRepo(testutil.OpenTestTx(t))

	created := insertTestAlbum(t, repo, "Insert Test Album")
	if created.ID == "" {
		t.Fatal("InsertAlbum() returned empty ID")
	}
	if created.SortOrder != 0 {
		t.Errorf("SortOrder = %d, want 0 (default)", created.SortOrder)
	}
	if created.Title != "Insert Test Album" || created.Description != "an album description" {
		t.Errorf("InsertAlbum() did not persist title/description correctly: %+v", created)
	}
}

func TestRepo_ListAlbums_OrderedBySortOrder(t *testing.T) {
	repo := NewRepo(testutil.OpenTestTx(t))

	a := insertTestAlbum(t, repo, "A")
	b := insertTestAlbum(t, repo, "B")
	c := insertTestAlbum(t, repo, "C")

	three, two, one := 3, 2, 1
	if _, err := repo.UpdateAlbum(context.Background(), a.ID, AlbumUpdate{SortOrder: &three}); err != nil {
		t.Fatalf("UpdateAlbum(a) error = %v", err)
	}
	if _, err := repo.UpdateAlbum(context.Background(), b.ID, AlbumUpdate{SortOrder: &one}); err != nil {
		t.Fatalf("UpdateAlbum(b) error = %v", err)
	}
	if _, err := repo.UpdateAlbum(context.Background(), c.ID, AlbumUpdate{SortOrder: &two}); err != nil {
		t.Fatalf("UpdateAlbum(c) error = %v", err)
	}

	list, err := repo.ListAlbums(context.Background())
	if err != nil {
		t.Fatalf("ListAlbums() error = %v", err)
	}

	var gotOrder []string
	for _, al := range list {
		if al.ID == a.ID || al.ID == b.ID || al.ID == c.ID {
			gotOrder = append(gotOrder, al.Title)
		}
	}
	want := []string{"B", "C", "A"} // sort_order 1, 2, 3
	if len(gotOrder) != len(want) {
		t.Fatalf("ListAlbums() returned %d of our 3 test rows, want 3 (got titles: %v)", len(gotOrder), gotOrder)
	}
	for i := range want {
		if gotOrder[i] != want[i] {
			t.Errorf("ListAlbums() order = %v, want %v", gotOrder, want)
			break
		}
	}
}

func TestRepo_UpdateAlbum_OnlyChangesProvidedFields(t *testing.T) {
	repo := NewRepo(testutil.OpenTestTx(t))
	created := insertTestAlbum(t, repo, "Original Album Title")

	newTitle := "Updated Album Title"
	updated, err := repo.UpdateAlbum(context.Background(), created.ID, AlbumUpdate{Title: &newTitle})
	if err != nil {
		t.Fatalf("UpdateAlbum(title only) error = %v", err)
	}
	if updated.Title != newTitle {
		t.Errorf("Title = %q, want %q", updated.Title, newTitle)
	}
	if updated.Description != created.Description {
		t.Errorf("Description = %q, want unchanged %q", updated.Description, created.Description)
	}
}

func TestRepo_DeleteAlbum_RemovesRowAndReturnsCoverPath(t *testing.T) {
	repo := NewRepo(testutil.OpenTestTx(t))
	cover := "covers/original.jpg"
	created, err := repo.InsertAlbum(context.Background(), &Album{Title: "Has Cover", CoverImagePath: &cover})
	if err != nil {
		t.Fatalf("InsertAlbum() error = %v", err)
	}

	gotCover, err := repo.DeleteAlbum(context.Background(), created.ID)
	if err != nil {
		t.Fatalf("DeleteAlbum() error = %v", err)
	}
	if gotCover == nil || *gotCover != cover {
		t.Errorf("DeleteAlbum() cover = %v, want %q", gotCover, cover)
	}
}

func TestRepo_DeleteAlbum_DetachesTracksToSingles(t *testing.T) {
	repo := NewRepo(testutil.OpenTestTx(t))
	album := insertTestAlbum(t, repo, "Album With Tracks")
	one := 1
	track := insertTestTrack(t, repo, "Attached Track", &album.ID, &one)

	if _, err := repo.DeleteAlbum(context.Background(), album.ID); err != nil {
		t.Fatalf("DeleteAlbum() error = %v", err)
	}

	tracks, err := repo.ListTracks(context.Background())
	if err != nil {
		t.Fatalf("ListTracks() error = %v", err)
	}
	var found *Track
	for _, tr := range tracks {
		if tr.ID == track.ID {
			found = tr
		}
	}
	if found == nil {
		t.Fatal("track disappeared after its album was deleted, want it detached to singles")
	}
	if found.AlbumID != nil {
		t.Errorf("AlbumID = %v after album delete, want nil (detached to singles)", *found.AlbumID)
	}
}

func TestRepo_InsertTrack_PersistsFields(t *testing.T) {
	repo := NewRepo(testutil.OpenTestTx(t))

	created := insertTestTrack(t, repo, "Insert Track Test", nil, nil)
	if created.ID == "" {
		t.Fatal("InsertTrack() returned empty ID")
	}
	if created.Title != "Insert Track Test" {
		t.Errorf("Title = %q, want %q", created.Title, "Insert Track Test")
	}
	if created.AlbumID != nil {
		t.Errorf("AlbumID = %v, want nil (single)", created.AlbumID)
	}
}

func TestRepo_UpdateTrack_MoveBetweenAlbumsAndDetachToSingles(t *testing.T) {
	repo := NewRepo(testutil.OpenTestTx(t))
	albumA := insertTestAlbum(t, repo, "Album A")
	albumB := insertTestAlbum(t, repo, "Album B")
	one := 1
	track := insertTestTrack(t, repo, "Movable Track", &albumA.ID, &one)

	moved, err := repo.UpdateTrack(context.Background(), track.ID, TrackUpdate{AlbumID: &albumB.ID})
	if err != nil {
		t.Fatalf("UpdateTrack(move) error = %v", err)
	}
	if moved.AlbumID == nil || *moved.AlbumID != albumB.ID {
		t.Errorf("AlbumID = %v, want %q", moved.AlbumID, albumB.ID)
	}

	empty := ""
	detached, err := repo.UpdateTrack(context.Background(), track.ID, TrackUpdate{AlbumID: &empty})
	if err != nil {
		t.Fatalf("UpdateTrack(detach) error = %v", err)
	}
	if detached.AlbumID != nil {
		t.Errorf("AlbumID = %v after detach, want nil", *detached.AlbumID)
	}
}

func TestRepo_UpdateTrack_ClearAndSetTrackNumber(t *testing.T) {
	repo := NewRepo(testutil.OpenTestTx(t))
	album := insertTestAlbum(t, repo, "Album For Track Numbers")
	one := 1
	track := insertTestTrack(t, repo, "Numbered Track", &album.ID, &one)

	five := 5
	updated, err := repo.UpdateTrack(context.Background(), track.ID, TrackUpdate{TrackNumber: &five})
	if err != nil {
		t.Fatalf("UpdateTrack(set number) error = %v", err)
	}
	if updated.TrackNumber == nil || *updated.TrackNumber != 5 {
		t.Errorf("TrackNumber = %v, want 5", updated.TrackNumber)
	}

	zero := 0
	cleared, err := repo.UpdateTrack(context.Background(), track.ID, TrackUpdate{TrackNumber: &zero})
	if err != nil {
		t.Fatalf("UpdateTrack(clear number) error = %v", err)
	}
	if cleared.TrackNumber != nil {
		t.Errorf("TrackNumber = %v after clear (<= 0), want nil", *cleared.TrackNumber)
	}
}

func TestRepo_DeleteTrack_RemovesRowAndReturnsPaths(t *testing.T) {
	repo := NewRepo(testutil.OpenTestTx(t))
	created := insertTestTrack(t, repo, "To Delete", nil, nil)

	storagePath, coverPath, err := repo.DeleteTrack(context.Background(), created.ID)
	if err != nil {
		t.Fatalf("DeleteTrack() error = %v", err)
	}
	if storagePath != created.StoragePath {
		t.Errorf("storagePath = %q, want %q", storagePath, created.StoragePath)
	}
	if coverPath != nil {
		t.Errorf("coverPath = %v, want nil", *coverPath)
	}

	tracks, err := repo.ListTracks(context.Background())
	if err != nil {
		t.Fatalf("ListTracks() error = %v", err)
	}
	for _, tr := range tracks {
		if tr.ID == created.ID {
			t.Fatal("track still present after DeleteTrack()")
		}
	}
}
