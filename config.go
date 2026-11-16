// الإعدادات من متغيرات البيئة — تُقرأ مرة واحدة عند الإقلاع.
package main

import (
	"encoding/json"
	"log"
	"os"
	"strconv"
	"strings"
	"time"
)

// OSAccount حساب OpenSubtitles: مفتاح API + حساب لتسجيل الدخول عند التنزيل.
type OSAccount struct {
	APIKey   string
	Username string
	Password string
}

type Config struct {
	AppToken string

	OSAccounts      []OSAccount
	OSDailyLimit    int
	SubDLKeys       []string
	SubDLDailyLimit int

	// روابط قابلة للاستبدال (للاختبار بسرفرات وهمية)
	OSBase    string
	SubDLBase string
	SubDLDl   string

	// السرعة: ميزانية زمنية صارمة + كاش طويل + كاش سلبي قصير
	SearchTimeout   time.Duration
	RequestTimeout  time.Duration
	CacheTTL        time.Duration
	NegCacheTTL     time.Duration
	RateLimitPerMin int

	DBPath    string
	UserAgent string

	KeepAliveURL      string
	KeepAliveInterval time.Duration

	Port int
}

func envStr(k, def string) string {
	if v := strings.TrimSpace(os.Getenv(k)); v != "" {
		return v
	}
	return def
}

func envInt(k string, def int) int {
	if v := strings.TrimSpace(os.Getenv(k)); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return def
}

func LoadConfig() Config {
	return Config{
		AppToken:        envStr("APP_TOKEN", ""),
		OSAccounts:      parseAccounts(os.Getenv("OPENSUBTITLES_ACCOUNTS")),
		OSDailyLimit:    envInt("OPENSUBTITLES_DAILY_LIMIT", 200),
		SubDLKeys:       splitList(os.Getenv("SUBDL_API_KEYS")),
		SubDLDailyLimit: envInt("SUBDL_DAILY_LIMIT", 500),
		OSBase:          envStr("OS_BASE", "https://api.opensubtitles.com/api/v1"),
		SubDLBase:       envStr("SUBDL_BASE", "https://api.subdl.com/api/v1/subtitles"),
		SubDLDl:         envStr("SUBDL_DL", "https://dl.subdl.com"),
		SearchTimeout:   time.Duration(envInt("SEARCH_TIMEOUT_MS", 6000)) * time.Millisecond,
		RequestTimeout:  time.Duration(envInt("REQUEST_TIMEOUT_MS", 15000)) * time.Millisecond,
		CacheTTL:        time.Duration(envInt("CACHE_TTL_HOURS", 720)) * time.Hour,
		NegCacheTTL:     time.Duration(envInt("NEG_CACHE_TTL_HOURS", 3)) * time.Hour,
		RateLimitPerMin: envInt("RATE_LIMIT_PER_MIN", 60),
		DBPath:          envStr("DB_PATH", "data/cache.db"),
		UserAgent:       envStr("USER_AGENT", "SubtitleServer/3.0-go"),
		KeepAliveURL: strings.TrimRight(envStr("KEEP_ALIVE_URL",
			strings.TrimRight(os.Getenv("RENDER_EXTERNAL_URL"), "/")), "/"),
		KeepAliveInterval: time.Duration(envInt("KEEP_ALIVE_INTERVAL_MS", 600000)) * time.Millisecond,
		Port:              envInt("PORT", 8000),
	}
}

func splitList(s string) []string {
	out := []string{}
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// parseAccounts يقرأ قائمة JSON من الحسابات
// [{"api_key":"...","username":"...","password":"..."}]
func parseAccounts(raw string) []OSAccount {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	var arr []map[string]string
	if err := json.Unmarshal([]byte(raw), &arr); err != nil {
		log.Printf("OPENSUBTITLES_ACCOUNTS: صيغة غير صالحة: %v", err)
		return nil
	}
	out := make([]OSAccount, 0, len(arr))
	for _, a := range arr {
		if a["api_key"] == "" || a["username"] == "" || a["password"] == "" {
			continue
		}
		out = append(out, OSAccount{APIKey: a["api_key"], Username: a["username"], Password: a["password"]})
	}
	return out
}
