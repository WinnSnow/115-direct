package tmdb

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/local/115-direct/internal/store"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type Client struct {
	HTTP      *http.Client
	ImageHTTP *http.Client
	BaseURL   string
	Token     func(context.Context) (string, error)
	Cache     *store.Store
}

type Candidate struct {
	Kind          string   `json:"kind"`
	ID            int64    `json:"id"`
	Title         string   `json:"title"`
	OriginalTitle string   `json:"original_title"`
	Year          int      `json:"year,omitempty"`
	Score         float64  `json:"score"`
	Reasons       []string `json:"reasons,omitempty"`
	Overview      string   `json:"overview,omitempty"`
	PosterPath    string   `json:"poster_path,omitempty"`
}

type Details struct {
	Kind             string          `json:"kind"`
	ID               int64           `json:"id"`
	Title            string          `json:"title"`
	OriginalTitle    string          `json:"original_title"`
	Year             int             `json:"year"`
	OriginalLanguage string          `json:"original_language,omitempty"`
	OriginCountries  []string        `json:"origin_countries,omitempty"`
	GenreIDs         []int           `json:"genre_ids,omitempty"`
	Genres           []string        `json:"genres,omitempty"`
	Overview         string          `json:"overview,omitempty"`
	Date             string          `json:"date,omitempty"`
	PosterPath       string          `json:"poster_path,omitempty"`
	BackdropPath     string          `json:"backdrop_path,omitempty"`
	Rating           float64         `json:"rating,omitempty"`
	Runtime          int             `json:"runtime,omitempty"`
	Seasons          []SeasonSummary `json:"seasons,omitempty"`
	EpisodeCount     int             `json:"episode_count,omitempty"`
}

type SeasonSummary struct {
	ID           int64  `json:"id"`
	Number       int    `json:"season_number"`
	Title        string `json:"name"`
	EpisodeCount int    `json:"episode_count"`
	Date         string `json:"air_date,omitempty"`
	PosterPath   string `json:"poster_path,omitempty"`
	Overview     string `json:"overview,omitempty"`
}

func New(token func(context.Context) (string, error)) *Client {
	return &Client{HTTP: &http.Client{Timeout: 15 * time.Second}, BaseURL: "https://api.themoviedb.org/3", Token: token}
}

func (c *Client) Search(ctx context.Context, query string) ([]Candidate, error) {
	var response struct {
		Results []struct {
			MediaType     string `json:"media_type"`
			Title         string `json:"title"`
			Name          string `json:"name"`
			OriginalTitle string `json:"original_title"`
			OriginalName  string `json:"original_name"`
			ReleaseDate   string `json:"release_date"`
			FirstAirDate  string `json:"first_air_date"`
			Overview      string `json:"overview"`
			PosterPath    string `json:"poster_path"`
			ID            int64  `json:"id"`
		} `json:"results"`
	}
	params := url.Values{"query": {query}, "language": {"zh-CN"}, "include_adult": {"false"}, "page": {"1"}}
	if err := c.get(ctx, "/search/multi", params, &response); err != nil {
		return nil, err
	}
	out := make([]Candidate, 0, len(response.Results))
	for _, item := range response.Results {
		if item.MediaType != "movie" && item.MediaType != "tv" {
			continue
		}
		title, original, date := item.Title, item.OriginalTitle, item.ReleaseDate
		if item.MediaType == "tv" {
			title, original, date = item.Name, item.OriginalName, item.FirstAirDate
		}
		out = append(out, Candidate{Kind: item.MediaType, ID: item.ID, Title: title, OriginalTitle: original,
			Year: parseYear(date), Overview: item.Overview, PosterPath: item.PosterPath})
	}
	return out, nil
}

func (c *Client) Details(ctx context.Context, kind string, id int64) (*Details, error) {
	if (kind != "movie" && kind != "tv") || id <= 0 {
		return nil, fmt.Errorf("invalid TMDB kind")
	}
	var response struct {
		ID               int64           `json:"id"`
		Title            string          `json:"title"`
		Name             string          `json:"name"`
		OriginalTitle    string          `json:"original_title"`
		OriginalName     string          `json:"original_name"`
		OriginalLanguage string          `json:"original_language"`
		OriginCountry    []string        `json:"origin_country"`
		ReleaseDate      string          `json:"release_date"`
		FirstAirDate     string          `json:"first_air_date"`
		Overview         string          `json:"overview"`
		PosterPath       string          `json:"poster_path"`
		BackdropPath     string          `json:"backdrop_path"`
		Rating           float64         `json:"vote_average"`
		Runtime          int             `json:"runtime"`
		Seasons          []SeasonSummary `json:"seasons"`
		EpisodeCount     int             `json:"number_of_episodes"`
		EpisodeRuntime   []int           `json:"episode_run_time"`
		Production       []struct {
			Code string `json:"iso_3166_1"`
		} `json:"production_countries"`
		Genres []struct {
			ID   int    `json:"id"`
			Name string `json:"name"`
		} `json:"genres"`
	}
	if err := c.get(ctx, "/"+kind+"/"+strconv.FormatInt(id, 10), url.Values{"language": {"zh-CN"}}, &response); err != nil {
		return nil, err
	}
	title, original, date := response.Title, response.OriginalTitle, response.ReleaseDate
	if kind == "tv" {
		title, original, date = response.Name, response.OriginalName, response.FirstAirDate
		if response.Runtime == 0 && len(response.EpisodeRuntime) > 0 {
			response.Runtime = response.EpisodeRuntime[0]
		}
	} else {
		response.Seasons = nil
		response.EpisodeCount = 0
	}
	if title == "" {
		title = original
	}
	if response.ID != id || strings.TrimSpace(title) == "" {
		if c.Cache != nil {
			_ = c.Cache.DeleteCached(ctx, c.cacheKey("/"+kind+"/"+strconv.FormatInt(id, 10), url.Values{"language": {"zh-CN"}}))
		}
		return nil, fmt.Errorf("invalid TMDB details for %s/%d", kind, id)
	}
	title = c.preferChineseTitle(ctx, kind, id, title)
	countries := uniqueUpper(response.OriginCountry)
	for _, country := range response.Production {
		countries = appendUnique(countries, strings.ToUpper(strings.TrimSpace(country.Code)))
	}
	genres := make([]int, 0, len(response.Genres))
	genreNames := make([]string, 0, len(response.Genres))
	for _, genre := range response.Genres {
		genres = append(genres, genre.ID)
		genreNames = append(genreNames, genre.Name)
	}
	return &Details{
		Kind: kind, ID: response.ID, Title: title, OriginalTitle: original, Year: parseYear(date),
		OriginalLanguage: strings.ToLower(strings.TrimSpace(response.OriginalLanguage)),
		OriginCountries:  countries, GenreIDs: genres,
		Genres: genreNames, Overview: response.Overview, Date: date, PosterPath: response.PosterPath,
		BackdropPath: response.BackdropPath, Rating: response.Rating, Runtime: response.Runtime,
		Seasons: response.Seasons, EpisodeCount: response.EpisodeCount,
	}, nil
}

type EpisodeDetails struct {
	ID        int64   `json:"id"`
	Number    int     `json:"episode_number"`
	Title     string  `json:"name"`
	Overview  string  `json:"overview"`
	Date      string  `json:"air_date"`
	Rating    float64 `json:"vote_average"`
	StillPath string  `json:"still_path"`
}

type SeasonDetails struct {
	ID         int64            `json:"id"`
	Number     int              `json:"season_number"`
	Title      string           `json:"name"`
	Overview   string           `json:"overview"`
	Date       string           `json:"air_date"`
	PosterPath string           `json:"poster_path"`
	Episodes   []EpisodeDetails `json:"episodes"`
}

func (c *Client) Season(ctx context.Context, id int64, number int) (*SeasonDetails, error) {
	if id <= 0 || number < 0 || number > 99 {
		return nil, fmt.Errorf("invalid TMDB season")
	}
	var result SeasonDetails
	err := c.get(ctx, fmt.Sprintf("/tv/%d/season/%d", id, number), url.Values{"language": {"zh-CN"}}, &result)
	if err == nil && result.Number != number {
		return nil, fmt.Errorf("invalid TMDB season response")
	}
	return &result, err
}

// Images are fetched only from TMDB, never from arbitrary metadata URLs.
func (c *Client) Image(ctx context.Context, path string) ([]byte, error) {
	if !strings.HasPrefix(path, "/") || strings.Contains(path, "..") || strings.ContainsAny(path, "?#\\") {
		return nil, fmt.Errorf("invalid TMDB image path")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://image.tmdb.org/t/p/w780"+path, nil)
	if err != nil {
		return nil, err
	}
	client := c.ImageHTTP
	if client == nil {
		client = c.HTTP
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("TMDB image HTTP %d", resp.StatusCode)
	}
	if !strings.HasPrefix(resp.Header.Get("Content-Type"), "image/") {
		return nil, fmt.Errorf("invalid TMDB image content")
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, (12<<20)+1))
	if len(data) > 12<<20 {
		return nil, fmt.Errorf("TMDB image exceeds size limit")
	}
	return data, err
}

func uniqueUpper(values []string) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		result = appendUnique(result, strings.ToUpper(strings.TrimSpace(value)))
	}
	return result
}

func appendUnique(values []string, value string) []string {
	if value == "" {
		return values
	}
	for _, current := range values {
		if current == value {
			return values
		}
	}
	return append(values, value)
}

func (c *Client) cacheKey(path string, params url.Values) string {
	return store.ContentHash([]byte(c.BaseURL + path + "?" + params.Encode()))
}
func (c *Client) get(ctx context.Context, path string, params url.Values, out any) error {
	token, err := c.Token(ctx)
	if err != nil || strings.TrimSpace(token) == "" {
		return fmt.Errorf("TMDB token is not configured")
	}
	key := c.cacheKey(path, params)
	if c.Cache != nil {
		if raw, hit, err := c.Cache.Cached(ctx, key); err == nil && hit {
			if err := json.Unmarshal(raw, out); err == nil {
				c.Cache.Log(ctx, "recognition", "debug", "TMDB缓存命中 "+path+" · "+params.Get("query"))
				return nil
			}
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+path+"?"+params.Encode(), nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return fmt.Errorf("TMDB HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, (4<<20)+1))
	if err != nil {
		return err
	}
	if len(raw) > 4<<20 {
		return fmt.Errorf("TMDB response exceeds size limit")
	}
	var envelope struct {
		Success *bool `json:"success"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return err
	}
	if envelope.Success != nil && !*envelope.Success {
		return fmt.Errorf("TMDB returned an unsuccessful response")
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return err
	}
	if c.Cache != nil {
		kind := "details"
		label := path
		if strings.HasPrefix(path, "/search/") {
			kind = "search"
			label = params.Get("query")
		} else if strings.Contains(path, "/season/") {
			kind = "season"
		}
		if err := c.Cache.PutCached(ctx, key, kind, label, raw); err != nil {
			c.Cache.Log(ctx, "recognition", "warning", "缓存写入失败："+err.Error())
		}
	}
	return nil
}

func parseYear(value string) int {
	if len(value) < 4 {
		return 0
	}
	year, _ := strconv.Atoi(value[:4])
	return year
}
