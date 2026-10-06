package tmdb

import (
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"
)

var (
	yearPattern    = regexp.MustCompile(`(?:^|[^0-9])((?:19|20)\d{2})(?:[^0-9]|$)`)
	episodePattern = regexp.MustCompile(`(?i)(?:S\d{1,2}E\d{1,3}|Season[ ._-]*\d{1,2}|第\s*\d+\s*季)`)
	// Match release metadata by shape instead of enumerating every spelling.
	// For example, [hx][ ._-]*\d{3,4} covers H264, H.264, H-264,
	// H265, H.265, X264 and future H/X codec revisions without adding
	// one literal for each variant.
	releaseTokens  = regexp.MustCompile(`(?i)\b(?:\d{3,4}p|[1-8]k|uhd|bluray|blu[ ._-]?ray|web[ ._-]?dl|webrip|hdtv|remux|[hx][ ._-]*\d{3,4}|hevc|avc|av\d+|vp\d+|vvc|hdr\d*|dolby[ ._-]?vision|dv|dts|e?ac3|ddp|aac|atmos|truehd|flac|opus|mp3|proper|repack|10bit|8bit)\b`)
	audioChannels  = regexp.MustCompile(`(?i)\b\d+\.\d+\b`)
	mediaExtension = regexp.MustCompile(`(?i)\.(?:mkv|mp4|avi|mov|wmv|ts|m2ts|webm|strm)$`)
	tmdbIDPattern  = regexp.MustCompile(`(?i)(?:[\[{(]\s*)?tmdb(?:id)?\s*(?:=|:|-)\s*(\d+)(?:\s*[\]})])?`)
)

type Hint struct {
	Title  string
	Year   int
	Kind   string
	TMDBID int64
}

func ExtractHint(values ...string) Hint {
	joined := strings.Join(values, " ")
	hint := Hint{}
	if match := yearPattern.FindStringSubmatch(joined); len(match) == 2 {
		hint.Year, _ = strconv.Atoi(match[1])
	}
	if episodePattern.MatchString(joined) {
		hint.Kind = "tv"
	}
	if match := tmdbIDPattern.FindStringSubmatch(joined); len(match) == 2 {
		hint.TMDBID, _ = strconv.ParseInt(match[1], 10, 64)
	}
	if len(values) == 0 {
		return hint
	}
	clean := mediaExtension.ReplaceAllString(values[0], " ")
	clean = trimMetadataSuffix(clean)
	clean = releaseTokens.ReplaceAllString(clean, " ")
	clean = audioChannels.ReplaceAllString(clean, " ")
	clean = episodePattern.ReplaceAllString(clean, " ")
	clean = tmdbIDPattern.ReplaceAllString(clean, " ")
	clean = yearPattern.ReplaceAllString(clean, " ")
	clean = strings.TrimSpace(regexp.MustCompile(`[\[\](){}._-]+`).ReplaceAllString(clean, " "))
	clean = strings.Join(strings.Fields(clean), " ")
	hint.Title = clean
	return hint
}

// trimMetadataSuffix keeps the title portion before the first structural
// release marker. This prevents an unknown future codec or release tag after
// the year/episode from affecting the TMDB query; known token patterns remain
// as a fallback for names without a year or episode marker.
func trimMetadataSuffix(value string) string {
	cut := len(value)
	for _, pattern := range []*regexp.Regexp{yearPattern, episodePattern, tmdbIDPattern} {
		for _, index := range pattern.FindAllStringIndex(value, -1) {
			if index[0] > 0 && index[0] < cut {
				cut = index[0]
			}
		}
	}
	if cut == len(value) {
		if index := releaseTokens.FindStringIndex(value); index != nil && index[0] > 0 {
			cut = index[0]
		}
	}
	return value[:cut]
}

func Rank(hint Hint, candidates []Candidate) []Candidate {
	for i := range candidates {
		titleScore := math.Max(similarity(normalize(hint.Title), normalize(candidates[i].Title)), similarity(normalize(hint.Title), normalize(candidates[i].OriginalTitle))) * 55
		yearScore := 0.0
		if hint.Year != 0 && candidates[i].Year == hint.Year {
			yearScore = 25
		} else if hint.Year != 0 && abs(hint.Year-candidates[i].Year) == 1 {
			yearScore = 12
		} else if hint.Year == 0 {
			yearScore = 8
		}
		kindScore := 5.0
		if hint.Kind != "" && candidates[i].Kind == hint.Kind {
			kindScore = 15
		} else if hint.Kind != "" && candidates[i].Kind != hint.Kind {
			kindScore = 0
		}
		consistency := 5.0
		candidates[i].Score = math.Round((titleScore+yearScore+kindScore+consistency)*10) / 10
		candidates[i].Reasons = []string{fmt.Sprintf("片名相似 %.1f/55", titleScore), fmt.Sprintf("年份 %d→%d %.0f/25", hint.Year, candidates[i].Year, yearScore), fmt.Sprintf("类型 %s→%s %.0f/15", hint.Kind, candidates[i].Kind, kindScore), "基础一致性 5/5"}
	}
	sort.SliceStable(candidates, func(i, j int) bool { return candidates[i].Score > candidates[j].Score })
	return candidates
}

func AutoMatch(candidates []Candidate) (Candidate, bool) {
	if len(candidates) == 0 || candidates[0].Score < 85 {
		return Candidate{}, false
	}
	if len(candidates) > 1 && candidates[0].Score-candidates[1].Score < 10 {
		return Candidate{}, false
	}
	return candidates[0], true
}

func normalize(value string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(value) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func similarity(a, b string) float64 {
	if a == "" || b == "" {
		return 0
	}
	if a == b {
		return 1
	}
	ar, br := []rune(a), []rune(b)
	row := make([]int, len(br)+1)
	for j := range row {
		row[j] = j
	}
	for i, ca := range ar {
		prev := row[0]
		row[0] = i + 1
		for j, cb := range br {
			old := row[j+1]
			cost := 1
			if ca == cb {
				cost = 0
			}
			row[j+1] = min(row[j+1]+1, row[j]+1, prev+cost)
			prev = old
		}
	}
	distance := row[len(br)]
	return 1 - float64(distance)/float64(max(len(ar), len(br)))
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}
