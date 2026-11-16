// خادم بروتوكول v2 فائق السرعة: net/http خام، بحث متوازٍ، كاش ثلاثي الطبقات، singleflight.
// لا AI، لا ترجمة آلية، لا TMDB — OpenSubtitles + SubDL فقط.
package main

import (
	"context"
	"crypto/sha1"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// QueryInfo معلومات الاستعلام المستخرجة من اسم الملف.
type QueryInfo struct {
	Title   string `json:"title"`
	Year    *int   `json:"year"`
	Season  *int   `json:"season"`
	Episode *int   `json:"episode"`
}

// Result عنصر نتيجة وفق بروتوكول v2.
type Result struct {
	ID        string   `json:"id"`
	Source    string   `json:"source"`
	Lang      string   `json:"lang"`
	Release   string   `json:"release"`
	Match     int      `json:"match"`
	HashMatch bool     `json:"hash_match"`
	Rating    *float64 `json:"rating"`
	Downloads *int64   `json:"downloads"`
	Format    string   `json:"format"`
	Hi        bool     `json:"hi"`
	Fps       *float64 `json:"fps,omitempty"`
}

// SearchResponse الرد الكامل لـ /search.
type SearchResponse struct {
	Query   QueryInfo `json:"query"`
	Tier    int       `json:"tier"`
	Cached  bool      `json:"cached"`
	Results []Result  `json:"results"`
}

var fileUIDRx = regexp.MustCompile(`^/file/([0-9a-f]{16})$`)

// Server الحاوية الكاملة.
type Server struct {
	cfg    Config
	store  *Store
	hot    *HotCache
	osub   *osClient
	subdl  *subdlClient
	osPool *KeyPool
	sdPool *KeyPool

	flightsMu sync.Mutex
	flights   map[string]*flight

	hitsMu sync.Mutex
	hits   map[string][]int64

	kaMu    sync.Mutex
	ka      map[string]any
	started time.Time
}

type flight struct {
	done  chan struct{}
	resp  SearchResponse
	fresh bool // أول منفّذ (يُعلن cached=false) — المنضمّون يعلنون true
	err   error
}

// NewServer يبني الخادم مع عميل HTTP مضبوط لإعادة استخدام الاتصالات.
func NewServer(cfg Config, store *Store) *Server {
	transport := &http.Transport{
		MaxIdleConns:        64,
		MaxIdleConnsPerHost: 12,
		IdleConnTimeout:     90 * time.Second,
		TLSHandshakeTimeout: 8 * time.Second,
		ForceAttemptHTTP2:   true,
	}
	client := &http.Client{Transport: transport}
	s := &Server{
		cfg:     cfg,
		store:   store,
		hot:     NewHotCache(3000),
		osub:    NewOSClient(&cfg, client),
		subdl:   NewSubDLClient(&cfg, client),
		osPool:  NewKeyPool("opensubtitles", osAccountKeys(cfg.OSAccounts), cfg.OSDailyLimit),
		sdPool:  NewKeyPool("subdl", cfg.SubDLKeys, cfg.SubDLDailyLimit),
		flights: map[string]*flight{},
		hits:    map[string][]int64{},
		ka:      map[string]any{"enabled": cfg.KeepAliveURL != "", "last_ok": nil, "last_error": nil, "pings": 0},
		started: time.Now(),
	}
	// تنظيف دوري للكاش المنتهي
	go func() {
		t := time.NewTicker(time.Hour)
		defer t.Stop()
		for range t.C {
			store.PurgeExpired()
		}
	}()
	// إبقاء Render مستيقظاً (يمنع الاستيقاظ البارد ~60 ثانية)
	if cfg.KeepAliveURL != "" {
		go func() {
			time.Sleep(30 * time.Second)
			t := time.NewTicker(cfg.KeepAliveInterval)
			defer t.Stop()
			ping := func() {
				c, cancel := context.WithTimeout(context.Background(), 15*time.Second)
				defer cancel()
				req, err := http.NewRequestWithContext(c, http.MethodGet, cfg.KeepAliveURL+"/health", nil)
				if err != nil {
					return
				}
				resp, err := client.Do(req)
				s.kaMu.Lock()
				defer s.kaMu.Unlock()
				pings := s.ka["pings"].(int)
				if err != nil {
					s.ka["last_error"] = "error"
				} else {
					resp.Body.Close()
					pings++
					s.ka["pings"] = pings
					if resp.StatusCode < 500 {
						s.ka["last_ok"] = time.Now().Unix()
						s.ka["last_error"] = nil
					} else {
						s.ka["last_error"] = fmt.Sprintf("http %d", resp.StatusCode)
					}
				}
			}
			ping()
			for range t.C {
				ping()
			}
		}()
	}
	return s
}

func osAccountKeys(accs []OSAccount) []string {
	out := make([]string, 0, len(accs))
	for _, a := range accs {
		out = append(out, a.APIKey)
	}
	return out
}

// Handler موزّع الطلبات.
func (s *Server) Handler() http.Handler {
	return http.HandlerFunc(s.route)
}

func (s *Server) route(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimRight(r.URL.Path, "/")
	if path == "" {
		path = "/"
	}

	if path == "/health" {
		s.handleHealth(w, r)
		return
	}

	// المصادقة (بروتوكول v2 §1) — مقارنة زمن ثابت
	if s.cfg.AppToken != "" {
		got := r.Header.Get("X-App-Token")
		if subtle.ConstantTimeCompare([]byte(got), []byte(s.cfg.AppToken)) != 1 {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
			return
		}
	}
	// حد المعدل
	ip := clientIP(r)
	if !s.rateOk(ip) {
		writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": "rate_limited"})
		return
	}

	switch {
	case path == "/search" && r.Method == http.MethodGet:
		s.handleSearch(w, r)
	case fileUIDRx.MatchString(path) && r.Method == http.MethodGet:
		uid := fileUIDRx.FindStringSubmatch(path)[1]
		s.handleFile(w, r, uid)
	default:
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
	}
}

func clientIP(r *http.Request) string {
	// the LAST entry is the one appended by our own proxy (Render); earlier ones are client-controlled
	if xf := r.Header.Get("X-Forwarded-For"); xf != "" {
		parts := strings.Split(xf, ",")
		if ip := strings.TrimSpace(parts[len(parts)-1]); ip != "" {
			return ip
		}
	}
	// RemoteAddr يأتي "ip:port" — نأخذ IP فقط (نفس سلوك socket.remoteAddress)
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return host
	}
	return r.RemoteAddr
}

func (s *Server) rateOk(ip string) bool {
	s.hitsMu.Lock()
	defer s.hitsMu.Unlock()
	now := time.Now().UnixMilli()
	win := now - 60_000
	q := s.hits[ip]
	i := 0
	for ; i < len(q); i++ {
		if q[i] >= win {
			break
		}
	}
	q = q[i:]
	if len(q) >= s.cfg.RateLimitPerMin {
		s.hits[ip] = q
		return false
	}
	q = append(q, now)
	s.hits[ip] = q
	if len(s.hits) > 5000 { // sweep only stale IPs; never reset everyone's counters
		for k, v := range s.hits {
			if len(v) == 0 || v[len(v)-1] < win {
				delete(s.hits, k)
			}
		}
	}
	return true
}

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	s.kaMu.Lock()
	ka := map[string]any{}
	for k, v := range s.ka {
		ka[k] = v
	}
	s.kaMu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":         true,
		"uptime":     int(time.Since(s.started).Seconds()),
		"providers":  map[string]any{"osub": s.osPool.Status(), "subdl": s.sdPool.Status()},
		"hot_cache":  s.hot.Size(),
		"keep_alive": ka,
	})
}

// ————— البحث —————

func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	if s.osPool.Size() == 0 && s.sdPool.Size() == 0 {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "quota_exhausted"})
		return
	}
	q := r.URL.Query()
	lang := normalizeLangs(q.Get("lang"))
	filename := q.Get("filename")
	manual := strings.TrimSpace(q.Get("q"))
	hash := strings.ToLower(strings.TrimSpace(q.Get("hash")))
	if !hashRx.MatchString(hash) {
		hash = ""
	}
	var videoFps *float64
	if f, err := parseFloat(q.Get("fps")); err == nil && f > 0 {
		videoFps = &f
	}
	slangs := map[string]bool{}
	for _, l := range strings.Split(q.Get("slangs"), ",") {
		if l = strings.TrimSpace(strings.ToLower(l)); l != "" {
			slangs[l] = true
		}
	}
	refresh := q.Get("refresh") == "1"
	imdbID := q.Get("imdb_id")
	tmdbID := q.Get("tmdb_id")

	if filename == "" && manual == "" && imdbID == "" && tmdbID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "filename or q required"})
		return
	}

	// استخراج العنوان محلياً — فوري (<0.05ms)
	parsed := ParseFilename(firstNonEmpty(manual, filename))
	if manual != "" && filename != "" {
		m := ParseFilename(filename)
		if parsed.Title == "" {
			parsed.Title = m.Title
		}
		if parsed.TitleNorm == "" {
			parsed.TitleNorm = m.TitleNorm
		}
		if parsed.Year == nil {
			parsed.Year = m.Year
		}
	}
	if sv, ev := parseInt(q.Get("season")), parseInt(q.Get("episode")); sv > 0 && ev > 0 {
		parsed.Season, parsed.Episode = &sv, &ev
	}

	// مفتاح الكاش حسب بروتوكول v2 §6: البصمة إن وُجدت وإلا الاسم+q+اللغات
	slangKey := sortedKeys(slangs)
	// every input that changes the upstream query must be part of the key
	ids := fmt.Sprintf("%s|%s|%d|%d", imdbID, tmdbID, parseInt(q.Get("season")), parseInt(q.Get("episode")))
	ckey := ""
	if hash != "" {
		ckey = fmt.Sprintf("h:%s:%s:%s:%s:%s", hash, strings.Join(lang, ","), manual, slangKey, ids)
	} else {
		sum := sha1.Sum([]byte(filename + "|" + manual + "|" + strings.Join(lang, ",") + "|" + slangKey + "|" + ids))
		ckey = "n:" + hex.EncodeToString(sum[:])
	}

	// 1) ذاكرة ساخنة → 2) bbolt → 3) الأعلى
	if !refresh {
		if resp, ok := s.hot.Get(ckey); ok {
			writeSearch(w, resp, true)
			return
		}
		if resp, ok := s.store.GetSearch(ckey); ok {
			s.hot.Set(ckey, *resp, 5*time.Minute)
			writeSearch(w, resp, true)
			return
		}
	}

	// singleflight: من يتطابق مع طلب جارٍ ينتظر نفس النتيجة
	resp, fresh, err := s.flightDo(ckey, parsed, lang, hash, slangs, videoFps, imdbID, tmdbID)
	if err != nil {
		if _, isNK := err.(*NoKeyAvailable); isNK {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "quota_exhausted"})
			return
		}
		log.Printf("[search] %v", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "server_error"})
		return
	}
	writeSearch(w, &resp, !fresh)
}

var hashRx = regexp.MustCompile(`^[0-9a-f]{16}$`)

// flightDo singleflight: أول طلب ينفّذ، والمنضمّون ينتظرون نفس النتيجة.
func (s *Server) flightDo(ckey string, parsed Parsed, lang []string, hash string,
	slangs map[string]bool, videoFps *float64, imdbID, tmdbID string) (SearchResponse, bool, error) {

	s.flightsMu.Lock()
	if f, ok := s.flights[ckey]; ok {
		s.flightsMu.Unlock()
		<-f.done
		return f.resp, false, f.err // المنضمّ يعلن cached=true إن نجح الأول
	}
	f := &flight{done: make(chan struct{}), fresh: true}
	s.flights[ckey] = f
	s.flightsMu.Unlock()

	go func() {
		defer close(f.done)
		defer func() {
			s.flightsMu.Lock()
			delete(s.flights, ckey)
			s.flightsMu.Unlock()
		}()
		resp, err := s.searchFlow(ckey, parsed, lang, hash, slangs, videoFps, imdbID, tmdbID)
		if err != nil {
			f.err = err
			return
		}
		f.resp = resp
	}()

	<-f.done
	return f.resp, f.fresh, f.err
}

// searchFlow البحث الكامل: الأعلى → الترتيب → الكاش (مفتاح موحّد ckey).
func (s *Server) searchFlow(ckey string, parsed Parsed, lang []string, hash string,
	slangs map[string]bool, videoFps *float64, imdbID, tmdbID string) (SearchResponse, error) {

	cands, partial, err := s.searchUpstream(parsed, lang, hash, imdbID, tmdbID)
	if err != nil {
		return SearchResponse{}, err
	}

	// استبعاد لغات مضمّنة في الفيديو (slangs في v2) ما لم تكن النتائج كلها منها
	pool := cands
	noSlang := filterOutSlangs(cands, slangs)
	if len(noSlang) > 0 || !anySlang(cands, slangs) {
		pool = noSlang
	}

	ranked := Rank(pool, &parsed, RankContext{VideoFps: videoFps, Slangs: slangs})
	tier := 2
	for _, r := range ranked {
		if r.C.HashMatch {
			tier = 1
			break
		}
	}

	resp := SearchResponse{
		Query:   QueryInfo{Title: parsed.Title, Year: parsed.Year, Season: parsed.Season, Episode: parsed.Episode},
		Tier:    tier,
		Results: make([]Result, 0, min(30, len(ranked))),
	}
	for i, r := range ranked {
		if i >= 30 {
			break
		}
		resp.Results = append(resp.Results, Result{
			ID:        r.C.UID,
			Source:    r.C.Provider,
			Lang:      r.C.Lang,
			Release:   r.C.Release,
			Match:     r.Match,
			HashMatch: r.C.HashMatch,
			Rating:    r.C.Rating,
			Downloads: r.C.Downloads,
			Format:    orDefault(r.C.Format, "srt"),
			Hi:        r.C.Hi,
			Fps:       r.C.Fps,
		})
	}

	// كاش: موجبة طويلة، وسلبيات قصيرة (كاش سلبي يمنع ضرب الأعلى مراراً)
	ttl, hotTTL := s.cfg.CacheTTL, 5*time.Minute
	if len(resp.Results) == 0 {
		ttl, hotTTL = s.cfg.NegCacheTTL, time.Minute
	}
	if partial { // a provider failed: don't pin an incomplete answer for days
		ttl, hotTTL = 2*time.Minute, 30*time.Second
	}
	s.store.PutSearch(ckey, resp, ttl)
	s.hot.Set(ckey, resp, hotTTL)
	return resp, nil
}

// searchUpstream الطلبات المتوازية للمزوّدين (بصمة + اسم + SubDL) ضمن مهلة صارمة.
func (s *Server) searchUpstream(parsed Parsed, lang []string, hash, imdbID, tmdbID string) ([]*Candidate, bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), s.cfg.SearchTimeout)
	defer cancel()
	langStr := strings.Join(lang, ",")

	var (
		mu    sync.Mutex
		cands []*Candidate
		errs  []error
		wg    sync.WaitGroup
	)
	collect := func(cs []*Candidate, err error) {
		defer wg.Done()
		mu.Lock()
		defer mu.Unlock()
		if err != nil {
			errs = append(errs, err)
			return
		}
		for _, c := range cs {
			c.UID = uid16(c.Provider, c.Ref)
		}
		cands = append(cands, cs...)
	}

	// OpenSubtitles بالبصمة أولاً (الأدق حسب v2)
	if s.osPool.Size() > 0 && hash != "" {
		wg.Add(1)
		go func() {
			var got []*Candidate
			var err error
			e := s.osPool.Call(ctx, true, func(key string) error {
				got, err = s.osub.SearchByHash(ctx, s.accByKey(key), hash, langStr)
				return err
			})
			collect(got, e)
		}()
	}
	// OpenSubtitles بالاسم
	if s.osPool.Size() > 0 {
		wg.Add(1)
		go func() {
			var got []*Candidate
			var err error
			e := s.osPool.Call(ctx, true, func(key string) error {
				got, err = s.osub.SearchByName(ctx, s.accByKey(key), parsed.Title, parsed.Year,
					parsed.Season, parsed.Episode, imdbID, tmdbID, langStr)
				return err
			})
			collect(got, e)
		}()
	}
	// SubDL بالاسم
	if s.sdPool.Size() > 0 {
		wg.Add(1)
		go func() {
			var got []*Candidate
			var err error
			e := s.sdPool.Call(ctx, true, func(key string) error {
				got, err = s.subdl.Search(ctx, key, parsed.Title, parsed.Season, parsed.Episode, imdbID, tmdbID, langStr)
				return err
			})
			collect(got, e)
		}()
	}
	wg.Wait()

	allNoKey := len(errs) > 0
	for _, e := range errs {
		if _, ok := e.(*NoKeyAvailable); !ok {
			allNoKey = false
		}
		log.Printf("[provider] %v", e) // log every provider failure
	}
	if len(cands) == 0 && allNoKey {
		return nil, true, errs[0]
	}

	// دمج + إزالة التكرار (نفس المزوّد والمرجع)
	seen := map[string]bool{}
	uniq := make([]*Candidate, 0, len(cands))
	for _, c := range cands {
		k := c.Provider + "|" + c.Ref
		if seen[k] {
			continue
		}
		seen[k] = true
		uniq = append(uniq, c)
	}
	s.store.PutCandidates(uniq) // single transaction
	return uniq, len(errs) > 0, nil
}

func (s *Server) accByKey(key string) OSAccount {
	for _, a := range s.cfg.OSAccounts {
		if a.APIKey == key {
			return a
		}
	}
	return OSAccount{APIKey: key}
}

func filterOutSlangs(cands []*Candidate, slangs map[string]bool) []*Candidate {
	out := make([]*Candidate, 0, len(cands))
	for _, c := range cands {
		if !slangs[c.Lang] {
			out = append(out, c)
		}
	}
	return out
}

func anySlang(cands []*Candidate, slangs map[string]bool) bool {
	for _, c := range cands {
		if slangs[c.Lang] {
			return true
		}
	}
	return false
}

// ————— الملف —————

func (s *Server) handleFile(w http.ResponseWriter, r *http.Request, uid string) {
	meta, ok := s.store.GetMeta(uid)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown id"})
		return
	}
	if blob := s.store.GetBlob(uid); len(blob) > 0 {
		writeFile(w, blob, meta.Fmt, meta.Lang)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), s.cfg.RequestTimeout)
	defer cancel()
	var (
		data []byte
		err  error
	)
	switch meta.Provider {
	case "opensubtitles":
		err = s.osPool.Call(ctx, false, func(key string) error {
			data, err = s.osub.Download(ctx, s.accByKey(key), meta.Ref)
			return err
		})
	case "subdl":
		data, err = s.subdl.Download(ctx, meta.Ref)
	default:
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "bad provider"})
		return
	}
	if err != nil {
		if nk, isNK := err.(*NoKeyAvailable); isNK {
			_ = nk
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "quota_exhausted"})
			return
		}
		if pe, isPE := err.(*ProviderHTTPError); isPE {
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": fmt.Sprintf("provider error %d", pe.Status)})
			return
		}
		log.Printf("[file] %v", err)
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "bad subtitle"})
		return
	}
	blob, fmt_, _, nerr := NormalizeSubtitle(data, meta.Lang)
	if nerr != nil {
		log.Printf("[file] normalize: %v", nerr)
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "bad subtitle"})
		return
	}
	s.store.PutBlob(uid, blob)
	writeFile(w, blob, fmt_, meta.Lang)
}

// ————— مساعدات —————

func writeJSON(w http.ResponseWriter, code int, v any) {
	raw, _ := json.Marshal(v)
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Content-Length", strconv.Itoa(len(raw)))
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	w.Write(raw)
}

func writeSearch(w http.ResponseWriter, resp *SearchResponse, cached bool) {
	resp.Cached = cached
	writeJSON(w, http.StatusOK, resp)
}

func writeFile(w http.ResponseWriter, blob []byte, fmt_, lang string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Content-Length", strconv.Itoa(len(blob)))
	w.Header().Set("X-Subtitle-Format", fmt_)
	w.Header().Set("X-Subtitle-Lang", lang)
	w.WriteHeader(http.StatusOK)
	w.Write(blob)
}

func normalizeLangs(s string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, p := range strings.Split(s, ",") {
		p = strings.TrimSpace(strings.ToLower(p))
		if p != "" && !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	if len(out) == 0 {
		return []string{"ar", "en"}
	}
	return out
}

func sortedKeys(m map[string]bool) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return strings.Join(keys, ",")
}

func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

func parseInt(s string) int {
	n, _ := strconv.Atoi(strings.TrimSpace(s))
	return n
}

func parseFloat(s string) (float64, error) {
	return strconv.ParseFloat(strings.TrimSpace(s), 64)
}
