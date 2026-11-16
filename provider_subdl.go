// SubDL: الواجهة الرسمية بمفتاح — بحث بالاسم أو بمعرّف IMDb/TMDB.
package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

type subdlClient struct {
	cfg  *Config
	http *http.Client
}

func NewSubDLClient(cfg *Config, client *http.Client) *subdlClient {
	return &subdlClient{cfg: cfg, http: client}
}

type subdlSub struct {
	URL         string `json:"url"`
	ReleaseName string `json:"release_name"`
	Name        string `json:"name"`
	Lang        string `json:"lang"`
	Language    string `json:"language"`
	Downloads   any    `json:"downloads"`
	Hi          bool   `json:"hi"`
	Hearing     bool   `json:"hearing_impaired"`
}

type subdlResp struct {
	Status    bool       `json:"status"`
	Subtitles []subdlSub `json:"subtitles"`
}

func (c *subdlClient) getJSON(ctx context.Context, params url.Values) (*subdlResp, error) {
	u := c.cfg.SubDLBase + "?" + params.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode >= 400 {
		return nil, &ProviderHTTPError{Status: resp.StatusCode, Message: string(body[:min(120, len(body))]),
			RetryAfter: retryAfter(resp.Header.Get("Retry-After"))}
	}
	var out subdlResp
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Search بحث SubDL: film_name أو imdb/tmdb + اللغات (حروف كبيرة عندهم).
func (c *subdlClient) Search(ctx context.Context, key string, filmName string, season, episode *int, imdbID, tmdbID, lang string) ([]*Candidate, error) {
	params := url.Values{
		"api_key":       {key},
		"languages":     {strings.ToUpper(lang)},
		"subs_per_page": {"30"},
		"releases":      {"1"},
	}
	if imdbID != "" {
		params.Set("imdb_id", imdbID)
	} else if tmdbID != "" {
		params.Set("tmdb_id", tmdbID)
	} else if filmName != "" {
		params.Set("film_name", filmName)
	} else {
		return nil, nil
	}
	if season != nil && episode != nil {
		params.Set("season_number", strconv.Itoa(*season))
		params.Set("episode_number", strconv.Itoa(*episode))
		params.Set("type", "tv")
	}
	out, err := c.getJSON(ctx, params)
	if err != nil {
		return nil, err
	}
	cands := make([]*Candidate, 0, len(out.Subtitles))
	for _, s := range out.Subtitles {
		if s.URL == "" {
			continue
		}
		lg := normLang(firstNonEmpty(s.Lang, s.Language, lang))
		c := &Candidate{
			Provider: "subdl",
			Ref:      s.URL,
			Lang:     lg,
			Release:  firstNonEmpty(s.ReleaseName, s.Name),
			Hi:       s.Hi || s.Hearing,
		}
		if d, ok := s.Downloads.(float64); ok && d > 0 {
			n := int64(d)
			c.Downloads = &n
		}
		cands = append(cands, c)
	}
	return cands, nil
}

// Download تنزيل من dl.subdl.com (قد يكون ZIP).
func (c *subdlClient) Download(ctx context.Context, ref string) ([]byte, error) {
	u := ref
	if !strings.HasPrefix(u, "http") {
		u = strings.TrimRight(c.cfg.SubDLDl, "/") + "/" + strings.TrimLeft(u, "/")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return nil, &ProviderHTTPError{Status: resp.StatusCode, Message: "download failed"}
	}
	return io.ReadAll(io.LimitReader(resp.Body, 20<<20))
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
