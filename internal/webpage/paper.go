package webpage

import (
	"slices"
	"strings"

	"golang.org/x/text/language"
)

var letterRegions = []string{"BZ", "CA", "CL", "CO", "CR", "GT", "MX", "NI", "PA", "PH", "PR", "SV", "US", "VE"}

func Paper(region string) (width, height float64) {
	if slices.Contains(letterRegions, strings.ToUpper(region)) {
		return 8.5, 11
	}
	return 8.27, 11.69
}

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
