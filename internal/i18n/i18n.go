// Package i18n provides lightweight translation lookups. Locale files are JSON
// dictionaries keyed by string; missing keys fall back to English then the key.
package i18n

import (
	"strings"
	"sync"
)

// Bundles maps locale name -> key -> translation.
type Bundles struct {
	mu      sync.RWMutex
	locales map[string]map[string]string
	order   []string
}

var defaultBundles = &Bundles{locales: map[string]map[string]string{}}

// Register adds a locale bundle (used by the embedded locales below).
func Register(name string, dict map[string]string) {
	defaultBundles.mu.Lock()
	defer defaultBundles.mu.Unlock()
	if defaultBundles.locales[name] == nil {
		defaultBundles.order = append(defaultBundles.order, name)
	}
	defaultBundles.locales[name] = dict
}

// Locales lists available locale names (en first).
func Locales() []string {
	defaultBundles.mu.RLock()
	defer defaultBundles.mu.RUnlock()
	out := []string{}
	for _, l := range defaultBundles.order {
		if l != "en" {
			out = append(out, l)
		}
	}
	return append([]string{"en"}, out...)
}

// Normalize maps legacy locale codes (en_EN, ru_RU, ...) to bundle names.
func Normalize(lang string) string {
	lang = strings.TrimSpace(lang)
	if lang == "" {
		return "en"
	}
	l := strings.ReplaceAll(lang, "_", "-")
	parts := strings.SplitN(l, "-", 2)
	base := strings.ToLower(parts[0])
	if base == "" {
		return "en"
	}
	if base == "zh" && strings.Contains(l, "TW") {
		return "zh-TW"
	}
	return base
}

// Translate looks up key in lang, falling back to en then the key itself.
func Translate(lang, key string, args ...interface{}) string {
	name := Normalize(lang)
	defaultBundles.mu.RLock()
	defer defaultBundles.mu.RUnlock()
	if d, ok := defaultBundles.locales[name]; ok {
		if s, ok := d[key]; ok {
			return sprintf(s, args)
		}
	}
	if d, ok := defaultBundles.locales["en"]; ok {
		if s, ok := d[key]; ok {
			return sprintf(s, args)
		}
	}
	if len(args) > 0 {
		return sprintf(key, args)
	}
	return key
}

func sprintf(s string, args []interface{}) string {
	if len(args) == 0 {
		return s
	}
	// only format if the string has verbs
	if strings.Contains(s, "%") && !strings.Contains(s, "%%") {
		return fmtSprintf(s, args)
	}
	if strings.Contains(s, "%") {
		s = strings.ReplaceAll(s, "%%", "%")
	}
	return s
}
