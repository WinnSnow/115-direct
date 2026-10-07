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
	episodePattern = regexp.MustCompile(`(?i)(?:S\d{1,2}E\d{1,3}(?:[-~]E?\d{1,3})?|Season[ ._-]*\d{1,2}|第\s*\d+\s*[季集])`)
	// A bare season marker is common in complete season releases, e.g.
	// "HeXi.Corridor.S01.2015". It must be treated as structure rather than
	// part of the title, while the boundary prevents matching the S01 prefix
	// inside S01E01.
	seasonOnlyPattern = regexp.MustCompile(`(?i)(?:^|[^A-Za-z0-9])S\d{1,2}(?:[^A-Za-z0-9]|$)`)
	// Match release metadata by shape instead of enumerating every spelling.
	// For example, [hx][ ._-]*\d{3,4} covers H264, H.264, H-264,
	// H265, H.265, X264 and future H/X codec revisions without adding
	// one literal for each variant.
	releaseTokens  = regexp.MustCompile(`(?i)\b(?:\d{3,4}p|[1-8]k|uhd|bluray|blu[ ._-]?ray|web[ ._-]?dl|webrip|hdtv|remux|[hx][ ._-]*\d{3,4}|hevc|avc|av\d+|vp\d+|vvc|hdr\d*|dolby[ ._-]?vision|dv|dts|e?ac3|ddp|aac|atmos|truehd|flac|opus|mp3|proper|repack|10bit|8bit)\b`)
	audioChannels  = regexp.MustCompile(`(?i)\b\d+\.\d+\b`)
	mediaExtension = regexp.MustCompile(`(?i)\.(?:mkv|mp4|avi|mov|wmv|ts|m2ts|webm|strm)$`)
	tmdbIDPattern  = regexp.MustCompile(`(?i)(?:[\[{(]\s*)?tmdb(?:id)?\s*(?:=|:|-)\s*(\d+)(?:\s*[\]})])?`)
	bracketGroup   = regexp.MustCompile(`[\[【(（{][^\]】)）}]{1,120}[\]】)）}]`)
	// Go's regexp engine does not support look-around. The pattern consumes the
	// domain itself; punctuation is normalized after this pass.
	domainPattern    = regexp.MustCompile(`(?i)(?:https?://|www\.)[^\s\[\]【】()（）{}<>]+|(?:^|[\s._\-\[\]【】()（）{}])[a-z][a-z-]*(?:\.[a-z][a-z0-9-]*)*\.(?:com|net|org|cc|cn|tv|vip|top|me|io)(?:$|[\s\[\]【】()（）{}<>])`)
	adKeywordPattern = regexp.MustCompile(`(?i)(?:高码|低码|全\s*\d+\s*集|国语|国語|中字|中文字幕|配音|字幕|无水印|完整版|合集|收藏版|发布页|交流群|字幕组|压制组|免费|下载|影视|资源|网站|网盘|发布|官网|网)`)
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
	if episodePattern.MatchString(joined) || seasonOnlyPattern.MatchString(joined) {
		hint.Kind = "tv"
	}
	if match := tmdbIDPattern.FindStringSubmatch(joined); len(match) == 2 {
		hint.TMDBID, _ = strconv.ParseInt(match[1], 10, 64)
	}
	if len(values) == 0 {
		return hint
	}
	clean := mediaExtension.ReplaceAllString(values[0], " ")
	clean = stripFilenameNoise(clean)
	clean = trimMetadataSuffix(clean)
	clean = releaseTokens.ReplaceAllString(clean, " ")
	clean = audioChannels.ReplaceAllString(clean, " ")
	clean = episodePattern.ReplaceAllString(clean, " ")
	clean = seasonOnlyPattern.ReplaceAllString(clean, " ")
	clean = tmdbIDPattern.ReplaceAllString(clean, " ")
	clean = yearPattern.ReplaceAllString(clean, " ")
	clean = strings.TrimSpace(regexp.MustCompile(`[\[\](){}._-]+`).ReplaceAllString(clean, " "))
	clean = strings.Join(strings.Fields(clean), " ")
	hint.Title = simplifyBilingualTitle(clean)
	return hint
}

// stripFilenameNoise removes transport and release noise before matching. A
// bracketed segment is removed only when it contains a known metadata/ad
// marker, so title brackets such as "[一]" remain valid title text.
func stripFilenameNoise(value string) string {
	value = domainPattern.ReplaceAllString(value, " ")
	value = bracketGroup.ReplaceAllStringFunc(value, func(segment string) string {
		if adKeywordPattern.MatchString(segment) || releaseTokens.MatchString(segment) {
			return " "
		}
		return segment
	})
	return value
}

// Many Chinese releases contain both the localized title and a transliterated
// alias before the season marker. When the title starts with Han characters and
// a later token contains Latin letters, the localized portion is the safer
// TMDB query (for example "河西走廊 HeXi Corridor" -> "河西走廊"). Numeric
// tokens between the localized title and alias belong to the title, so
// "流浪地球 2 The Wandering Earth" remains "流浪地球 2".
func simplifyBilingualTitle(value string) string {
	parts := strings.Fields(value)
	if len(parts) < 2 || !containsHan(parts[0]) {
		return value
	}
	firstLatin := -1
	for i, part := range parts[1:] {
		if !asciiTitleToken(part) {
			return value
		}
		if containsLatin(part) {
			firstLatin = i + 1
			break
		}
	}
	if firstLatin < 0 {
		return value
	}
	return strings.Join(parts[:firstLatin], " ")
}

func containsHan(value string) bool {
	for _, r := range value {
		if unicode.Is(unicode.Han, r) {
			return true
		}
	}
	return false
}

func asciiTitleToken(value string) bool {
	if value == "" {
		return false
	}
	for _, r := range value {
		if (r < 'A' || r > 'Z') && (r < 'a' || r > 'z') && (r < '0' || r > '9') {
			return false
		}
	}
	return true
}

func containsLatin(value string) bool {
	for _, r := range value {
		if (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') {
			return true
		}
	}
	return false
}

// trimMetadataSuffix keeps the title portion before the first structural
// release marker. This prevents an unknown future codec or release tag after
// the year/episode from affecting the TMDB query; known token patterns remain
// as a fallback for names without a year or episode marker.
func trimMetadataSuffix(value string) string {
	cut := len(value)
	for _, pattern := range []*regexp.Regexp{yearPattern, episodePattern, seasonOnlyPattern, tmdbIDPattern} {
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
