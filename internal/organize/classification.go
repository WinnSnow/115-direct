package organize

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"reflect"
	"strings"

	"github.com/local/115-direct/internal/tmdb"
)

type ClassificationRule struct {
	Name      string   `json:"name"`
	Enabled   bool     `json:"enabled"`
	Kind      string   `json:"kind"`
	Countries []string `json:"countries"`
	Languages []string `json:"languages"`
	Genres    []int    `json:"genres"`
	Keyword   string   `json:"keyword"`
	Target    string   `json:"target"`
}
type ClassificationConfig struct {
	MovieRoot string               `json:"movie_root"`
	TVRoot    string               `json:"tv_root"`
	Rules     []ClassificationRule `json:"rules"`
}

func DefaultClassification() ClassificationConfig {
	region := func(name, kind, target string, countries, languages []string, genres ...int) ClassificationRule {
		return ClassificationRule{Name: name, Enabled: true, Kind: kind, Target: target, Countries: countries, Languages: languages, Genres: genres}
	}
	cn := []string{"CN", "HK", "TW", "MO"}
	zh := []string{"zh", "cn", "yue"}
	western := []string{"US", "GB", "FR", "DE", "IT", "ES", "CA", "AU", "NZ", "RU"}
	westernLang := []string{"en", "fr", "de", "es", "it", "ru"}
	return ClassificationConfig{MovieRoot: "电影", TVRoot: "电视剧", Rules: []ClassificationRule{
		region("动画电影", "movie", "动画电影", nil, nil, 16),
		region("华语电影", "movie", "华语电影", cn, zh), region("日韩电影", "movie", "日韩电影", []string{"JP", "KR"}, []string{"ja", "ko"}), region("欧美电影", "movie", "欧美电影", western, westernLang),
		region("纪录片", "tv", "纪录片", nil, nil, 99), region("日番", "tv", "日番", []string{"JP"}, []string{"ja"}, 16), region("国漫", "tv", "国漫", cn, zh, 16),
		region("国产剧", "tv", "国产剧", cn, zh), region("日韩剧", "tv", "日韩剧", []string{"JP", "KR"}, []string{"ja", "ko"}), region("欧美剧", "tv", "欧美剧", western, westernLang),
	}}
}

// Only extend the unmodified legacy defaults. Explicit user rule order remains
// authoritative, including intentionally disabled or renamed animation rules.
func upgradeDefaultClassification(c ClassificationConfig) ClassificationConfig {
	d := DefaultClassification()
	if reflect.DeepEqual(c.Rules, d.Rules[1:]) {
		c.Rules = append([]ClassificationRule{d.Rules[0]}, c.Rules...)
	}
	return c
}
func (s *Service) migrateClassification(ctx context.Context) error {
	var c ClassificationConfig
	if err := s.Store.GetSetting(ctx, "classification", &c); errors.Is(err, sql.ErrNoRows) {
		return nil
	} else if err != nil {
		return err
	}
	updated := upgradeDefaultClassification(c)
	if len(updated.Rules) != len(c.Rules) {
		return s.Store.PutSetting(ctx, "classification", updated)
	}
	return nil
}
func (c ClassificationConfig) Validate() error {
	if !validCloudName(c.MovieRoot) || !validCloudName(c.TVRoot) || c.MovieRoot == c.TVRoot {
		return fmt.Errorf("电影与电视剧一级分类名称须合法且不同")
	}
	if len(c.Rules) == 0 || len(c.Rules) > 100 {
		return fmt.Errorf("分类规则须为1至100条")
	}
	for _, r := range c.Rules {
		if strings.TrimSpace(r.Name) == "" || len(r.Name) > 120 || !validCloudName(r.Target) || (r.Kind != "movie" && r.Kind != "tv") {
			return fmt.Errorf("分类规则名称、类型或目标无效")
		}
		for _, country := range r.Countries {
			if len(country) != 2 || strings.TrimSpace(country) != country {
				return fmt.Errorf("国家代码使用两位代码，例如 CN、JP")
			}
		}
		for _, language := range r.Languages {
			if len(language) < 2 || len(language) > 8 || strings.TrimSpace(language) != language {
				return fmt.Errorf("语言代码无效")
			}
		}
		for _, genre := range r.Genres {
			if genre < 1 {
				return fmt.Errorf("类型ID必须为正整数")
			}
		}
		if len(r.Keyword) > 200 {
			return fmt.Errorf("关键词过长")
		}
	}
	return nil
}
func (s *Service) Classification(ctx context.Context) ClassificationConfig {
	c := DefaultClassification()
	_ = s.Store.GetSetting(ctx, "classification", &c)
	return upgradeDefaultClassification(c)
}
func (c ClassificationConfig) Match(d *tmdb.Details) (string, string, string) {
	root := c.MovieRoot
	if d.Kind == "tv" {
		root = c.TVRoot
	}
	for _, r := range c.Rules {
		if !r.Enabled || r.Kind != d.Kind {
			continue
		}
		if len(r.Countries)+len(r.Languages) > 0 && !hasCountry(d, r.Countries...) && !hasLanguage(d, r.Languages...) {
			continue
		}
		matched := true
		for _, g := range r.Genres {
			if !hasGenre(d, g) {
				matched = false
				break
			}
		}
		if !matched {
			continue
		}
		if r.Keyword != "" && !strings.Contains(strings.ToLower(d.Title+" "+d.OriginalTitle), strings.ToLower(r.Keyword)) {
			continue
		}
		return root, r.Target, r.Name
	}
	return root, "", "未匹配分类规则"
}
func (s *Service) mediaDirectories(ctx context.Context, d *tmdb.Details) (string, string) {
	root, sub, _ := s.Classification(ctx).Match(d)
	return root, sub
}
func (s *Service) taxonomy(ctx context.Context) []libraryCategory {
	c := s.Classification(ctx)
	out := []libraryCategory{{Name: c.MovieRoot}, {Name: c.TVRoot}}
	seen := map[string]bool{}
	for _, r := range c.Rules {
		if !r.Enabled {
			continue
		}
		i := 0
		if r.Kind == "tv" {
			i = 1
		}
		key := r.Kind + ":" + r.Target
		if !seen[key] {
			out[i].Subcategories = append(out[i].Subcategories, r.Target)
			seen[key] = true
		}
	}
	return out
}
