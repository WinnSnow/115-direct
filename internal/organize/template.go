package organize

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/local/115-direct/internal/store"
	"github.com/local/115-direct/internal/tmdb"
)

const DefaultMovieTemplate = `{{title}}{% if year %} ({{year}}){% endif %}/{{title}}{% if year %} ({{year}}){% endif %}{% if part %}-{{part}}{% endif %}{% if videoFormat %} - {{videoFormat}}{% endif %}{{fileExt}}`
const DefaultTVTemplate = `{{title}}{% if year %} ({{year}}){% endif %}/Season {{season}}/{{title}} - {{season_episode}}{% if part %}-{{part}}{% endif %}{% if episode %} - 第 {{episode}} 集{% endif %}{{fileExt}}`

type Options struct {
	MovieTemplate      string `json:"movie_template"`
	TVTemplate         string `json:"tv_template"`
	EpisodeRegex       string `json:"episode_regex"`
	Mode               string `json:"mode"`
	VersionPolicy      string `json:"version_policy"`
	TVVersionPolicy    string `json:"tv_version_policy"`
	MovieVersionPolicy string `json:"movie_version_policy"`
	DeletePolicy       string `json:"delete_policy"`
	RetirePolicy       string `json:"retire_policy"`
	ReceiveCleanupMode string `json:"receive_cleanup_mode"`
	ReceiveCleanupDays int    `json:"receive_cleanup_days"`
}

func DefaultOptions() Options {
	return Options{MovieTemplate: DefaultMovieTemplate, TVTemplate: DefaultTVTemplate, EpisodeRegex: episodeRE.String(), Mode: "copy", VersionPolicy: "coexist", TVVersionPolicy: "keep", MovieVersionPolicy: "quality", DeletePolicy: "output", RetirePolicy: "output", ReceiveCleanupMode: "disabled", ReceiveCleanupDays: 7}
}
func (o *Options) Normalize() {
	d := DefaultOptions()
	if o.MovieTemplate == "" {
		o.MovieTemplate = d.MovieTemplate
	}
	if o.TVTemplate == "" {
		o.TVTemplate = d.TVTemplate
	}
	if o.EpisodeRegex == "" {
		o.EpisodeRegex = d.EpisodeRegex
	}
	if o.Mode == "" {
		o.Mode = d.Mode
	}
	if o.VersionPolicy == "" {
		o.VersionPolicy = d.VersionPolicy
	}
	if o.TVVersionPolicy == "" {
		o.TVVersionPolicy = d.TVVersionPolicy
	}
	if o.MovieVersionPolicy == "" {
		o.MovieVersionPolicy = d.MovieVersionPolicy
	}
	if o.DeletePolicy == "" {
		o.DeletePolicy = d.DeletePolicy
	}
	if o.RetirePolicy == "" {
		o.RetirePolicy = d.RetirePolicy
	}
	if o.ReceiveCleanupMode == "" {
		o.ReceiveCleanupMode = d.ReceiveCleanupMode
	}
	if o.ReceiveCleanupDays <= 0 {
		o.ReceiveCleanupDays = d.ReceiveCleanupDays
	}
}
func validPolicy(p string) bool { return p == "output" || p == "local" || p == "chain" }
func validVersionPolicy(p string) bool {
	return p == "keep" || p == "coexist" || p == "overwrite" || p == "newest" || p == "quality"
}
func (o Options) Policy(kind string) string {
	o.Normalize()
	if kind == "tv" {
		return o.TVVersionPolicy
	}
	return o.MovieVersionPolicy
}
func (o Options) Validate() error {
	o.Normalize()
	if o.Mode != "copy" && o.Mode != "hardlink" && o.Mode != "symlink" && o.Mode != "move" {
		return fmt.Errorf("整理方式无效")
	}
	if !validVersionPolicy(o.TVVersionPolicy) || !validVersionPolicy(o.MovieVersionPolicy) {
		return fmt.Errorf("版本策略无效")
	}
	if !validPolicy(o.DeletePolicy) || !validPolicy(o.RetirePolicy) {
		return fmt.Errorf("删除策略无效")
	}
	if o.ReceiveCleanupMode != "disabled" && o.ReceiveCleanupMode != "after_days" {
		return fmt.Errorf("接收目录清理策略无效")
	}
	if o.ReceiveCleanupDays < 1 || o.ReceiveCleanupDays > 3650 {
		return fmt.Errorf("接收目录清理天数须为1至3650天")
	}
	r, err := regexp.Compile(o.EpisodeRegex)
	if err != nil || r.NumSubexp() < 2 {
		return fmt.Errorf("季集正则至少需要季号、集号两个捕获组")
	}
	v := map[string]string{"title": "Title", "year": "2024", "season": "01", "episode": "32", "season_episode": "S01E32", "part": "", "videoFormat": "1080P", "fileExt": ".strm"}
	if _, err := RenderTemplate(o.MovieTemplate, v); err != nil {
		return err
	}
	_, err = RenderTemplate(o.TVTemplate, v)
	return err
}

// This grammar only substitutes known fields and conditionals; it never evaluates expressions.
func RenderTemplate(template string, values map[string]string) (string, error) {
	known := map[string]bool{"title": true, "year": true, "season": true, "episode": true, "season_episode": true, "part": true, "videoFormat": true, "fileExt": true}
	if len(template) > 4096 {
		return "", fmt.Errorf("命名模板过长")
	}
	var out strings.Builder
	active := []bool{true}
	for len(template) > 0 {
		i := strings.Index(template, "{")
		if i < 0 {
			if active[len(active)-1] {
				out.WriteString(template)
			}
			break
		}
		if active[len(active)-1] {
			out.WriteString(template[:i])
		}
		template = template[i:]
		var end, token string
		if strings.HasPrefix(template, "{{") {
			end = "}}"
			token = "var"
		} else if strings.HasPrefix(template, "{%") {
			end = "%}"
			token = "if"
		} else {
			return "", fmt.Errorf("模板语法无效")
		}
		j := strings.Index(template[2:], end)
		if j < 0 {
			return "", fmt.Errorf("模板未闭合")
		}
		j += 2
		key := strings.TrimSpace(template[2:j])
		template = template[j+2:]
		if token == "var" {
			if !known[key] {
				return "", fmt.Errorf("模板变量无效: %s", key)
			}
			if active[len(active)-1] {
				if values[key] == "" && (key == "title" || key == "season" || key == "season_episode") {
					return "", fmt.Errorf("缺少命名字段: %s", key)
				}
				out.WriteString(values[key])
			}
		} else if key == "endif" {
			if len(active) == 1 {
				return "", fmt.Errorf("多余 endif")
			}
			active = active[:len(active)-1]
		} else if strings.HasPrefix(key, "if ") {
			field := strings.TrimSpace(strings.TrimPrefix(key, "if "))
			if !known[field] {
				return "", fmt.Errorf("条件字段无效")
			}
			active = append(active, active[len(active)-1] && values[field] != "")
			if len(active) > 16 {
				return "", fmt.Errorf("模板嵌套过深")
			}
		} else {
			return "", fmt.Errorf("只支持 if 和 endif")
		}
	}
	if len(active) != 1 {
		return "", fmt.Errorf("缺少 endif")
	}
	path := out.String()
	if strings.ContainsAny(path, `\:*?"<>|`) || strings.ContainsAny(path, "\x00\r\n\t") {
		return "", fmt.Errorf("命名含非法字符")
	}
	for _, part := range strings.Split(path, "/") {
		if part == "" || part == "." || part == ".." || len([]byte(part)) > 240 || strings.TrimRight(part, " .") != part {
			return "", fmt.Errorf("路径分段无效")
		}
	}
	if !filepath.IsLocal(filepath.FromSlash(path)) || !strings.HasSuffix(path, ".strm") {
		return "", fmt.Errorf("目标必须是根目录内的 .strm 路径")
	}
	return filepath.FromSlash(path), nil
}

var fpsRE = regexp.MustCompile(`(?i)\b(\d{2}(?:\.\d+)?)\s*(?:fps|帧)`)
var partRE = regexp.MustCompile(`(?i)\b(?:cd|part|disc)[ ._-]?(\d+)\b`)

func ParseQuality(name string) store.Quality {
	q := store.Quality{}
	u := strings.ToUpper(name)
	for _, r := range []struct {
		n      int
		tokens []string
	}{{2160, []string{"2160P", "4K", "UHD"}}, {1080, []string{"1080P", "1080I"}}, {720, []string{"720P"}}, {480, []string{"480P"}}} {
		for _, token := range r.tokens {
			if strings.Contains(u, token) {
				q.Resolution = r.n
				break
			}
		}
		if q.Resolution > 0 {
			break
		}
	}
	if m := fpsRE.FindStringSubmatch(name); len(m) > 0 {
		q.FPS = m[1]
	}
	for _, v := range []string{"DOVI", "DV", "HDR10+", "HDR10", "HDR"} {
		if strings.Contains(u, v) {
			q.HDR = v
			break
		}
	}
	if regexp.MustCompile(`(?i)dolby[ ._-]*vision`).MatchString(name) {
		q.HDR = "DV"
	}
	for _, v := range []string{"HEVC", "H.265", "X265", "AVC", "H.264", "X264", "AV1"} {
		if strings.Contains(u, v) {
			q.Codec = v
			break
		}
	}
	for _, v := range []string{"ATMOS", "TRUEHD", "DTS-HD", "DTS", "EAC3", "AC3", "AAC"} {
		if strings.Contains(u, v) {
			q.Audio = v
			break
		}
	}
	return q
}
func templateValues(d *tmdb.Details, name string, ep Episode) map[string]string {
	v := map[string]string{"title": safeName(d.Title), "fileExt": ".strm", "videoFormat": technicalLabel(name)}
	if d.Year > 0 {
		v["year"] = strconv.Itoa(d.Year)
	}
	if m := partRE.FindStringSubmatch(name); len(m) > 0 {
		v["part"] = "Part" + m[1]
	}
	if d.Kind == "tv" {
		v["season"] = fmt.Sprintf("%02d", ep.Season)
		v["episode"] = strconv.Itoa(ep.Start)
		v["season_episode"] = fmt.Sprintf("S%02dE%02d", ep.Season, ep.Start)
		if ep.End != ep.Start {
			v["season_episode"] += fmt.Sprintf("-E%02d", ep.End)
		}
	}
	return v
}

func ValidateRoots(cfg DirectoryConfig) error {
	if cfg.STRMPath == "" || cfg.PendingPath == "" {
		return fmt.Errorf("成品和待整理目录必须分别配置")
	}
	a, err := filepath.Abs(cfg.STRMPath)
	if err != nil {
		return err
	}
	b, err := filepath.Abs(cfg.PendingPath)
	if err != nil {
		return err
	}
	for _, p := range []string{a, b} {
		info, err := os.Stat(p)
		if err != nil || !info.IsDir() {
			return fmt.Errorf("目录挂载不可用: %s", p)
		}
	}
	a, err = filepath.EvalSymlinks(a)
	if err != nil {
		return err
	}
	b, err = filepath.EvalSymlinks(b)
	if err != nil {
		return err
	}
	if within(a, b) || within(b, a) {
		return fmt.Errorf("本地根目录不得相同或互相包含")
	}
	for _, p := range []string{a, b} {
		f, err := os.CreateTemp(p, ".115-write-test-*")
		if err != nil {
			return fmt.Errorf("目录不可写: %s", p)
		}
		name := f.Name()
		f.Close()
		os.Remove(name)
	}
	return nil
}
func within(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && (rel == "." || filepath.IsLocal(rel))
}
