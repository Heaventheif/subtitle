// OpenSubtitles: البحث بالبصمة (الأدق) + البحث بالاسم، والتنزيل بتسجيل دخول الحساب.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

var osLangMap = map[string]string{
	"ara": "ar", "arabic": "ar", "eng": "en", "english": "en", "fre": "fr",
	"french": "fr", "spa": "es", "spanish": "es", "tur": "tr", "turkish": "tr",
	"per": "fa", "persian": "fa", "german": "de", "ger": "de", "portuguese": "pt",
	"por": "pt", "chinese": "zh", "chi": "zh", "dutch": "nl", "russian": "ru",
	"japanese": "ja", "korean": "ko", "italian": "it", "hebrew": "he", "indonesian": "id",
}

func normLang(v string) string {
	v = strings.ToLower(strings.TrimSpace(v))
	if m, ok := osLangMap[v]; ok {
		return m
	}
	if len(v) > 2 {
		v = v[:2]
	}
	return v
}

type osClient struct {
	cfg    *Config
	http   *http.Client
	mu     sync.Mutex
	tokens map[string]osToken // packed account → token
}

type osToken struct {
	token   string
	expires time.Time
}

func NewOSClient(cfg *Config, client *http.Client) *osClient {
	return &osClient{cfg: cfg, http: client, tokens: map[string]osToken{}}
}

func (c *osClient) headers(apiKey string) map[string]string {
	return map[string]string{
		"Api-Key":      apiKey,
		"User-Agent":   c.cfg.UserAgent,
		"Accept":       "application/json",
		"Content-Type": "application/json",
	}
}

func (c *osClient) getJSON(ctx context.Context, path string, params url.Values, apiKey string) (map[string]any, error) {
	u := c.cfg.OSBase + path + "?" + params.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	for k, v := range c.headers(apiKey) {
		req.Header.Set(k, v)
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
	var out map[string]any
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func retryAfter(v string) float64 {
	if n, err := strconv.ParseFloat(strings.TrimSpace(v), 64); err == nil {
		return n
	}
	return 0
}

func (c *osClient) forgetToken(acc OSAccount) {
	c.mu.Lock()
	delete(c.tokens, acc.APIKey+"|"+acc.Username+"|"+acc.Password)
	c.mu.Unlock()
}

func (c *osClient) loginToken(ctx context.Context, acc OSAccount) (string, error) {
	packed := acc.APIKey + "|" + acc.Username + "|" + acc.Password
	c.mu.Lock()
	if t, ok := c.tokens[packed]; ok && time.Now().Before(t.expires) {
		c.mu.Unlock()
		return t.token, nil
	}
	c.mu.Unlock()

	bodyJSON, _ := json.Marshal(map[string]string{"username": acc.Username, "password": acc.Password})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.cfg.OSBase+"/login", strings.NewReader(string(bodyJSON)))
	if err != nil {
		return "", err
	}
	for k, v := range c.headers(acc.APIKey) {
		req.Header.Set(k, v)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode >= 400 {
		return "", &ProviderHTTPError{Status: resp.StatusCode, Message: string(body[:min(120, len(body))])}
	}
	var out struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(body, &out); err != nil || out.Token == "" {
		return "", fmt.Errorf("os login: bad response")
	}
	c.mu.Lock()
	c.tokens[packed] = osToken{token: out.Token, expires: time.Now().Add(20 * time.Hour)}
	c.mu.Unlock()
	return out.Token, nil
}

// attrsOS يستخرج الحقول المتوقعة من سجل OpenSubtitles بتساهل.
type osFileRef struct {
	FileID   any `json:"file_id"`
	FileName any `json:"file_name"`
}

type osAttrs struct {
	Language          string      `json:"language"`
	Release           string      `json:"release"`
	Files             []osFileRef `json:"files"`
	DownloadCount     int64       `json:"download_count"`
	Ratings           any         `json:"ratings"`
	HearingImpaired   bool        `json:"hearing_impaired"`
	MoviehashMatch    bool        `json:"moviehash_match"`
	Fps               any         `json:"fps"`
	FramesPerSecond   any         `json:"frames_per_second"`
	AiTranslated      bool        `json:"ai_translated"`
	MachineTranslated bool        `json:"machine_translated"`
	AutoTranslation   bool        `json:"auto_translation"`
}

func toCandidatesOS(data []any) []*Candidate {
	out := make([]*Candidate, 0, len(data))
	for _, d := range data {
		item, ok := d.(map[string]any)
		if !ok {
			continue
		}
		rawAttrs, _ := json.Marshal(item["attributes"])
		var a osAttrs
		if json.Unmarshal(rawAttrs, &a) != nil || len(a.Files) == 0 {
			continue
		}
		fileID := fmt.Sprintf("%v", a.Files[0].FileID)
		c := &Candidate{
			Provider:     "opensubtitles",
			Ref:          fileID,
			Lang:         normLang(a.Language),
			Release:      a.Release,
			Hi:           a.HearingImpaired,
			HashMatch:    a.MoviehashMatch,
			AiTranslated: a.AiTranslated || a.MachineTranslated || a.AutoTranslation,
		}
		if a.Release == "" {
			if s, ok := a.Files[0].FileName.(string); ok {
				c.Release = s
			}
		}
		if a.DownloadCount > 0 {
			c.Downloads = &a.DownloadCount
		}
		if r, ok := a.Ratings.(float64); ok && r > 0 {
			c.Rating = &r
		}
		if f := anyToFps(a.Fps); f != nil {
			c.Fps = f
		} else if f := anyToFps(a.FramesPerSecond); f != nil {
			c.Fps = f
		}
		out = append(out, c)
	}
	return out
}

func anyToFps(v any) *float64 {
	switch x := v.(type) {
	case float64:
		if x > 0 {
			return &x
		}
	case string:
		if f, err := strconv.ParseFloat(x, 64); err == nil && f > 0 {
			return &f
		}
	}
	return nil
}

// SearchByHash بحث بالبصمة — أسرع وأدق مسار (بروتوكول v2 §3).
func (c *osClient) SearchByHash(ctx context.Context, acc OSAccount, hash, lang string) ([]*Candidate, error) {
	params := url.Values{"languages": {lang}, "moviehash": {hash}, "order_by": {"download_count"}}
	out, err := c.getJSON(ctx, "/subtitles", params, acc.APIKey)
	if err != nil {
		return nil, err
	}
	data, _ := out["data"].([]any)
	return toCandidatesOS(data), nil
}

// SearchByName بحث نصي/بمعرّف.
func (c *osClient) SearchByName(ctx context.Context, acc OSAccount,
	query string, year *int, season, episode *int, imdbID, tmdbID, lang string) ([]*Candidate, error) {
	params := url.Values{"languages": {lang}, "order_by": {"download_count"}}
	if imdbID != "" {
		params.Set("imdb_id", strings.TrimLeft(imdbID, "t"))
	} else if tmdbID != "" {
		params.Set("tmdb_id", tmdbID)
	} else if query != "" {
		q := query
		if year != nil && season == nil {
			q = fmt.Sprintf("%s %d", query, *year)
		}
		params.Set("query", q)
	} else {
		return nil, nil
	}
	if season != nil && episode != nil {
		params.Set("season_number", strconv.Itoa(*season))
		params.Set("episode_number", strconv.Itoa(*episode))
	}
	out, err := c.getJSON(ctx, "/subtitles", params, acc.APIKey)
	if err != nil {
		return nil, err
	}
	data, _ := out["data"].([]any)
	return toCandidatesOS(data), nil
}

// Download تنزيل ملف الترجمة عبر تسجيل الدخول ثم الرابط الموقت.
func (c *osClient) Download(ctx context.Context, acc OSAccount, fileID string) ([]byte, error) {
	token, err := c.loginToken(ctx, acc)
	if err != nil {
		return nil, err
	}
	bodyJSON, _ := json.Marshal(map[string]any{"file_id": fileID})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.cfg.OSBase+"/download", strings.NewReader(string(bodyJSON)))
	if err != nil {
		return nil, err
	}
	for k, v := range c.headers(acc.APIKey) {
		req.Header.Set(k, v)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	resp.Body.Close()
	if resp.StatusCode >= 400 {
		if resp.StatusCode == 401 {
			c.forgetToken(acc) // stale token: force fresh login next time
		}
		return nil, &ProviderHTTPError{Status: resp.StatusCode, Message: string(body[:min(120, len(body))])}
	}
	var out struct {
		Link string `json:"link"`
	}
	if err := json.Unmarshal(body, &out); err != nil || out.Link == "" {
		return nil, &ProviderHTTPError{Status: 502, Message: "no link"}
	}
	f, err := http.NewRequestWithContext(ctx, http.MethodGet, out.Link, nil)
	if err != nil {
		return nil, err
	}
	fr, err := c.http.Do(f)
	if err != nil {
		return nil, err
	}
	defer fr.Body.Close()
	if fr.StatusCode >= 400 {
		return nil, &ProviderHTTPError{Status: fr.StatusCode, Message: "file fetch failed"}
	}
	return io.ReadAll(io.LimitReader(fr.Body, 20<<20))
}
