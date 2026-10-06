package organize

import (
	"context"
	"database/sql"
	"encoding/xml"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/local/115-direct/internal/store"
	"github.com/local/115-direct/internal/tmdb"
)

// The compatibility migration only adds associations. Existing files are never moved.
func (s *Service) MigrateLinks(ctx context.Context) error {
	c := s.Directories(ctx)
	if c.PendingPath == "" {
		return nil
	}
	media, err := s.Store.ListMedia(ctx)
	if err != nil {
		return err
	}
	for _, m := range media {
		if _, err := s.Store.MediaLink(ctx, m.RemoteID); err == nil {
			continue
		} else if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		l := store.MediaLink{RemoteID: m.RemoteID, IngestedAt: m.UpdatedAt, Quality: ParseQuality(m.Name), Unavailable: m.Missing > 0, Mode: "copy", SourcePath: filepath.Join(c.PendingPath, "legacy", stableSuffix(m.RemoteID)+".strm")}
		if within(c.STRMPath, m.STRMPath) {
			l.OutputPath = m.STRMPath
			l.Manual = true
		} else if within(c.PendingPath, m.STRMPath) {
			l.SourcePath = m.STRMPath
		} else {
			return fmt.Errorf("旧STRM映射超出配置目录，需核对: %s", m.ID)
		}
		hint := tmdb.ExtractHint(filepath.Dir(m.STRMPath))
		l.TMDBID = hint.TMDBID
		l.Kind = "movie"
		ep, ok := parseEpisode(m.Name)
		if ok || strings.Contains(m.STRMPath, "电视剧") {
			l.Kind = "tv"
		}
		l.Season = ep.Season
		l.Episode = ep.Start
		l.EpisodeEnd = ep.End
		if match := partRE.FindStringSubmatch(m.Name); len(match) > 0 {
			l.Part = "Part" + match[1]
		}
		if l.TMDBID > 0 {
			l.VersionGroup = fmt.Sprintf("%s:%d:%d:%d-%d:%s", l.Kind, l.TMDBID, l.Season, l.Episode, l.EpisodeEnd, l.Part)
		}
		if l.OutputPath != "" {
			l.TitlePath = filepath.Dir(l.OutputPath)
			if l.Kind == "tv" {
				l.TitlePath = filepath.Dir(l.TitlePath)
			}
		}
		if err := s.Store.PutLink(ctx, l); err != nil {
			return err
		}
		if l.OutputPath != "" {
			paths := []string{strings.TrimSuffix(l.OutputPath, filepath.Ext(l.OutputPath)) + ".nfo", filepath.Join(filepath.Dir(l.OutputPath), "movie.nfo"), filepath.Join(filepath.Dir(l.OutputPath), "season.nfo"), filepath.Join(filepath.Dir(filepath.Dir(l.OutputPath)), "tvshow.nfo")}
			for _, path := range paths {
				if !within(c.STRMPath, path) {
					continue
				}
				data, err := os.ReadFile(path)
				if err != nil {
					continue
				}
				var nfo metadataNFO
				if xml.Unmarshal(data, &nfo) != nil || nfo.Generator != "115 Direct" {
					continue
				}
				owners := []string{l.RemoteID}
				if a, err := s.Store.Asset(ctx, path); err == nil {
					owners = append(owners, a.Owners...)
				}
				if err := s.Store.PutAsset(ctx, store.Asset{Path: path, Hash: store.ContentHash(data), Owners: owners}); err != nil {
					return err
				}
			}
		}
	}
	return nil
}
