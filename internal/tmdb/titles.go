package tmdb

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"unicode"
)

func chineseTitle(value string) bool {
	hasHan := false
	for _, r := range value {
		if unicode.Is(unicode.Hiragana, r) || unicode.Is(unicode.Katakana, r) {
			return false
		}
		hasHan = hasHan || unicode.Is(unicode.Han, r)
	}
	return hasHan
}

type translatedTitle struct {
	Language string `json:"iso_639_1"`
	Country  string `json:"iso_3166_1"`
	Data     struct {
		Title string `json:"title"`
		Name  string `json:"name"`
	} `json:"data"`
}
type alternativeTitle struct {
	Country string `json:"iso_3166_1"`
	Title   string `json:"title"`
}

// zh-CN details may fall back to the original name even when a confirmed Chinese
// alias exists. Query aliases/translations only for those titles; the extra query
// has its own cache key so old English details caches need not be discarded.
func (c *Client) preferChineseTitle(ctx context.Context, kind string, id int64, title string) string {
	title = strings.TrimSpace(title)
	if chineseTitle(title) {
		return title
	}
	path := fmt.Sprintf("/%s/%d", kind, id)
	params := url.Values{"language": {"zh-CN"}, "append_to_response": {"translations,alternative_titles"}}
	var response struct {
		ID           int64  `json:"id"`
		Title        string `json:"title"`
		Name         string `json:"name"`
		Translations struct {
			Items []translatedTitle `json:"translations"`
		} `json:"translations"`
		Alternatives struct {
			Results []alternativeTitle `json:"results"`
			Titles  []alternativeTitle `json:"titles"`
		} `json:"alternative_titles"`
	}
	err := c.get(ctx, path, params, &response)
	if err != nil || response.ID != id {
		if c.Cache != nil {
			if err == nil {
				_ = c.Cache.DeleteCached(ctx, c.cacheKey(path, params))
			}
			c.Cache.Log(ctx, "recognition", "warning", fmt.Sprintf("TMDB %s 中文别名查询失败，暂用主标题 %s", path, title))
		}
		return title
	}
	primary := response.Title
	if kind == "tv" {
		primary = response.Name
	}
	if chineseTitle(primary) {
		return c.recordChineseTitle(ctx, path, title, primary)
	}
	aliases := append(response.Alternatives.Results, response.Alternatives.Titles...)
	// Mainland Chinese names take precedence over Taiwan/Hong Kong translations.
	for _, country := range []string{"CN", "SG", "TW", "HK"} {
		for _, t := range response.Translations.Items {
			if t.Language != "zh" || t.Country != country {
				continue
			}
			name := t.Data.Title
			if kind == "tv" {
				name = t.Data.Name
			}
			if chineseTitle(name) {
				return c.recordChineseTitle(ctx, path, title, name)
			}
		}
		for _, a := range aliases {
			if a.Country == country && chineseTitle(a.Title) {
				return c.recordChineseTitle(ctx, path, title, a.Title)
			}
		}
	}
	if c.Cache != nil {
		c.Cache.Log(ctx, "recognition", "warning", fmt.Sprintf("TMDB %s 尚无中文标题或中文别名，暂用主标题 %s", path, title))
	}
	return title
}

func (c *Client) recordChineseTitle(ctx context.Context, path, original, localized string) string {
	localized = strings.TrimSpace(localized)
	if c.Cache != nil {
		c.Cache.Log(ctx, "recognition", "info", fmt.Sprintf("TMDB %s 使用中文译名：%s -> %s", path, original, localized))
	}
	return localized
}
