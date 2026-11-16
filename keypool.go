// تدوير المفاتيح: أقل مفتاح استخداماً أولاً، وتوقف تلقائي عند 429/401/402/403 ثم تجربة التالي.
// العدّادات في الذاكرة (تُصفَّر عند إعادة التشغيل — نفس سلوك قرص Render المؤقت).
package main

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// ProviderHTTPError خطأ HTTP من المزوّد (مع Retry-After عند وجوده).
type ProviderHTTPError struct {
	Status     int
	Message    string
	RetryAfter float64
}

func (e *ProviderHTTPError) Error() string {
	return fmt.Sprintf("HTTP %d: %s", e.Status, e.Message)
}

// NoKeyAvailable لا يوجد مفتاح متاح (حصص مستنفدة).
type NoKeyAvailable struct{ Provider string }

func (e *NoKeyAvailable) Error() string { return "no key available: " + e.Provider }

func nextUtcMidnight() time.Time {
	now := time.Now().UTC()
	return time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 5, 0, now.Location()).AddDate(0, 0, 1)
}

// KeyPool تدوير مفاتيح مزوّد واحد.
type KeyPool struct {
	provider   string
	keys       []string
	dailyLimit int
	mu         sync.Mutex
	used       map[string]int
	cooldown   map[string]time.Time
	day        string
}

func NewKeyPool(provider string, keys []string, dailyLimit int) *KeyPool {
	uniq := make([]string, 0, len(keys))
	seen := map[string]bool{}
	for _, k := range keys {
		if !seen[k] {
			seen[k] = true
			uniq = append(uniq, k)
		}
	}
	return &KeyPool{
		provider: provider, keys: uniq, dailyLimit: dailyLimit,
		used: map[string]int{}, cooldown: map[string]time.Time{},
		day: time.Now().UTC().Format("2006-01-02"),
	}
}

func (p *KeyPool) rollDayLocked() {
	today := time.Now().UTC().Format("2006-01-02")
	if p.day != today {
		p.day = today
		p.used = map[string]int{}
		p.cooldown = map[string]time.Time{}
	}
}

// acquire أقل مفتاح استخداماً صالح (بلا قفل — يُستدعى مقفولاً).
func (p *KeyPool) acquire(exclude map[string]bool) (string, bool) {
	p.rollDayLocked()
	now := time.Now()
	best, bestUsed := "", -1
	for _, k := range p.keys {
		if exclude[k] {
			continue
		}
		if p.cooldown[k].After(now) || p.used[k] >= p.dailyLimit {
			continue
		}
		if bestUsed == -1 || p.used[k] < bestUsed {
			best, bestUsed = k, p.used[k]
		}
	}
	if bestUsed == -1 {
		return "", false
	}
	return best, true
}

// Call ينفّذ fn بمفتاح؛ عند 429/402/401/403 يوقف المفتاح ويجرّب التالي (مع احترام المهلة).
func (p *KeyPool) Call(ctx context.Context, countUse bool, fn func(key string) error) error {
	tried := map[string]bool{}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		p.mu.Lock()
		key, ok := p.acquire(tried)
		if ok && countUse {
			p.used[key]++
		}
		p.mu.Unlock()
		if !ok {
			return &NoKeyAvailable{Provider: p.provider}
		}
		tried[key] = true
		err := fn(key)
		if perr, isP := err.(*ProviderHTTPError); isP {
			switch perr.Status {
			case 401, 402, 403, 429:
				p.mu.Lock()
				if perr.Status == 402 || perr.Status == 429 {
					if perr.RetryAfter > 0 {
						p.cooldown[key] = time.Now().Add(time.Duration(perr.RetryAfter * float64(time.Second)))
					} else {
						p.cooldown[key] = nextUtcMidnight()
					}
				} else {
					p.cooldown[key] = time.Now().Add(6 * time.Hour)
				}
				p.mu.Unlock()
				continue // جرّب المفتاح التالي فوراً
			}
		}
		return err
	}
}

// Status حالة المفاتيح (بلا كشفها).
func (p *KeyPool) Status() map[string]any {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.rollDayLocked()
	now := time.Now()
	avail := 0
	used := 0
	for _, k := range p.keys {
		if !p.cooldown[k].After(now) && p.used[k] < p.dailyLimit {
			avail++
		}
		used += p.used[k]
	}
	return map[string]any{
		"keys":       len(p.keys),
		"available":  avail,
		"used_today": used,
	}
}

func (p *KeyPool) Size() int { return len(p.keys) }
