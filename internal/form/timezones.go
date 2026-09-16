package form

import (
	_ "embed"
	"slices"
	"strings"
)

// embeddedZoneTable is the IANA tz database's zone1970.tab, public domain,
// for hosts without tzdata. The host's own copy is preferred when present.
//
//go:embed zone1970.tab
var embeddedZoneTable string

// Paths read from the host.
const (
	hostZoneTablePath = "/usr/share/zoneinfo/zone1970.tab"
	hostTimezonePath  = "/etc/timezone"
)

// Timezones returns every zone name, UTC first and the rest sorted, from the
// host's zone1970.tab or the embedded copy.
func Timezones(host Host) []string {
	table := embeddedZoneTable
	if host.ReadFile != nil {
		if content, err := host.ReadFile(hostZoneTablePath); err == nil && len(content) > 0 {
			table = string(content)
		}
	}
	zones := parseZoneTable(table)
	if len(zones) == 0 {
		zones = parseZoneTable(embeddedZoneTable)
	}
	return append([]string{"UTC"}, zones...)
}

// parseZoneTable reads the zone names (third column) of a zone1970.tab.
func parseZoneTable(table string) []string {
	var zones []string
	for _, line := range strings.Split(table, "\n") {
		if strings.HasPrefix(line, "#") {
			continue
		}
		columns := strings.Split(line, "\t")
		if len(columns) < 3 || columns[2] == "" {
			continue
		}
		zones = append(zones, columns[2])
	}
	slices.Sort(zones)
	return slices.Compact(zones)
}

// HostTimezone returns the host's configured timezone when it is a known
// zone, and UTC otherwise.
func HostTimezone(host Host) string {
	if host.ReadFile == nil {
		return "UTC"
	}
	content, err := host.ReadFile(hostTimezonePath)
	if err != nil {
		return "UTC"
	}
	zone := strings.TrimSpace(string(content))
	if slices.Contains(Timezones(host), zone) {
		return zone
	}
	return "UTC"
}

// timezoneOptions renders the zone list as Select options.
func timezoneOptions(host Host) []Option {
	zones := Timezones(host)
	options := make([]Option, 0, len(zones))
	for _, zone := range zones {
		options = append(options, Option{Value: zone})
	}
	return options
}
