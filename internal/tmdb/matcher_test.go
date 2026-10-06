package tmdb

import "testing"

func TestRankAndAutoMatch(t *testing.T) {
	hint := ExtractHint("流浪地球 2 (2023) 2160p WEB-DL")
	candidates := Rank(hint, []Candidate{
		{Kind: "movie", ID: 1, Title: "流浪地球2", OriginalTitle: "The Wandering Earth II", Year: 2023},
		{Kind: "movie", ID: 2, Title: "流浪地球", Year: 2019},
	})
	match, ok := AutoMatch(candidates)
	if !ok || match.ID != 1 {
		t.Fatalf("expected exact movie match, got %#v, %v", match, ok)
	}
	if candidates[0].Score < 85 {
		t.Fatalf("score too low: %.1f", candidates[0].Score)
	}
}

func TestAutoMatchRejectsAmbiguousResults(t *testing.T) {
	_, ok := AutoMatch([]Candidate{{ID: 1, Score: 90}, {ID: 2, Score: 85}})
	if ok {
		t.Fatal("ambiguous candidates must require confirmation")
	}
	_, ok = AutoMatch([]Candidate{{ID: 1, Score: 84.9}})
	if ok {
		t.Fatal("low confidence candidate must require confirmation")
	}
}

func TestExtractTVHint(t *testing.T) {
	hint := ExtractHint("Example.Show.2024.S01E03.1080p.WEB-DL.mkv")
	if hint.Kind != "tv" || hint.Year != 2024 || hint.Title == "" {
		t.Fatalf("unexpected hint: %#v", hint)
	}
}

func TestExtractHintRemovesCodecVariantsAndExtension(t *testing.T) {
	for _, name := range []string{
		"功夫女足 (2026).2160p.HDR10.H.265.AAC 2.0.mp4",
		"功夫女足 (2026) 1080p H265 AAC 2.0.mkv",
		"功夫女足 (2026) 1080p H-264 WEB-DL 5.1.strm",
		"功夫女足 (2026) 1080p X.265 HEVC.mkv",
		"功夫女足 (2026).FutureCodec-Z99.foo.mp4",
	} {
		hint := ExtractHint(name)
		if hint.Title != "功夫女足" || hint.Year != 2026 {
			t.Fatalf("%q: unexpected hint: %#v", name, hint)
		}
	}
}

func TestExtractTMDBIDHint(t *testing.T) {
	tests := []string{
		"沧元图 (2023) {tmdbid=229192}",
		"沧元图 (2023) [tmdbid-229192]",
		"沧元图.2023.tmdb-229192",
	}
	for _, value := range tests {
		hint := ExtractHint(value, "沧元图.2023.S01E01.mp4")
		if hint.TMDBID != 229192 || hint.Kind != "tv" || hint.Title != "沧元图" {
			t.Fatalf("%q: unexpected hint: %#v", value, hint)
		}
	}
}
