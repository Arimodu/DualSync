package dualsync

import "math/rand/v2"

type musicShuffle struct {
	tracks    []PlexTrack
	remaining []PlexTrack
	used      map[string]bool
	last      string
}

func (s *musicShuffle) update(tracks []PlexTrack) {
	if s.used == nil {
		s.used = map[string]bool{}
		if s.last != "" {
			s.used[s.last] = true
		}
	}
	s.tracks = nil
	unique := map[string]bool{}
	for _, track := range tracks {
		if !unique[track.ID] {
			s.tracks = append(s.tracks, track)
			unique[track.ID] = true
		}
	}
	s.refill()
}

func (s *musicShuffle) refill() {
	s.remaining = nil
	for _, track := range s.tracks {
		if !s.used[track.ID] {
			s.remaining = append(s.remaining, track)
		}
	}
	rand.Shuffle(len(s.remaining), func(i, j int) { s.remaining[i], s.remaining[j] = s.remaining[j], s.remaining[i] })
	if len(s.remaining) > 1 && s.remaining[0].ID == s.last {
		s.remaining[0], s.remaining[1] = s.remaining[1], s.remaining[0]
	}
}

func (s *musicShuffle) next() PlexTrack {
	if len(s.remaining) == 0 {
		s.used = map[string]bool{}
		s.refill()
	}
	track := s.remaining[0]
	s.remaining = s.remaining[1:]
	s.used[track.ID] = true
	s.last = track.ID
	return track
}
