// Collation parity with String.prototype.localeCompare.
//
// TypeScript sorts names with `a.localeCompare(b)` at four sites: the fallback
// ripgrep file finder (src/runtime/ripgrep.ts:84), the rtk passthrough
// tie-break (src/compression/utils.ts:570), the directory listing in
// src/tools/read.ts:311 and src/tools/ls.ts:66. V8 implements that with the
// process default ICU collator, so the Go port uses the same locale data via
// golang.org/x/text/collate (the only added dependency) instead of the
// hand-rolled ASCII table this package used to carry.
//
// # Locale selection
//
// Node never reads LANG directly; the default locale comes from ICU's
// uprv_getDefaultLocaleID (icu4c/source/common/putil.cpp), whose POSIX path
// reads the environment chain LC_ALL, LC_MESSAGES, LANG (LC_COLLATE is ignored
// for collation) and maps "C"/"POSIX" — including "C.UTF-8" once the codeset
// is stripped — to en_US_POSIX. When none of the three variables is set, the
// chain falls back to en_US_POSIX as well, which V8 reports as "en-US". A
// variable that is set but empty wins over the ones after it and yields the
// empty (root) locale, which V8 reports as "und"; that is the one place the
// ICU code diverges from plain POSIX precedence, and locale_env.json records
// it. TestResolveLocaleMatchesNode replays the measured matrix from Node 24.19
// (ICU 78.3).
//
// # Thread safety
//
// collate.Collator keeps mutable state, so comparisons borrow a collator from a
// per-locale sync.Pool. Sorts precompute keys with a borrowed collator and reuse
// its Buffer, then compare those keys as plain strings.
package runtime

import (
	"bytes"
	"os"
	"sort"
	"strings"
	"sync"

	"golang.org/x/text/collate"
	"golang.org/x/text/language"
)

// asciiCollationOrder is the ICU primary order of printable ASCII, taken from
// Node's localeCompare: punctuation, then digits, then letters, with each
// letter's upper/lower pair sharing a primary weight.
const asciiCollationOrder = " _-,;:!?.'\"()[]{}@*/\\&#%`^+<=>|~$0123456789abcdefghijklmnopqrstuvwxyz"

// asciiPrimary maps an ASCII byte to its primary weight.
var asciiPrimary = func() [128]int {
	var table [128]int
	for index := range table {
		table[index] = -1
	}
	for index := 0; index < len(asciiCollationOrder); index++ {
		char := asciiCollationOrder[index]
		table[char] = index
		if char >= 'a' && char <= 'z' {
			table[char-'a'+'A'] = index
		}
	}
	return table
}()

// printableASCII reports whether every byte of s is printable ASCII. Names in
// this range can be compared by asciiCompare when the locale keeps the root
// ASCII order (see asciiFastPath); TestASCIICollationMatchesCollator proves the
// two agree for every such locale.
func printableASCII(s string) bool {
	for index := 0; index < len(s); index++ {
		if s[index] < 0x20 || s[index] > 0x7e {
			return false
		}
	}
	return true
}

// asciiFastPath reports whether the locale leaves the root ASCII order intact,
// so asciiCompare may stand in for the collator. It is enabled only for
// English and undetermined locales: CLDR tailors the ASCII letter order in
// several others (Turkish "I" folds onto dotless "ı", and aa/az/ee/et/haw/lt/ssy
// reorder letters outright), and TestASCIICollationMatchesCollator walks
// collate.Supported() to keep this list honest. Every other locale simply pays
// for the collator; the order stays correct either way.
func asciiFastPath(tag string) bool {
	if tag == "und" || tag == "en" {
		return true
	}
	// en-US-u-va-posix is the POSIX collation variant, which reorders
	// punctuation; resolveLocale never produces it, but do not trust the prefix.
	return strings.HasPrefix(tag, "en-") && !strings.Contains(tag, "posix")
}

// asciiWeight is asciiPrimary with a deterministic position for bytes outside
// printable ASCII. LocaleCompareIn only reaches asciiCompare with printable
// strings; the fallback keeps misuse from panicking.
func asciiWeight(char byte) int {
	if char < 128 {
		if weight := asciiPrimary[char]; weight >= 0 {
			return weight
		}
	}
	return 1000 + int(char)
}

// asciiCompare orders two printable-ASCII strings: ICU primary weights first,
// then length, then the tertiary (lowercase-before-uppercase) level.
func asciiCompare(a, b string) int {
	shared := len(a)
	if len(b) < shared {
		shared = len(b)
	}
	for index := 0; index < shared; index++ {
		leftWeight := asciiWeight(a[index])
		rightWeight := asciiWeight(b[index])
		if leftWeight != rightWeight {
			return compareInt(leftWeight, rightWeight)
		}
	}
	if len(a) != len(b) {
		return compareInt(len(a), len(b))
	}
	for index := 0; index < len(a); index++ {
		if a[index] == b[index] {
			continue
		}
		return compareInt(int(upperPriority(a[index])), int(upperPriority(b[index])))
	}
	return 0
}

// upperPriority is the tertiary weight of an ASCII letter: lowercase first.
func upperPriority(char byte) int {
	if char >= 'A' && char <= 'Z' {
		return 1
	}
	return 0
}

func compareInt(a int, b int) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

// localeTag caches the collation locale derived from the process environment.
var (
	localeTagOnce sync.Once
	localeTag     string
)

// LocaleTag returns the collation locale the default ICU collator would use,
// as a BCP 47-style tag ("en-US", "sv-SE", "und", ...).
func LocaleTag() string {
	localeTagOnce.Do(func() { localeTag = resolveLocale(os.LookupEnv) })
	return localeTag
}

// resolveLocale mirrors ICU's POSIX default-locale resolution. `lookup`
// reports whether a variable is set; ICU distinguishes an absent variable from
// one that is set to the empty string, which it keeps as a locale of "" (root)
// rather than skipping like the C library would. When no variable is set at
// all the chain defaults to en_US_POSIX.
func resolveLocale(lookup func(string) (string, bool)) string {
	posix, found := "", false
	for _, key := range []string{"LC_ALL", "LC_MESSAGES", "LANG"} {
		if value, ok := lookup(key); ok {
			posix, found = value, true
			break
		}
	}
	if !found || posix == "C" || posix == "POSIX" {
		posix = "en_US_POSIX"
	}
	return canonicalLocale(posix)
}

// canonicalLocale turns a POSIX locale name into the BCP 47 tag V8 reports and
// the x/text language package accepts.
func canonicalLocale(posix string) string {
	base := posix
	if index := strings.IndexAny(base, ".@"); index >= 0 {
		base = base[:index]
	}
	variant := ""
	if index := strings.LastIndex(posix, "@"); index >= 0 {
		variant = posix[index+1:]
		if dot := strings.Index(variant, "."); dot >= 0 {
			variant = variant[:dot]
		}
		switch variant {
		case "nynorsk":
			variant = "NY"
		}
	}
	if base == "C" || base == "POSIX" {
		// C.UTF-8 / POSIX.UTF-8 and friends.
		base = "en_US_POSIX"
	}
	parts := strings.Split(base, "_")
	lang := strings.ToLower(parts[0])
	if !validLanguageSubtag(lang) {
		return "und"
	}
	tag := lang
	if len(parts) > 1 && validRegionSubtag(parts[1]) {
		tag += "-" + strings.ToUpper(parts[1])
	}
	if variant != "" {
		tag += "-x-lvariant-" + variant
	}
	return tag
}

func validLanguageSubtag(value string) bool {
	if len(value) < 2 || len(value) > 8 {
		return false
	}
	for index := 0; index < len(value); index++ {
		char := value[index]
		if (char < 'a' || char > 'z') && (char < '0' || char > '9') {
			return false
		}
	}
	return true
}

func validRegionSubtag(value string) bool {
	if len(value) == 2 {
		for index := 0; index < 2; index++ {
			char := value[index]
			if (char < 'a' || char > 'z') && (char < 'A' || char > 'Z') {
				return false
			}
		}
		return true
	}
	if len(value) == 3 {
		for index := 0; index < 3; index++ {
			if value[index] < '0' || value[index] > '9' {
				return false
			}
		}
		return true
	}
	return false
}

// collatorHandle is one goroutine's collator plus the scratch buffer its keys
// are built in.
type collatorHandle struct {
	collator *collate.Collator
	buffer   collate.Buffer
}

// collatorPools holds one sync.Pool per locale tag. collate.Collator is not
// safe for concurrent use, so callers borrow a handle and return it.
var collatorPools sync.Map // string -> *sync.Pool

func collatorPool(tag string) *sync.Pool {
	if pool, ok := collatorPools.Load(tag); ok {
		return pool.(*sync.Pool)
	}
	parsed, err := language.Parse(tag)
	if err != nil {
		parsed = language.Und
	}
	pool := &sync.Pool{New: func() any {
		return &collatorHandle{collator: collate.New(parsed)}
	}}
	actual, _ := collatorPools.LoadOrStore(tag, pool)
	return actual.(*sync.Pool)
}

func borrowCollator(tag string) (*collatorHandle, *sync.Pool) {
	pool := collatorPool(tag)
	return pool.Get().(*collatorHandle), pool
}

// LocaleCompare returns the sign of `a.localeCompare(b)` for the process
// default locale.
func LocaleCompare(a, b string) int {
	return LocaleCompareIn(LocaleTag(), a, b)
}

// LocaleCompareIn is LocaleCompare for an explicit locale tag. It is the hook
// the differential tests use to drive the fixture locales.
func LocaleCompareIn(tag, a, b string) int {
	if a == b {
		return 0
	}
	if asciiFastPath(tag) && printableASCII(a) && printableASCII(b) {
		return asciiCompare(a, b)
	}
	handle, pool := borrowCollator(tag)
	result := handle.collator.CompareString(a, b)
	pool.Put(handle)
	return result
}

// SortStringsLocale sorts s in locale order the way `Array.prototype.sort((a,
// b) => a.localeCompare(b))` does: stably, and without adding a tie-break that
// V8 does not have (the collator can consider distinct strings equal, for
// example when one contains only ignorable characters).
func SortStringsLocale(s []string) {
	sortStringsLocaleIn(LocaleTag(), s)
}

// SortStableByLocale sorts items by name in locale order, stably.
func SortStableByLocale[T any](items []T, name func(T) string) {
	sortStableByLocaleIn(LocaleTag(), items, name)
}

func sortStringsLocaleIn(tag string, s []string) {
	sortStableByLocaleIn(tag, s, func(value string) string { return value })
}

func sortStableByLocaleIn[T any](tag string, items []T, name func(T) string) {
	if len(items) < 2 {
		return
	}
	handle, pool := borrowCollator(tag)
	// Keys live in one arena: a key is a few hundred bytes, so per-name slices
	// would otherwise dominate the allocation of a large sort.
	estimate := 0
	for _, item := range items {
		estimate += len(name(item)) + 8
	}
	arena := make([]byte, 0, 6*estimate)
	bounds := make([]int, len(items)+1)
	for index, item := range items {
		bounds[index] = len(arena)
		arena = append(arena, handle.collator.KeyFromString(&handle.buffer, name(item))...)
	}
	bounds[len(items)] = len(arena)
	pool.Put(handle)

	order := make([]int, len(items))
	for index := range order {
		order[index] = index
	}
	key := func(item int) []byte { return arena[bounds[item]:bounds[item+1]] }
	sort.SliceStable(order, func(i, j int) bool { return bytes.Compare(key(order[i]), key(order[j])) < 0 })

	sorted := make([]T, len(items))
	for index, source := range order {
		sorted[index] = items[source]
	}
	copy(items, sorted)
}
