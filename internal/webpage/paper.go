package webpage

import (
	"slices"
	"strings"

	"golang.org/x/text/language"
)

// letterRegions print on Letter paper, as CLDR has it; the rest of the
// world prints on A4.
var letterRegions = []string{"BZ", "CA", "CL", "CO", "CR", "GT", "MX", "NI", "PA", "PH", "PR", "SV", "US", "VE"}

// Paper is the size, in inches, of the paper a region prints on, the
// region a code such as "US"; "" or one not known prints on A4.
func Paper(region string) (width, height float64) {
	if slices.Contains(letterRegions, strings.ToUpper(region)) {
		return 8.5, 11
	}
	return 8.27, 11.69
}

// RegionOf is the region a BCP 47 locale, as "en-US", names, or "" for
// one that names none.
func RegionOf(locale string) string {
	tag, err := language.Parse(locale)
	if err != nil {
		return ""
	}
	if r, conf := tag.Region(); conf == language.Exact {
		return r.String()
	}
	return ""
}
