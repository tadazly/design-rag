package core

import (
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

var (
	versionDateHeaderPattern  = regexp.MustCompile(`(?i)(版本|修订|修改|更新|变更)\s*(日期|时间)|\b(version|revision)\s*(date|time)\b`)
	versionFieldDatePattern   = regexp.MustCompile(`(?i)(版本|version)\]?\s*[:：=]\s*20\d{2}`)
	versionActionLinePattern  = regexp.MustCompile(`(?i)(初版|首版|修改|更新|复用|迭代|变更|revision|changelog)`)
	versionLeadingDatePattern = regexp.MustCompile(`(?i)^\s*([#>*-]+\s*)?(版本|version)\]?\s*[:：=]?\s*(20\d{6}|20\d{2}[-/.年_](0?[1-9]|1[0-2])([-/.月_](0?[1-9]|[12]\d|3[01])日?)?)(\D|$)`)
	versionCoverLinePattern   = regexp.MustCompile(`(?i)(版本|version)\s*([:：]|\s+)\s*v?\d+(\.\d+){0,3}\s+20\d{2}`)
	excelSerialPattern        = regexp.MustCompile(`(^|\D)(\d{5})(\D|$)`)
	versionDateSerialPattern  = regexp.MustCompile(`(?i)\[(版本|修订|修改|更新|变更)(日期|时间)\]\s*=\s*(\d{5})(\D|$)`)
	revisionDateFieldPattern  = regexp.MustCompile(`(?i)^(?:(修订|版本|修改|更新|变更)\s*)?(日期|时间)$`)
	cellLabelPrefixPattern    = regexp.MustCompile(`^[A-Z]+\d*(\[[^\]]*\])?=`)
	shortYearDatePattern      = regexp.MustCompile(`(^|\D)(0?[1-9]|1[0-2])/(0?[1-9]|[12]\d|3[01])/(\d{2})(\D|$)`)
)

func validDate(year, month, day int) (time.Time, bool) {
	if year < 2000 || month < 1 || month > 12 || day < 1 || day > 31 {
		return time.Time{}, false
	}
	value := time.Date(year, time.Month(month), day, 0, 0, 0, 0, time.UTC)
	if value.Year() != year || int(value.Month()) != month || value.Day() != day {
		return time.Time{}, false
	}
	minimum := time.Date(2000, time.January, 1, 0, 0, 0, 0, time.UTC)
	if value.Before(minimum) || value.After(time.Now().UTC().Add(366*24*time.Hour)) {
		return time.Time{}, false
	}
	return value, true
}

// Go regexp does not support the lookbehind/lookahead used by dates.ts. The
// shared patterns therefore consume their non-digit boundaries. Reusing the
// trailing boundary as the next search start preserves adjacent matches such
// as 20260101_20260819 without weakening the digit-boundary rule.
func overlappingDateMatches(pattern *regexp.Regexp, value string, trailingGroup int) [][]string {
	result := make([][]string, 0)
	for offset := 0; offset <= len(value); {
		indices := pattern.FindStringSubmatchIndex(value[offset:])
		if indices == nil {
			break
		}
		match := make([]string, len(indices)/2)
		for group := range match {
			start := indices[group*2]
			end := indices[group*2+1]
			if start >= 0 && end >= start {
				match[group] = value[offset+start : offset+end]
			}
		}
		result = append(result, match)

		nextOffset := offset + indices[1]
		boundaryIndex := trailingGroup * 2
		if boundaryIndex+1 < len(indices) {
			start := indices[boundaryIndex]
			end := indices[boundaryIndex+1]
			if start >= 0 && end > start {
				nextOffset = offset + start
			}
		}
		if nextOffset <= offset {
			nextOffset = offset + 1
		}
		offset = nextOffset
	}
	return result
}

func findDates(value string) []time.Time {
	found := map[int64]time.Time{}
	for _, match := range overlappingDateMatches(dateSeparated, value, 6) {
		if len(match) < 7 {
			continue
		}
		year, _ := strconv.Atoi(match[2])
		month, _ := strconv.Atoi(match[3])
		day := 1
		if match[5] != "" {
			day, _ = strconv.Atoi(match[5])
		}
		if date, ok := validDate(year, month, day); ok {
			found[date.UnixMilli()] = date
		}
	}
	for _, match := range overlappingDateMatches(dateCompact, value, 5) {
		if len(match) < 6 {
			continue
		}
		year, _ := strconv.Atoi(match[2])
		month, _ := strconv.Atoi(match[3])
		day, _ := strconv.Atoi(match[4])
		if date, ok := validDate(year, month, day); ok {
			found[date.UnixMilli()] = date
		}
	}
	result := make([]time.Time, 0, len(found))
	for _, value := range found {
		result = append(result, value)
	}
	sort.Slice(result, func(left, right int) bool {
		return result[left].Before(result[right])
	})
	return result
}

func findShortYearDates(value string) []time.Time {
	found := map[int64]time.Time{}
	for _, match := range shortYearDatePattern.FindAllStringSubmatch(value, -1) {
		month, monthErr := strconv.Atoi(match[2])
		day, dayErr := strconv.Atoi(match[3])
		year, yearErr := strconv.Atoi(match[4])
		if monthErr != nil || dayErr != nil || yearErr != nil {
			continue
		}
		if date, ok := validDate(2000+year, month, day); ok {
			found[date.UnixMilli()] = date
		}
	}
	return dateValues(found)
}

func latestDate(values []time.Time) *time.Time {
	var latest time.Time
	for _, value := range values {
		if value.After(latest) {
			latest = value
		}
	}
	if latest.IsZero() {
		return nil
	}
	return &latest
}

func addDates(target map[int64]time.Time, values []time.Time) {
	for _, value := range values {
		target[value.UnixMilli()] = value
	}
}

func dateValues(target map[int64]time.Time) []time.Time {
	result := make([]time.Time, 0, len(target))
	for _, value := range target {
		result = append(result, value)
	}
	return result
}

func excelSerialDate(serial int) (time.Time, bool) {
	if serial <= 0 {
		return time.Time{}, false
	}
	value := time.Date(1899, time.December, 30, 0, 0, 0, 0, time.UTC).AddDate(0, 0, serial)
	return validDate(value.Year(), int(value.Month()), value.Day())
}

func findExcelSerialDates(value string, pattern *regexp.Regexp, serialGroup int) []time.Time {
	found := map[int64]time.Time{}
	for _, match := range pattern.FindAllStringSubmatch(value, -1) {
		if len(match) <= serialGroup {
			continue
		}
		serial, err := strconv.Atoi(match[serialGroup])
		if err != nil {
			continue
		}
		if date, ok := excelSerialDate(serial); ok {
			found[date.UnixMilli()] = date
		}
	}
	return dateValues(found)
}

func collectBlockDateEvidence(blocks []Block) []DateEvidence {
	evidence := make([]DateEvidence, 0, 8)
	add := func(values []time.Time, kind, locator string) {
		for _, value := range values {
			duplicate := false
			for _, existing := range evidence {
				if existing.TimestampMS == value.UnixMilli() && existing.Kind == kind && existing.Locator == locator {
					duplicate = true
					break
				}
			}
			if !duplicate {
				evidence = append(evidence, DateEvidence{TimestampMS: value.UnixMilli(), Strength: "strong", Kind: kind, Locator: locator})
			}
		}
	}
	for _, block := range blocks {
		lines := strings.Split(block.Text, "\n")
		rows := make([][]string, 0, len(lines))
		for _, line := range lines {
			parts := strings.Split(line, "|")
			for index := range parts {
				parts[index] = strings.TrimSpace(parts[index])
			}
			rows = append(rows, parts)
		}
		for rowIndex, header := range rows {
			dateColumn := -1
			for column, cell := range header {
				field := strings.TrimSpace(cellLabelPrefixPattern.ReplaceAllString(cell, ""))
				if revisionDateFieldPattern.MatchString(field) {
					dateColumn = column
					break
				}
			}
			if dateColumn < 0 {
				continue
			}
			for _, dataRow := range rows[rowIndex+1:] {
				if dateColumn < len(dataRow) {
					add(findDates(dataRow[dateColumn]), "revision_table", block.Locator)
				}
			}
			break
		}
		for _, line := range lines {
			if prefix := versionLeadingDatePattern.FindString(line); prefix != "" {
				add(findDates(prefix), "leading_version", block.Locator)
			}
		}
	}
	return evidence
}

func versionBlockEligible(sourceKind string, block Block) bool {
	if block.SectionType != "version_history" {
		return false
	}
	if sourceKind != "table" {
		return true
	}
	for _, heading := range block.HeadingPath {
		switch strings.ToLower(strings.TrimSpace(heading)) {
		case "版本修改记录", "版本记录", "修改记录", "changelog", "revision history":
			return true
		}
	}
	return false
}

type versionLogDates struct {
	strong *time.Time
	weak   *time.Time
}

func findVersionLogDates(sourceKind string, document ExtractedDocument) versionLogDates {
	const maxBlocks = 300
	const maxRelevantLines = 2_000

	strongDates := map[int64]time.Time{}
	weakDates := map[int64]time.Time{}
	selected := 0
	relevantLines := 0
	inDateTable := false
	for _, block := range document.Blocks {
		if !versionBlockEligible(sourceKind, block) {
			continue
		}
		if selected >= maxBlocks || relevantLines >= maxRelevantLines {
			break
		}
		selected++
		// TypeScript passes `${headingPath} ${block.text}` into one shared
		// line stream. Preserve that exact boundary and table state so a sheet
		// name/date plus its first row cannot diverge between engines.
		lines := strings.Split(strings.TrimSpace(strings.Join(block.HeadingPath, " / ")+" "+block.Text), "\n")
		for _, line := range lines {
			if relevantLines >= maxRelevantLines {
				break
			}
			if versionDateHeaderPattern.MatchString(line) {
				inDateTable = true
				addDates(strongDates, findDates(line))
				// Some native OOXML projections retain date-formatted header cells as
				// Excel serials. Only interpret them when the same row explicitly
				// identifies a version/revision date field, never in arbitrary text.
				addDates(strongDates, findExcelSerialDates(line, excelSerialPattern, 2))
				relevantLines++
				continue
			}
			if inDateTable {
				addDates(strongDates, findDates(line))
				addDates(strongDates, findExcelSerialDates(line, versionDateSerialPattern, 3))
				relevantLines++
				continue
			}
			if versionFieldDatePattern.MatchString(line) || versionActionLinePattern.MatchString(line) {
				addDates(strongDates, findDates(line))
				relevantLines++
				continue
			}
			if leadingVersionDate := versionLeadingDatePattern.FindString(line); leadingVersionDate != "" {
				// Only the date immediately following the version marker is
				// authoritative. A later campaign date on the same line must not
				// rescue an invalid leading date or turn version marketing into a log.
				addDates(strongDates, findDates(leadingVersionDate))
				relevantLines++
				continue
			}
			if versionCoverLinePattern.MatchString(line) {
				addDates(weakDates, findDates(line))
				relevantLines++
			}
		}
	}
	return versionLogDates{
		strong: latestDate(dateValues(strongDates)),
		weak:   latestDate(dateValues(weakDates)),
	}
}

func ResolveEffectiveDate(candidate Candidate, document ExtractedDocument) DateResolution {
	base := strings.TrimSuffix(filepath.Base(candidate.AbsolutePath), filepath.Ext(candidate.AbsolutePath))
	filenameDate := latestDate(findDates(base))
	pathDate := latestDate(findDates(filepath.Dir(candidate.AbsolutePath)))
	versionDates := findVersionLogDates(candidate.SourceKind, document)
	if document.DateEvidence != nil {
		strong := map[int64]time.Time{}
		weak := map[int64]time.Time{}
		for _, evidence := range document.DateEvidence {
			value := time.UnixMilli(evidence.TimestampMS).UTC()
			if _, ok := validDate(value.Year(), int(value.Month()), value.Day()); !ok {
				continue
			}
			if evidence.Strength == "weak" {
				weak[value.UnixMilli()] = value
			} else {
				strong[value.UnixMilli()] = value
			}
		}
		versionDates = versionLogDates{strong: latestDate(dateValues(strong)), weak: latestDate(dateValues(weak))}
	}
	if filenameDate != nil {
		return DateResolution{EffectiveUpdatedAtMS: filenameDate.UnixMilli(), DateSource: "filename"}
	}
	if versionDates.strong != nil {
		return DateResolution{EffectiveUpdatedAtMS: versionDates.strong.UnixMilli(), DateSource: "version_log"}
	}
	if pathDate != nil {
		return DateResolution{EffectiveUpdatedAtMS: pathDate.UnixMilli(), DateSource: "path"}
	}
	if versionDates.weak != nil {
		return DateResolution{EffectiveUpdatedAtMS: versionDates.weak.UnixMilli(), DateSource: "version_log"}
	}
	if document.EmbeddedModifiedAt != nil {
		value := document.EmbeddedModifiedAt.UTC()
		minimum := time.Date(2000, time.January, 1, 0, 0, 0, 0, time.UTC)
		if !value.Before(minimum) && !value.After(time.Now().UTC().Add(366*24*time.Hour)) {
			return DateResolution{EffectiveUpdatedAtMS: value.UnixMilli(), DateSource: "embedded_modified"}
		}
	}
	return DateResolution{EffectiveUpdatedAtMS: candidate.FilesystemMtimeMS, DateSource: "filesystem_mtime"}
}

func shouldReadEmbeddedModified(filePath string) bool {
	base := strings.TrimSuffix(filepath.Base(filePath), filepath.Ext(filePath))
	return latestDate(findDates(base)) == nil && latestDate(findDates(filepath.Dir(filePath))) == nil
}
