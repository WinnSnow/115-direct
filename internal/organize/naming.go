package organize

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/local/115-direct/internal/tmdb"
)

var (
	episodeRE  = regexp.MustCompile(`(?i)S(\d{1,2})E(\d{1,3})\b(?:\s*[-~]\s*E?(\d{1,3})\b)?`)
	qualityRE  = regexp.MustCompile(`(?i)(2160p|1080p|720p|4K|UHD|BluRay|WEB[- .]?DL|WEBRip|HDTV|REMUX|HDR10\+?|HDR|DV|DoVi|x26[45]|H\.26[45]|HEVC|AVC|Atmos|DTS(?:-HD)?|AAC)`)
	languageRE = regexp.MustCompile(`(?i)(zh[-_.]?(?:cn|hans|hant)|chs|cht|eng|jpn|kor|forced|sdh)`)
)

var videoExt = map[string]bool{".mkv": true, ".mp4": true, ".avi": true, ".ts": true, ".m2ts": true, ".mov": true, ".wmv": true, ".webm": true, ".iso": true}
var subtitleExt = map[string]bool{".srt": true, ".ass": true, ".ssa": true, ".sub": true, ".vtt": true, ".sup": true}

type libraryCategory struct {
	Name          string
	Subcategories []string
}

var libraryTaxonomy = []libraryCategory{
	{Name: "电影", Subcategories: []string{"欧美电影", "华语电影", "日韩电影"}},
	{Name: "电视剧", Subcategories: []string{"国产剧", "日韩剧", "欧美剧", "纪录片", "日番", "国漫"}},
}

type FileKind int

const (
	Unsupported FileKind = iota
	Video
	Subtitle
)

func classify(name string) FileKind {
	ext := strings.ToLower(filepath.Ext(name))
	if videoExt[ext] {
		return Video
	}
	if subtitleExt[ext] {
		return Subtitle
	}
	return Unsupported
}

func safeName(value string) string {
	replacer := strings.NewReplacer("/", " ", "\\", " ", ":", " -", "*", "", "?", "", "\"", "'", "<", "", ">", "", "|", "-")
	return strings.TrimSpace(strings.Join(strings.Fields(replacer.Replace(value)), " "))
}

func titleFolder(details *tmdb.Details) string {
	return fmt.Sprintf("%s (%d) [tmdbid-%d]", safeName(details.Title), details.Year, details.ID)
}

func mediaDirectories(details *tmdb.Details) (string, string) {
	if details.Kind == "movie" {
		switch {
		case isChinese(details):
			return "电影", "华语电影"
		case hasCountry(details, "JP", "KR") || hasLanguage(details, "ja", "ko"):
			return "电影", "日韩电影"
		default:
			if hasCountry(details, "US", "GB", "FR", "DE", "IT", "ES", "CA", "AU", "NZ", "RU") || hasLanguage(details, "en", "fr", "de", "es", "it", "ru") {
				return "电影", "欧美电影"
			}
			return "电影", ""
		}
	}
	if hasGenre(details, 99) {
		return "电视剧", "纪录片"
	}
	if hasGenre(details, 16) {
		switch {
		case hasCountry(details, "JP") || hasLanguage(details, "ja"):
			return "电视剧", "日番"
		case isChinese(details):
			return "电视剧", "国漫"
		}
	}
	switch {
	case isChinese(details):
		return "电视剧", "国产剧"
	case hasCountry(details, "JP", "KR") || hasLanguage(details, "ja", "ko"):
		return "电视剧", "日韩剧"
	default:
		if hasCountry(details, "US", "GB", "FR", "DE", "IT", "ES", "CA", "AU", "NZ", "RU") || hasLanguage(details, "en", "fr", "de", "es", "it", "ru") {
			return "电视剧", "欧美剧"
		}
		return "电视剧", ""
	}
}

func isChinese(details *tmdb.Details) bool {
	return hasCountry(details, "CN", "HK", "TW", "MO") || hasLanguage(details, "zh", "cn", "yue")
}

func hasCountry(details *tmdb.Details, countries ...string) bool {
	for _, actual := range details.OriginCountries {
		for _, expected := range countries {
			if strings.EqualFold(actual, expected) {
				return true
			}
		}
	}
	return false
}

func hasLanguage(details *tmdb.Details, languages ...string) bool {
	for _, language := range languages {
		if strings.EqualFold(details.OriginalLanguage, language) {
			return true
		}
	}
	return false
}

func hasGenre(details *tmdb.Details, genreID int) bool {
	for _, actual := range details.GenreIDs {
		if actual == genreID {
			return true
		}
	}
	return false
}

func technicalLabel(name string) string {
	seen := map[string]bool{}
	var labels []string
	for _, match := range qualityRE.FindAllString(name, -1) {
		label := strings.ReplaceAll(strings.ToUpper(match), ".", "")
		label = strings.ReplaceAll(label, " ", "-")
		if !seen[label] {
			seen[label] = true
			labels = append(labels, label)
		}
	}
	return strings.Join(labels, " ")
}

func movieName(details *tmdb.Details, original string) string {
	ext := strings.ToLower(filepath.Ext(original))
	base := fmt.Sprintf("%s (%d)", safeName(details.Title), details.Year)
	if label := technicalLabel(original); label != "" {
		base += " - " + label
	}
	return base + ext
}

type Episode struct{ Season, Start, End int }

func parseEpisode(name string) (Episode, bool) {
	match := episodeRE.FindStringSubmatch(name)
	if len(match) == 0 {
		return Episode{}, false
	}
	var ep Episode
	fmt.Sscanf(match[1], "%d", &ep.Season)
	fmt.Sscanf(match[2], "%d", &ep.Start)
	ep.End = ep.Start
	if match[3] != "" {
		fmt.Sscanf(match[3], "%d", &ep.End)
	}
	return ep, true
}

func episodeName(details *tmdb.Details, ep Episode, original string) string {
	ext := strings.ToLower(filepath.Ext(original))
	episode := fmt.Sprintf("S%02dE%02d", ep.Season, ep.Start)
	if ep.End != ep.Start {
		episode += fmt.Sprintf("-E%02d", ep.End)
	}
	base := safeName(details.Title) + " - " + episode
	if label := technicalLabel(original); label != "" {
		base += " - " + label
	}
	if classify(original) == Subtitle {
		if lang := languageRE.FindString(original); lang != "" {
			base += "." + strings.ReplaceAll(strings.ToLower(lang), "_", "-")
		}
	}
	return base + ext
}
