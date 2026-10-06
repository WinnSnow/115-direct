package organize

import (
	"strings"
	"testing"

	"github.com/local/115-direct/internal/tmdb"
)

func TestEpisodeQualityIsNotAnEpisodeRange(t *testing.T) {
	for _, name := range []string{"Show - S01E02 - 2160P WEB-DL.mkv", "Show S01E02 - 1080p.mkv", "Show S01E02 - 720p.mkv", "Show S01E02 - H265.mkv"} {
		ep, ok := parseEpisode(name)
		if !ok || ep.Season != 1 || ep.Start != 2 || ep.End != 2 {
			t.Fatalf("quality parsed as range: %q %#v", name, ep)
		}
	}
	for _, name := range []string{"Show S01E02-E03.mkv", "Show S01E02-03.mkv", "Show S01E02~E03.mkv"} {
		ep, ok := parseEpisode(name)
		if !ok || ep.Start != 2 || ep.End != 3 {
			t.Fatalf("valid range rejected: %q %#v", name, ep)
		}
	}
}

func TestJellyfinNaming(t *testing.T) {
	details := &tmdb.Details{Kind: "movie", ID: 603, Title: "The Matrix", Year: 1999}
	if got := titleFolder(details); got != "The Matrix (1999) [tmdbid-603]" {
		t.Fatalf("folder: %s", got)
	}
	got := movieName(details, "The.Matrix.1999.2160p.WEB-DL.DV.mkv")
	for _, part := range []string{"The Matrix (1999)", "2160P", "WEB-DL", "DV", ".mkv"} {
		if !strings.Contains(got, part) {
			t.Fatalf("%q missing %q", got, part)
		}
	}
}

func TestEpisodeAndSubtitleNaming(t *testing.T) {
	details := &tmdb.Details{Kind: "tv", ID: 1399, Title: "权力的游戏", Year: 2011}
	ep, ok := parseEpisode("Game.of.Thrones.S01E01-E02.1080p.mkv")
	if !ok || ep.Season != 1 || ep.Start != 1 || ep.End != 2 {
		t.Fatalf("episode: %#v %v", ep, ok)
	}
	if got := episodeName(details, ep, "show.S01E01-E02.zh-CN.ass"); got != "权力的游戏 - S01E01-E02.zh-cn.ass" {
		t.Fatalf("subtitle: %s", got)
	}
}

func TestMediaSignature(t *testing.T) {
	secret := []byte("01234567890123456789012345678901")
	sig := SignMedia(secret, "media-id")
	if !VerifyMedia(secret, "media-id", sig) || VerifyMedia(secret, "other", sig) {
		t.Fatal("signature verification failed")
	}
	jellyfinSig := SignJellyfinRequest(secret, "media-id")
	if jellyfinSig == sig || !VerifyJellyfinRequest(secret, "media-id", jellyfinSig) || VerifyJellyfinRequest(secret, "other", jellyfinSig) {
		t.Fatal("Jellyfin signature verification failed")
	}
	if got := versionedName("Movie (2024).mkv", "ABCDEF123456"); got != "Movie (2024) - abcdef12.mkv" {
		t.Fatalf("version name: %s", got)
	}
}

func TestMediaDirectories(t *testing.T) {
	tests := []struct {
		name        string
		details     tmdb.Details
		category    string
		subcategory string
	}{
		{name: "Chinese movie by country", details: tmdb.Details{Kind: "movie", OriginCountries: []string{"CN"}, OriginalLanguage: "en"}, category: "电影", subcategory: "华语电影"},
		{name: "Chinese movie by language", details: tmdb.Details{Kind: "movie", OriginalLanguage: "zh"}, category: "电影", subcategory: "华语电影"},
		{name: "Japanese movie", details: tmdb.Details{Kind: "movie", OriginCountries: []string{"JP"}}, category: "电影", subcategory: "日韩电影"},
		{name: "Korean movie", details: tmdb.Details{Kind: "movie", OriginalLanguage: "ko"}, category: "电影", subcategory: "日韩电影"},
		{name: "Western movie", details: tmdb.Details{Kind: "movie", OriginCountries: []string{"US"}, OriginalLanguage: "en"}, category: "电影", subcategory: "欧美电影"},
		{name: "Documentary takes priority", details: tmdb.Details{Kind: "tv", OriginCountries: []string{"JP"}, GenreIDs: []int{16, 99}}, category: "电视剧", subcategory: "纪录片"},
		{name: "Japanese animation", details: tmdb.Details{Kind: "tv", OriginCountries: []string{"JP"}, GenreIDs: []int{16}}, category: "电视剧", subcategory: "日番"},
		{name: "Chinese animation", details: tmdb.Details{Kind: "tv", OriginalLanguage: "zh", GenreIDs: []int{16}}, category: "电视剧", subcategory: "国漫"},
		{name: "Domestic series", details: tmdb.Details{Kind: "tv", OriginCountries: []string{"HK"}}, category: "电视剧", subcategory: "国产剧"},
		{name: "Japanese and Korean series", details: tmdb.Details{Kind: "tv", OriginCountries: []string{"KR"}}, category: "电视剧", subcategory: "日韩剧"},
		{name: "Western series", details: tmdb.Details{Kind: "tv", OriginCountries: []string{"GB"}}, category: "电视剧", subcategory: "欧美剧"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			category, subcategory := mediaDirectories(&test.details)
			if category != test.category || subcategory != test.subcategory {
				t.Fatalf("got %s/%s, want %s/%s", category, subcategory, test.category, test.subcategory)
			}
		})
	}
}
