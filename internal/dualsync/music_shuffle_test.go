package dualsync

import "testing"

func TestMusicShuffleCyclesSurviveCatalogRefresh(t *testing.T) {
	tracks := []PlexTrack{{ID: "a"}, {ID: "b"}, {ID: "c"}, {ID: "a"}, {ID: "d"}}
	s := musicShuffle{}
	s.update(tracks)
	last := ""
	for cycle := 0; cycle < 50; cycle++ {
		seen := map[string]bool{}
		for i := 0; i < 4; i++ {
			s.update(tracks)
			track := s.next()
			if seen[track.ID] {
				t.Fatalf("repeat before cycle completed: %s", track.ID)
			}
			if i == 0 && track.ID == last {
				t.Fatal("same song across cycle boundary")
			}
			seen[track.ID] = true
			last = track.ID
		}
	}
}

func TestMusicShuffleCatalogChangesKeepHistory(t *testing.T) {
	s := musicShuffle{last: "current"}
	s.update([]PlexTrack{{ID: "current"}, {ID: "a"}})
	if got := s.next().ID; got != "a" {
		t.Fatal("current song repeated", got)
	}
	s.update([]PlexTrack{{ID: "current"}, {ID: "a"}, {ID: "new"}})
	if got := s.next().ID; got != "new" {
		t.Fatal("new song not included before repeating", got)
	}
	s.update([]PlexTrack{{ID: "only"}, {ID: "only"}})
	for i := 0; i < 5; i++ {
		if got := s.next().ID; got != "only" {
			t.Fatal("one-song source failed", got)
		}
	}
}
