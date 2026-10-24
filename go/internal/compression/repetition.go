package compression

import (
	"fmt"
	"regexp"
	"strings"
)

// Consecutive identical-line run-length elision.
//
// Adapted from caveman engine/compressors/repetition.go
// (https://github.com/JuliusBrussee/caveman) — MIT License
// Copyright (c) 2026 Julius Brussee
//
// Default: exact-match only (minRun=3). Digit-masked comparison is opt-in
// (MaskDigits: true) so request IDs / counters are not over-collapsed.
// Marker lines are never themselves collapsed (idempotent on re-entry).

// CollapseRepetitionOptions tunes CollapseRepetition.
// port of src/compression/repetition.ts:CollapseRepetitionOptions
type CollapseRepetitionOptions struct {
	// MaskDigits collapses lines that differ only in digit runs. Default false.
	MaskDigits bool
	// MinRun is the minimum consecutive identical lines to collapse. nil means 3.
	MinRun *int
}

// port of src/compression/repetition.ts:DEFAULT_MIN_RUN
const defaultMinRun = 3

// port of src/compression/repetition.ts:MARKER_PREFIX
const markerPrefix = "… "

// port of src/compression/repetition.ts:MARKER_RE
var markerRE = regexp.MustCompile(`^… \d+ identical lines elided …$`)

// digitsRE is the inline /\d+/g regex in lineKey.
var digitsRE = regexp.MustCompile(`\d+`)

// RepetitionMarker renders the elision marker for elided lines.
// port of src/compression/repetition.ts:repetitionMarker
func RepetitionMarker(elided int) string {
	return fmt.Sprintf("… %d identical lines elided …", elided)
}

// IsRepetitionMarker reports whether line already is an elision marker.
// port of src/compression/repetition.ts:isRepetitionMarker
func IsRepetitionMarker(line string) bool {
	return markerRE.MatchString(line) ||
		(strings.HasPrefix(line, markerPrefix) && strings.HasSuffix(line, " identical lines elided …"))
}

// lineKey normalizes a line for comparison, optionally masking digit runs.
// port of src/compression/repetition.ts:lineKey
func lineKey(line string, maskDigits bool) string {
	if maskDigits {
		return digitsRE.ReplaceAllString(line, "#")
	}
	return line
}

// CollapseRepetition elides runs of identical lines using an elision marker.
// port of src/compression/repetition.ts:collapseRepetition
func CollapseRepetition(text string, options ...CollapseRepetitionOptions) string {
	minRun := defaultMinRun
	maskDigits := false
	if len(options) > 0 {
		if options[0].MinRun != nil {
			minRun = *options[0].MinRun
		}
		maskDigits = options[0].MaskDigits
	}

	lines := strings.Split(text, "\n")
	out := make([]string, 0, len(lines))
	collapsed := false

	i := 0
	for i < len(lines) {
		current := lines[i]
		key := lineKey(current, maskDigits)
		j := i + 1
		for j < len(lines) && lineKey(lines[j], maskDigits) == key {
			j++
		}
		runLen := j - i
		out = append(out, current)
		if runLen >= minRun &&
			strings.TrimSpace(current) != "" &&
			!IsRepetitionMarker(current) {
			out = append(out, RepetitionMarker(runLen-1))
			collapsed = true
		} else {
			for k := i + 1; k < j; k++ {
				out = append(out, lines[k])
			}
		}
		i = j
	}

	if !collapsed {
		return text
	}
	result := strings.Join(out, "\n")
	if utf16Length(result) < utf16Length(text) {
		return result
	}
	return text
}
