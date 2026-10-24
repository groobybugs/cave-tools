package compression

import (
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/groobybugs/cave-tools/go/internal/jsre"
)

// CodebookEntry is one learned boilerplate line.
// port of src/compression/codebook.ts:CodebookEntry
type codebookEntry struct {
	ID        string
	Pattern   string
	Frequency int
}

// port of src/compression/codebook.ts:MIN_PATTERN_LENGTH
const minPatternLength = 10

// port of src/compression/codebook.ts:MIN_DOCUMENT_FREQUENCY
const minDocumentFrequency = 3

// port of src/compression/codebook.ts:MAX_ENTRIES
const maxEntries = 50

// port of src/compression/codebook.ts:MAX_TOTAL_LINES
const maxTotalLines = 50_000

// codebookFile is one file fed to the codebook.
// port of src/compression/codebook.ts:Codebook.files
type codebookFile struct {
	Path    string
	Content string
}

// Codebook learns lines shared by at least three files and replaces them with
// short references.
// port of src/compression/codebook.ts:Codebook
type Codebook struct {
	mu          sync.Mutex
	entries     []codebookEntry
	patternToID map[string]string
	files       []codebookFile
	nextID      int
}

// AddFile feeds one file into the codebook; the third file triggers a rebuild.
// port of src/compression/codebook.ts:addFile
func (c *Codebook) AddFile(path string, content string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.files = append(c.files, codebookFile{Path: path, Content: content})
	if len(c.files) >= minDocumentFrequency {
		c.rebuild()
	}
}

// rebuild recomputes the entries from every file seen so far.
// port of src/compression/codebook.ts:rebuild
func (c *Codebook) rebuild() {
	totalLines := 0
	for _, f := range c.files {
		totalLines += len(strings.Split(f.Content, "\n"))
	}
	if totalLines > maxTotalLines {
		return
	}

	docFreq := make(map[string]map[int]struct{})
	termFreq := make(map[string]int)
	var patternsInOrder []string

	for docIdx := 0; docIdx < len(c.files); docIdx++ {
		content := c.files[docIdx].Content
		seenInDoc := make(map[string]struct{})

		for _, line := range strings.Split(content, "\n") {
			normalized := normalizeLine(line)
			if utf16Length(normalized) < minPatternLength {
				continue
			}

			termFreq[normalized]++

			if _, seen := seenInDoc[normalized]; !seen {
				seenInDoc[normalized] = struct{}{}
				docs, ok := docFreq[normalized]
				if !ok {
					docs = make(map[int]struct{})
					docFreq[normalized] = docs
					patternsInOrder = append(patternsInOrder, normalized)
				}
				docs[docIdx] = struct{}{}
			}
		}
	}

	type candidate struct {
		pattern string
		freq    int
		df      int
	}
	candidates := make([]candidate, 0, len(patternsInOrder))
	for _, pattern := range patternsInOrder {
		docs := docFreq[pattern]
		if len(docs) >= minDocumentFrequency {
			candidates = append(candidates, candidate{
				pattern: pattern,
				freq:    termFreq[pattern],
				df:      len(docs),
			})
		}
	}

	// Array.prototype.sort is stable; ties keep first-seen order.
	sort.SliceStable(candidates, func(i, j int) bool {
		return candidates[i].freq > candidates[j].freq
	})

	c.entries = nil
	c.patternToID = make(map[string]string)
	c.nextID = 0

	limit := len(candidates)
	if limit > maxEntries {
		limit = maxEntries
	}
	for _, candidate := range candidates[:limit] {
		id := fmt.Sprintf("§%d", c.nextID)
		c.nextID++
		c.patternToID[candidate.pattern] = id
		c.entries = append(c.entries, codebookEntry{
			ID:        id,
			Pattern:   candidate.pattern,
			Frequency: candidate.freq,
		})
	}
}

// Compress replaces known lines with their codebook references.
// port of src/compression/codebook.ts:compress
func (c *Codebook) Compress(content string) (text string, refsUsed []string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if len(c.entries) == 0 {
		return content, []string{}
	}

	result := make([]string, 0)
	refsUsed = make([]string, 0)
	for _, line := range strings.Split(content, "\n") {
		normalized := normalizeLine(line)
		id, ok := c.patternToID[normalized]
		if ok {
			if !containsString(refsUsed, id) {
				refsUsed = append(refsUsed, id)
			}
			result = append(result, "["+id+"]")
		} else {
			result = append(result, line)
		}
	}

	return strings.Join(result, "\n"), refsUsed
}

// FormatLegend renders the legend for the referenced entries.
// port of src/compression/codebook.ts:formatLegend
func (c *Codebook) FormatLegend(refsUsed []string) string {
	c.mu.Lock()
	defer c.mu.Unlock()

	if len(refsUsed) == 0 {
		return ""
	}

	lines := []string{"§CODEBOOK:"}
	for _, entry := range c.entries {
		if containsString(refsUsed, entry.ID) {
			short := entry.Pattern
			if utf16Length(short) > 60 {
				short = utf16Prefix(short, 57) + "..."
			}
			lines = append(lines, "  "+entry.ID+"="+short)
		}
	}
	return strings.Join(lines, "\n")
}

// Size reports how many entries the codebook holds.
// port of src/compression/codebook.ts:size
func (c *Codebook) Size() int {
	c.mu.Lock()
	defer c.mu.Unlock()

	return len(c.entries)
}

// normalizeLine collapses whitespace runs and trims: the TS implementation is
// line.split(/\s+/).filter(Boolean).join(" "), so it uses JS whitespace
// (NBSP and U+FEFF included) rather than strings.Fields.
// port of src/compression/codebook.ts:normalizeLine
func normalizeLine(line string) string {
	fields := make([]string, 0, 8)
	for _, part := range jsre.SplitWS(line) {
		if part != "" {
			fields = append(fields, part)
		}
	}
	return strings.Join(fields, " ")
}

// containsString mirrors Array.prototype.includes for the short ref lists.
func containsString(items []string, want string) bool {
	for _, item := range items {
		if item == want {
			return true
		}
	}
	return false
}

// utf16Length is JavaScript's String.prototype.length: UTF-16 code units.
// TS counts code units, not bytes or runes.
func utf16Length(s string) int {
	n := 0
	for _, r := range s {
		if r > 0xFFFF {
			n += 2
		} else {
			n++
		}
	}
	return n
}

// utf16Prefix mirrors String.prototype.slice(0, n) over UTF-16 code units.
// Slicing through a surrogate pair yields U+FFFD once serialized, like Node.
func utf16Prefix(s string, n int) string {
	units := 0
	for i, r := range s {
		width := 1
		if r > 0xFFFF {
			width = 2
		}
		if units+width > n {
			if units < n {
				return s[:i] + "\uFFFD"
			}
			return s[:i]
		}
		units += width
	}
	return s
}

// globalCodebook is the process-wide codebook.
// port of src/compression/codebook.ts:globalCodebook
var globalCodebook = &Codebook{patternToID: make(map[string]string)}

// AddCodebookFile feeds a file into the global codebook.
// port of src/compression/codebook.ts:addCodebookFile
func AddCodebookFile(path string, content string) {
	globalCodebook.AddFile(path, content)
}

// CompressWithCodebook compresses content with the global codebook and returns
// the legend for the references it used.
// port of src/compression/codebook.ts:compressWithCodebook
func CompressWithCodebook(content string) (text string, legend string, refsUsed []string) {
	text, refsUsed = globalCodebook.Compress(content)
	if len(refsUsed) >= 1 {
		legend = globalCodebook.FormatLegend(refsUsed)
	}
	return text, legend, refsUsed
}

// GetCodebookSize reports the global codebook's entry count.
// port of src/compression/codebook.ts:getCodebookSize
func GetCodebookSize() int {
	return globalCodebook.Size()
}
