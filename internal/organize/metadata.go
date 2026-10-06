package organize

import (
	"context"
	"database/sql"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/local/115-direct/internal/store"
	"github.com/local/115-direct/internal/tmdb"
)

type uniqueID struct {
	Type    string `xml:"type,attr"`
	Default string `xml:"default,attr,omitempty"`
	Value   int64  `xml:",chardata"`
}

type metadataNFO struct {
	XMLName       xml.Name
	Generator     string     `xml:"generator,omitempty"`
	Title         string     `xml:"title"`
	OriginalTitle string     `xml:"originaltitle,omitempty"`
	ShowTitle     string     `xml:"showtitle,omitempty"`
	Plot          string     `xml:"plot,omitempty"`
	Year          int        `xml:"year,omitempty"`
	Premiered     string     `xml:"premiered,omitempty"`
	Aired         string     `xml:"aired,omitempty"`
	Rating        float64    `xml:"rating,omitempty"`
	Runtime       int        `xml:"runtime,omitempty"`
	TMDBID        int64      `xml:"tmdbid,omitempty"`
	IDs           []uniqueID `xml:"uniqueid"`
	Genres        []string   `xml:"genre,omitempty"`
	Countries     []string   `xml:"country,omitempty"`
	Season        *int       `xml:"season,omitempty"`
	Episode       *int       `xml:"episode,omitempty"`
	Thumb         string     `xml:"thumb,omitempty"`
}

func writeAtomic(path string, data []byte) error {
	if existing, err := os.ReadFile(path); err == nil && string(existing) == string(data) {
		return nil
	}
	return writeAtomicReplacement(path, data)
}
func writeAtomicReplacement(path string, data []byte) error {
	file, err := os.CreateTemp(filepath.Dir(path), ".metadata-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if err = file.Chmod(0o640); err == nil {
		_, err = file.Write(data)
	}
	if err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(file.Name(), path)
}

func EnsureLocalDirectory(root, dir string) error {
	rel, err := filepath.Rel(root, dir)
	if err != nil || (rel != "." && !filepath.IsLocal(rel)) {
		return fmt.Errorf("本地路径超出 STRM 根目录")
	}
	if err := os.MkdirAll(root, 0o750); err != nil {
		return err
	}
	current, err := filepath.EvalSymlinks(root)
	if err != nil {
		return err
	}
	if rel == "." {
		return nil
	}
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if os.IsNotExist(err) {
			if err := os.Mkdir(current, 0o750); err != nil {
				return err
			}
			continue
		}
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return fmt.Errorf("本地目录包含符号链接或非目录项")
		}
	}
	return nil
}

func writeNFO(path string, value metadataNFO) error {
	if existing, err := os.ReadFile(path); err == nil {
		var nfo metadataNFO
		if xml.Unmarshal(existing, &nfo) != nil || nfo.Generator != "115 Direct" {
			return nil
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	value.Generator = "115 Direct"
	raw, err := xml.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return writeAtomic(path, append([]byte(xml.Header), append(raw, '\n')...))
}

func removeGeneratedNFO(path string) {
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var nfo metadataNFO
	if xml.Unmarshal(data, &nfo) == nil && nfo.Generator == "115 Direct" {
		_ = os.Remove(path)
	}
}

func removeEmptyLocalParents(path, root string) {
	root = filepath.Clean(root)
	for path = filepath.Clean(path); path != root; path = filepath.Dir(path) {
		rel, err := filepath.Rel(root, path)
		if err != nil || !filepath.IsLocal(rel) {
			return
		}
		if err := os.Remove(path); err != nil {
			return
		}
	}
}

func (s *Service) artwork(ctx context.Context, root, dir, name, remote string, owners []string) {
	if remote == "" {
		return
	}
	body, _ := json.Marshal(imageStep{root, dir, name, remote, owners})
	id := "image:" + store.ContentHash(body)
	e, err := s.Store.Execution(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		e = &store.Execution{ID: id, Kind: "image", Status: "planned", Body: body}
		err = s.Store.PutExecution(ctx, *e)
	}
	if err == nil {
		err = s.resumeImage(ctx, e)
	}
	if err != nil {
		s.Store.Audit(ctx, "scrape", fmt.Sprintf("图片下载失败 %s/%s: %v", filepath.Base(dir), name, err))
	}
}

func (s *Service) writeMetadata(ctx context.Context, cfg DirectoryConfig, details *tmdb.Details, media []store.MediaEntry) error {
	if len(media) == 0 {
		return nil
	}
	root := first(cfg.STRMPath, s.Defaults.STRMPath)
	owners := []string{}
	for _, m := range media {
		if m.RemoteID != "" {
			owners = append(owners, m.RemoteID)
		}
	}
	writeNFO := func(path string, nfo metadataNFO) error {
		assigned := owners
		for _, m := range media {
			if path == strings.TrimSuffix(m.STRMPath, filepath.Ext(m.STRMPath))+".nfo" && m.RemoteID != "" {
				assigned = []string{m.RemoteID}
				break
			}
		}
		return s.writeOwnedNFO(ctx, root, path, nfo, assigned)
	}
	if root == "" {
		return fmt.Errorf("STRM 目录尚未配置")
	}
	category, subcategory := s.mediaDirectories(ctx, details)
	dir := filepath.Join(root, category, subcategory, titleFolder(details))
	if len(media) > 0 {
		// Backfills use the actual existing title directory, not a newly guessed name.
		path := media[0].STRMPath
		if path == "" {
			path = filepath.Join(root, strings.TrimSuffix(media[0].RemotePath, filepath.Ext(media[0].RemotePath))+".strm")
		}
		dir = filepath.Dir(path)
		if details.Kind == "tv" {
			dir = filepath.Dir(dir)
		}
		if media[0].RemoteID != "" {
			link, err := s.Store.MediaLink(ctx, media[0].RemoteID)
			if err == nil && link.TitlePath != "" {
				dir = link.TitlePath
			} else if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return err
			}
		}
	}
	if err := validateMetadataDir(root, dir); err != nil {
		return err
	}
	nfo := metadataNFO{XMLName: xml.Name{Local: "movie"}, Title: details.Title, OriginalTitle: details.OriginalTitle,
		Plot: details.Overview, Year: details.Year, Premiered: details.Date, Rating: details.Rating, Runtime: details.Runtime,
		TMDBID: details.ID, IDs: []uniqueID{{Type: "tmdb", Default: "true", Value: details.ID}}, Genres: details.Genres, Countries: details.OriginCountries}
	if details.Kind == "tv" {
		nfo.XMLName.Local = "tvshow"
	}
	name := "movie.nfo"
	if details.Kind == "tv" {
		name = "tvshow.nfo"
	}
	if err := writeNFO(filepath.Join(dir, name), nfo); err != nil {
		return err
	}
	s.artwork(ctx, root, dir, "poster.jpg", details.PosterPath, owners)
	s.artwork(ctx, root, dir, "fanart.jpg", details.BackdropPath, owners)
	seasons := map[int]*tmdb.SeasonDetails{}
	for _, item := range media {
		path := item.STRMPath
		if path == "" {
			path = filepath.Join(root, strings.TrimSuffix(item.RemotePath, filepath.Ext(item.RemotePath))+".strm")
		}
		if err := validateMetadataDir(root, filepath.Dir(path)); err != nil {
			return err
		}
		if details.Kind == "movie" {
			if err := writeNFO(strings.TrimSuffix(path, filepath.Ext(path))+".nfo", nfo); err != nil {
				return err
			}
			continue
		}
		ep, ok := parseEpisode(filepath.Base(path))
		if item.RemoteID != "" {
			link, err := s.Store.MediaLink(ctx, item.RemoteID)
			if err == nil && link.Kind == "tv" && link.TMDBID == details.ID {
				ep = Episode{link.Season, link.Episode, link.EpisodeEnd}
				ok = link.Episode > 0
			} else if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return err
			}
		}
		if !ok {
			continue
		}
		season, exists := seasons[ep.Season]
		if !exists {
			var err error
			season, err = s.TMDB.Season(ctx, details.ID, ep.Season)
			if err != nil {
				return fmt.Errorf("刮削第 %d 季: %w", ep.Season, err)
			}
			seasons[ep.Season] = season
			seasonNFO := metadataNFO{XMLName: xml.Name{Local: "season"}, Title: season.Title, Plot: season.Overview, Premiered: season.Date, Season: &ep.Season}
			if season.ID > 0 {
				seasonNFO.IDs = []uniqueID{{Type: "tmdb", Default: "true", Value: season.ID}}
			}
			if err := writeNFO(filepath.Join(filepath.Dir(path), "season.nfo"), seasonNFO); err != nil {
				return err
			}
			s.artwork(ctx, root, filepath.Dir(path), "poster.jpg", season.PosterPath, owners)
		}
		found := false
		for _, info := range season.Episodes {
			if info.Number != ep.Start {
				continue
			}
			found = true
			episodeNFO := metadataNFO{XMLName: xml.Name{Local: "episodedetails"}, Title: info.Title, ShowTitle: details.Title,
				Plot: info.Overview, Aired: info.Date, Rating: info.Rating, Season: &ep.Season, Episode: &ep.Start,
				IDs: []uniqueID{{Type: "tmdb", Default: "true", Value: info.ID}}}
			if err := writeNFO(strings.TrimSuffix(path, filepath.Ext(path))+".nfo", episodeNFO); err != nil {
				return err
			}
			break
		}
		if !found {
			// Unreleased episodes must not inherit metadata from a different episode.
			fallback := metadataNFO{XMLName: xml.Name{Local: "episodedetails"}, Title: fmt.Sprintf("第 %d 集", ep.Start), ShowTitle: details.Title, Season: &ep.Season, Episode: &ep.Start}
			if err := writeNFO(strings.TrimSuffix(path, filepath.Ext(path))+".nfo", fallback); err != nil {
				return err
			}
			s.Store.Audit(ctx, "scrape", fmt.Sprintf("TMDB 尚无剧集信息 %s S%02dE%02d", details.Title, ep.Season, ep.Start))
		}
	}
	s.Store.Audit(ctx, "scrape", fmt.Sprintf("刮削完成 %s (%s/%d)，%d 个文件", details.Title, details.Kind, details.ID, len(media)))
	return nil
}

func validateMetadataDir(root, dir string) error {
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return err
	}
	realDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return err
	}
	rel, err := filepath.Rel(realRoot, realDir)
	if err != nil || (rel != "." && !filepath.IsLocal(rel)) {
		return fmt.Errorf("刮削路径超出 STRM 根目录")
	}
	return nil
}

type ScrapeResult struct {
	Titles  int `json:"titles"`
	Files   int `json:"files"`
	Skipped int `json:"skipped"`
}

// ScrapeLibrary backfills local metadata serially without accessing 115 or obtaining CDN links.
func (s *Service) ScrapeLibrary(ctx context.Context) (*ScrapeResult, error) {
	s.operationMu.Lock()
	defer s.operationMu.Unlock()
	return s.scrapeLibrary(ctx)
}

func (s *Service) scrapeLibrary(ctx context.Context) (*ScrapeResult, error) {
	if s.Directories(ctx).PendingPath != "" {
		cfg := s.Directories(ctx)
		if err := CheckRoots(ctx, s.Store, cfg); err != nil {
			return nil, err
		}
		links, err := s.Store.ListLinks(ctx)
		if err != nil {
			return nil, err
		}
		groups := map[string][]store.MediaEntry{}
		identities := map[string]store.MediaLink{}
		for _, l := range links {
			if l.Deleted != "" || l.OutputPath == "" || l.TMDBID == 0 {
				continue
			}
			if _, err := os.Stat(l.OutputPath); err != nil {
				continue
			}
			m, err := s.Store.GetMediaByRemoteID(ctx, l.RemoteID)
			if err != nil {
				return nil, err
			}
			key := fmt.Sprintf("%s:%d:%s", l.Kind, l.TMDBID, filepath.Dir(l.OutputPath))
			groups[key] = append(groups[key], *m)
			identities[key] = l
		}
		result := &ScrapeResult{}
		for key, items := range groups {
			l := identities[key]
			details, err := s.TMDB.Details(ctx, l.Kind, l.TMDBID)
			if err != nil {
				return nil, err
			}
			if err := s.writeMetadata(ctx, cfg, details, items); err != nil {
				return nil, err
			}
			result.Titles++
			result.Files += len(items)
		}
		return result, nil
	}
	var cfg DirectoryConfig
	if err := s.Store.GetSetting(ctx, "directories", &cfg); err != nil {
		cfg = s.Defaults
	}
	root := first(cfg.STRMPath, s.Defaults.STRMPath)
	groups := map[string][]store.MediaEntry{}
	result := &ScrapeResult{}
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if entry.IsDir() || entry.Type()&os.ModeSymlink != 0 || !strings.EqualFold(filepath.Ext(path), ".strm") {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		parts := strings.Split(filepath.ToSlash(rel), "/")
		if len(parts) < 4 || (parts[0] != "电影" && parts[0] != "电视剧") || tmdb.ExtractHint(parts[2]).TMDBID == 0 {
			result.Skipped++
			return nil
		}
		key := filepath.Join(parts[0], parts[1], parts[2])
		groups[key] = append(groups[key], store.MediaEntry{Name: entry.Name(), STRMPath: path})
		return nil
	})
	if err != nil {
		return result, err
	}
	keys := make([]string, 0, len(groups))
	for key := range groups {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		kind := "movie"
		if strings.Split(filepath.ToSlash(key), "/")[0] == "电视剧" {
			kind = "tv"
		}
		id := tmdb.ExtractHint(filepath.Base(key)).TMDBID
		details, err := s.TMDB.Details(ctx, kind, id)
		if err != nil {
			return result, fmt.Errorf("刮削 %s: %w", key, err)
		}
		if details.ID != id {
			return result, fmt.Errorf("TMDB 返回条目不匹配 %s", strconv.FormatInt(id, 10))
		}
		if err := s.writeMetadata(ctx, cfg, details, groups[key]); err != nil {
			return result, err
		}
		result.Titles++
		result.Files += len(groups[key])
	}
	return result, nil
}
