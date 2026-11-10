package mobile

import (
	"encoding/json"
	"math"
	"time"

	"github.com/egoist/mygo/i18n"

	"leaflet/locales"
)

func Text(locale, key, args string) string {
	defer catch(nil)
	var values map[string]any
	if args != "" {
		if err := json.Unmarshal([]byte(args), &values); err != nil {
			return key
		}
	}
	for name, v := range values {
		switch v := v.(type) {
		case float64:
			if v == math.Trunc(v) && math.Abs(v) < 1<<53 {
				values[name] = int64(v)
			}
		case map[string]any:
			if ms, ok := v["date"].(float64); ok {
				values[name] = time.UnixMilli(int64(ms))
			}
		}
	}
	return locales.Catalog.Format(locale, key, values)
}

func When(locale string, at, now int64) string {
	defer catch(nil)
	if at == 0 {
		return ""
	}
	return locales.In(locale).When(time.UnixMilli(at), time.UnixMilli(now))
}

func Language(preferred string) string {
	defer catch(nil)
	var tags []string
	_ = json.Unmarshal([]byte(preferred), &tags)
	return i18n.Match(tags, locales.Catalog.Languages())
}

type language struct {
	Tag  string `json:"tag"`
	Name string `json:"name"`
}

func Languages() string {
	defer catch(nil)
	tags := locales.Catalog.Languages()
	out := make([]language, 0, len(tags))
	for _, tag := range tags {
		out = append(out, language{Tag: tag, Name: i18n.Autonym(tag)})
	}
	raw, _ := json.Marshal(out)
	return string(raw)
}
