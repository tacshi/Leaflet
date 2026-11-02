package mobile

import (
	"encoding/json"
	"math"
	"time"

	"github.com/egoist/mygo/i18n"

	"leaflet/locales"
)

// Text says the message key of the app's catalogs (locales/) in the
// language locale, with args, a JSON object of its placeholders: strings,
// numbers, and dates as {"date": milliseconds since 1970}. The iOS app
// says the messages it shares with the desktop through it, so both say
// them alike, plurals and dates included.
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
			// Whole numbers count, as plurals take them.
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

// Language returns the language of the app's catalogs that suits the
// languages the user reads best, given as a JSON array of BCP 47 tags in
// the order they prefer them.
func Language(preferred string) string {
	defer catch(nil)
	var tags []string
	_ = json.Unmarshal([]byte(preferred), &tags)
	return i18n.Match(tags, locales.Catalog.Languages())
}

// language is a language the app can show, as Languages lists it.
type language struct {
	Tag string `json:"tag"`
	// Name is the language's name in itself.
	Name string `json:"name"`
}

// Languages returns the languages of the app's catalogs, as JSON, each
// with its name in itself, as the desktop's settings list them.
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
