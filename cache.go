// طبقات الكاش الثلاث: ذاكرة ساخنة (0ms) → bbolt (mmap، <0.05ms) → الشبكة.
// bbolt مخزن KV مضمّن بلغة Go خالصة — صفر تبعيات أصلية، ملف واحد.
package main

import (
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"

	bolt "go.etcd.io/bbolt"
)

// Candidate مرشّح ترجمة خام من مزوّد.
type Candidate struct {
	Provider     string   `json:"provider"`
	Ref          string   `json:"ref"`
	Lang         string   `json:"lang"`
	Release      string   `json:"release"`
	Rating       *float64 `json:"rating,omitempty"`
	Downloads    *int64   `json:"downloads,omitempty"`
	Format       string   `json:"fmt,omitempty"`
	Hi           bool     `json:"hi"`
	HashMatch    bool     `json:"hash_match"`
	Fps          *float64 `json:"fps,omitempty"`
	AiTranslated bool     `json:"ai_translated,omitempty"`
	UID          string   `json:"uid"`
}

// SubtitleFile سجل ملف ترجمة مخزّن (blob مستقل).
type SubtitleFile struct {
	UID       string   `json:"uid"`
	Provider  string   `json:"provider"`
	Ref       string   `json:"ref"`
	Lang      string   `json:"lang"`
	Release   string   `json:"release"`
	Rating    *float64 `json:"rating,omitempty"`
	Downloads *int64   `json:"downloads,omitempty"`
	Fmt       string   `json:"fmt"`
	Hi        bool     `json:"hi"`
	HashMatch bool     `json:"hash_match"`
	Fps       *float64 `json:"fps,omitempty"`
}

var (
	bktSearch = []byte("search")
	bktMeta   = []byte("fmeta")
	bktBlob   = []byte("fblob")
)

// Store مخزن bbolt الدائم.
type Store struct {
	db *bolt.DB
}

func OpenStore(path string) (*Store, error) {
	if path == ":memory:" {
		f, err := os.CreateTemp("", "bbolt-*.db")
		if err != nil {
			return nil, err
		}
		path = f.Name()
		f.Close()
	} else {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return nil, err
		}
	}
	opts := *bolt.DefaultOptions
	opts.Timeout = 5 * time.Second
	opts.NoFreelistSync = true
	db, err := bolt.Open(path, 0o600, &opts)
	if err != nil {
		return nil, err
	}
	err = db.Update(func(tx *bolt.Tx) error {
		for _, b := range [][]byte{bktSearch, bktMeta, bktBlob} {
			if _, e := tx.CreateBucketIfNotExists(b); e != nil {
				return e
			}
		}
		return nil
	})
	if err != nil {
		db.Close()
		return nil, err
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

type searchCacheEntry struct {
	Resp    SearchResponse `json:"resp"`
	Expires int64          `json:"expires"`
}

func (s *Store) GetSearch(key string) (*SearchResponse, bool) {
	var out *SearchResponse
	s.db.View(func(tx *bolt.Tx) error {
		raw := tx.Bucket(bktSearch).Get([]byte(key))
		if raw == nil {
			return nil
		}
		var e searchCacheEntry
		if json.Unmarshal(raw, &e) != nil || time.Now().UnixMilli() > e.Expires {
			return nil
		}
		out = &e.Resp
		return nil
	})
	return out, out != nil
}

func (s *Store) PutSearch(key string, resp SearchResponse, ttl time.Duration) {
	raw, _ := json.Marshal(searchCacheEntry{Resp: resp, Expires: time.Now().Add(ttl).UnixMilli()})
	s.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket(bktSearch).Put([]byte(key), raw)
	})
}

func (s *Store) GetMeta(uid string) (*SubtitleFile, bool) {
	var out *SubtitleFile
	s.db.View(func(tx *bolt.Tx) error {
		raw := tx.Bucket(bktMeta).Get([]byte(uid))
		if raw == nil {
			return nil
		}
		var f SubtitleFile
		if json.Unmarshal(raw, &f) != nil {
			return nil
		}
		out = &f
		return nil
	})
	return out, out != nil
}

func (s *Store) PutMeta(f *SubtitleFile) {
	raw, _ := json.Marshal(f)
	s.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket(bktMeta).Put([]byte(f.UID), raw)
	})
}

// PutCandidates stores all candidates in ONE transaction (one fsync).
func (s *Store) PutCandidates(cs []*Candidate) {
	if len(cs) == 0 {
		return
	}
	s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(bktMeta)
		for _, c := range cs {
			raw, _ := json.Marshal(&SubtitleFile{
				UID: c.UID, Provider: c.Provider, Ref: c.Ref, Lang: c.Lang, Release: c.Release,
				Rating: c.Rating, Downloads: c.Downloads, Fmt: c.Format, Hi: c.Hi,
				HashMatch: c.HashMatch, Fps: c.Fps,
			})
			if err := b.Put([]byte(c.UID), raw); err != nil {
				return err
			}
		}
		return nil
	})
}

func (s *Store) GetBlob(uid string) []byte {
	var out []byte
	s.db.View(func(tx *bolt.Tx) error {
		out = append([]byte(nil), tx.Bucket(bktBlob).Get([]byte(uid))...)
		return nil
	})
	return out
}

func (s *Store) PutBlob(uid string, blob []byte) {
	s.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket(bktBlob).Put([]byte(uid), blob)
	})
}

func (s *Store) PurgeExpired() {
	cut := time.Now().UnixMilli()
	s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(bktSearch)
		// collect first, delete after: deleting while iterating skips keys in bbolt
		var dead [][]byte
		c := b.Cursor()
		for k, v := c.First(); k != nil; k, v = c.Next() {
			var e searchCacheEntry
			if json.Unmarshal(v, &e) != nil || e.Expires < cut {
				dead = append(dead, append([]byte(nil), k...))
			}
		}
		for _, k := range dead {
			if err := b.Delete(k); err != nil {
				return err
			}
		}
		return nil
	})
}

// ————— ذاكرة ساخنة: إصابة = 0ms بلا أي IO أو قفل قرص —————

type hotEntry struct {
	resp    SearchResponse
	expires time.Time
}

// HotCache خريطة LRU بسيطة في الذاكرة.
type HotCache struct {
	mu  sync.RWMutex
	m   map[string]hotEntry
	max int
}

func NewHotCache(max int) *HotCache {
	return &HotCache{m: make(map[string]hotEntry, 256), max: max}
}

func (h *HotCache) Get(k string) (*SearchResponse, bool) {
	h.mu.RLock()
	e, ok := h.m[k]
	h.mu.RUnlock()
	if !ok {
		return nil, false
	}
	if time.Now().After(e.expires) {
		h.mu.Lock()
		delete(h.m, k)
		h.mu.Unlock()
		return nil, false
	}
	return &e.resp, true
}

func (h *HotCache) Set(k string, resp SearchResponse, ttl time.Duration) {
	h.mu.Lock()
	if len(h.m) >= h.max { // evict an arbitrary entry (not true LRU)
		for key := range h.m {
			delete(h.m, key)
			break
		}
	}
	h.m[k] = hotEntry{resp: resp, expires: time.Now().Add(ttl)}
	h.mu.Unlock()
}

func (h *HotCache) Size() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.m)
}

// uid16 معرّف قصير ثابت للمرشّح (نفس صيغة الإصدارات السابقة: sha1(provider|ref)[:16]).
func uid16(provider, ref string) string {
	sum := sha1.Sum([]byte(provider + "|" + ref))
	return hex.EncodeToString(sum[:])[:16]
}
