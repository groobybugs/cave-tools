package compression

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/groobybugs/cave-tools/go/internal/jsre"
	rt "github.com/groobybugs/cave-tools/go/internal/runtime"
	"github.com/groobybugs/cave-tools/go/internal/storage"
)

// This file ports src/compression/utils.ts: output budgets, ANSI/blank-line
// compaction, JSON compaction, rtk command rewriting, the per-session read
// dedup cache and the session stats buckets.
//
// Module-level state from TS (fileCache, stats buckets, caches, budgets) is
// package-level state guarded by utilsMu. State that TS keys by getContext()
// (the stats bucket, the subagent-detection cache) is keyed by the Go
// context.Context here, so daemon clients keep separate rows.

// DefaultSession is the session id used when the caller has none.
// port of src/compression/utils.ts:DEFAULT_SESSION
const DefaultSession = "default"

// DefaultDeliveryKey is the delivery key used when the caller has none.
// port of src/compression/utils.ts:DEFAULT_DELIVERY_KEY
const DefaultDeliveryKey = "default"

// port of src/compression/utils.ts:MAX_DELIVERIES_PER_FILE
const maxDeliveriesPerFile = 256

const (
	// port of src/compression/utils.ts:RTK_TOP_PASSTHROUGH_CAP
	rtkTopPassthroughCap = 50

	// port of src/compression/utils.ts:BOUNCE_WINDOW
	bounceWindow = 5
	// port of src/compression/utils.ts:EDIT_FORCE_WINDOW
	editForceWindow = 10
	// port of src/compression/utils.ts:BOUNCE_RATE_THRESHOLD
	bounceRateThreshold = 0.3

	// port of src/compression/utils.ts:RUN_LENGTH_MIN
	runLengthMin = 4
	// port of src/compression/utils.ts:RUN_MARKER_PREFIX
	runMarkerPrefix = "[... "

	// port of src/compression/utils.ts:GLOBAL_MAX_LINES
	globalMaxLines = 500
	// port of src/compression/utils.ts:GLOBAL_HEAD_LINES
	globalHeadLines = 200
	// port of src/compression/utils.ts:GLOBAL_TAIL_LINES
	globalTailLines = 100

	// port of src/compression/utils.ts:CHAR_MARKER_RESERVE
	charMarkerReserve = 48

	// BudgetFastPathMaxBytes is cave__read's MAX_BYTES fast-path cap.
	// port of src/compression/utils.ts:BUDGET_FAST_PATH_MAX_BYTES
	BudgetFastPathMaxBytes = 50 * 1024

	// port of src/compression/utils.ts:MAX_STRUCTURED_INPUT_BYTES
	maxStructuredInputBytes = 4 * 1024 * 1024

	// port of src/compression/utils.ts:RTK_REWRITE_TIMEOUT_MS
	rtkRewriteTimeoutMS = 3000
	// port of src/compression/utils.ts:RTK_TOKEN_MAX_LEN
	rtkTokenMaxLen = 80
)

// ReadStub is the fixed dedup stub TS exports as READ_STUB.
// port of src/compression/utils.ts:READ_STUB
const ReadStub = "<file unchanged since last read>"

var (
	// port of src/compression/utils.ts:fileCache
	fileCache = map[string]map[string]*cacheEntry{}

	// port of src/compression/utils.ts:subagentContextCache
	subagentContextCache = map[string]bool{}
	// port of src/compression/utils.ts:unknownSubagentEnvLogged
	unknownSubagentEnvLogged = false

	// port of src/compression/utils.ts:recentReads
	recentReads = map[string][]bounceEvent{}
	// port of src/compression/utils.ts:recentlyEdited
	recentlyEdited = map[string]int64{}
	// port of src/compression/utils.ts:seqCounter
	seqCounter int64

	// port of src/compression/utils.ts:localBucket
	localBucket = newBucket(int64(os.Getpid()))
	// port of src/compression/utils.ts:clientBuckets
	clientBuckets = map[int64]*StatsBucket{}

	// port of src/compression/utils.ts:budgets
	budgets = map[string]storage.BudgetConfig{
		"bash": {MaxLines: 50, HeadLines: 15, TailLines: 35, MaxChars: numberPtr(6000)},
		"read": {MaxLines: 200, HeadLines: 100, TailLines: 100},
		"grep": {MaxLines: 60, HeadLines: 30, TailLines: 30, MaxChars: numberPtr(6000)},
		"web":  {MaxLines: 120, HeadLines: 60, TailLines: 60, MaxChars: numberPtr(8000)},
		"find": {MaxLines: 120, HeadLines: 60, TailLines: 60},
		"ls":   {MaxLines: 60, HeadLines: 30, TailLines: 30},
	}
	// budgetOrder is the object insertion order TS keeps for Object.entries;
	// a Go map would randomize the status report.
	budgetOrder = []string{"bash", "read", "grep", "web", "find", "ls"}

	// port of src/compression/utils.ts:rtkAvailableCache
	rtkAvailableCache *bool

	// statsPersistScheduled/dirtyBuckets emulate the coalesced setImmediate
	// stats write in persistStats.
	statsPersistScheduled bool
	dirtyBuckets          []*StatsBucket
	persistWG             sync.WaitGroup

	utilsMu sync.Mutex
)

// Regexes compiled from their TS sources through jsre.
var (
	// port of src/compression/utils.ts:TASK_ID_STYLE
	taskIDStyleRE = jsre.MustCompile(`(?:^|_)TASK_ID$`, "")
	// port of src/compression/utils.ts:UNKNOWN_SUBAGENT_CANDIDATE
	unknownSubagentCandidateRE = jsre.MustCompile(`(?:SUBAGENT|^CLAUDE_CODE_)`, "")

	// digitsRunRE is the inline /\d+/g in maskDigitRuns.
	digitsRunRE = jsre.MustCompile(`\d+`, "g")
	// hasLetterRE is the inline /[a-zA-Z]/ test in collapseRepeatedLines.
	hasLetterRE = jsre.MustCompile(`[a-zA-Z]`, "")
	// port of src/compression/utils.ts:RUN_MARKER_RE
	runMarkerRE = jsre.MustCompile(`^\[\.\.\. \d+ similar lines omitted`, "")

	// port of src/compression/utils.ts:stripAnsi (inline regex)
	ansiRE = jsre.MustCompile(`[\u001b\u009b](?:[@-Z\\-_]|\[[0-9;]*[ -/]*[@-~]|[@-_][0-9;]*[@-~]?|[@-_]|[0-9;]*m)`, "g")
	// port of src/compression/utils.ts:collapseBlankLines (inline regex)
	blankLinesRE = jsre.MustCompile(`(\r?\n){3,}`, "g")

	// port of src/compression/utils.ts:ENV_ASSIGN_PREFIX
	envAssignPrefixRE = jsre.MustCompile(`^(?:[A-Za-z_][A-Za-z0-9_]*\s*=\s*(?:'[^']*'|"[^"]*"|\S*)\s+)`, "")

	// isPathToken's /^[A-Za-z]:[\\/]/.
	drivePathRE = jsre.MustCompile(`^[A-Za-z]:[\\/]`, "")
	// passthroughPrefix's /^[A-Za-z_][A-Za-z0-9_]*=/.
	envNameEqRE = jsre.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*=`, "")

	// extractStructuredData's whitespace-only XML cleanup.
	betweenTagsRE = jsre.MustCompile(`>\s+<`, "g")
	spaceRunRE    = jsre.MustCompile(`\s{2,}`, "g")
)

// cacheEntry is one file's dedup state for one session.
// port of src/compression/utils.ts:CacheEntry
type cacheEntry struct {
	hash       string
	mtime      float64
	deliveries *deliveries
	readCount  int
}

// deliveries is an insertion-ordered map[string]int, the Go form of the JS
// Map<string, number> in CacheEntry.deliveries.
type deliveries struct {
	order  []string
	values map[string]int
}

func newDeliveries() *deliveries {
	return &deliveries{values: map[string]int{}}
}

func (d *deliveries) has(key string) bool {
	_, ok := d.values[key]
	return ok
}

func (d *deliveries) get(key string) (int, bool) {
	value, ok := d.values[key]
	return value, ok
}

func (d *deliveries) set(key string, value int) {
	if _, ok := d.values[key]; !ok {
		d.order = append(d.order, key)
	}
	d.values[key] = value
}

func (d *deliveries) size() int { return len(d.order) }

// deleteOldest drops the first inserted key, like Map.keys().next().value.
func (d *deliveries) deleteOldest() {
	if len(d.order) == 0 {
		return
	}
	oldest := d.order[0]
	d.order = d.order[1:]
	delete(d.values, oldest)
}

// FileCacheUpdate is the optional update payload for UpdateFileCache.
// port of src/compression/utils.ts:FileCacheUpdate
type FileCacheUpdate struct {
	DeliveryKey *string
	// Content mirrors `content?: string | Buffer`; nil means "read the file".
	Content      []byte
	Hash         string
	MtimeMs      *float64
	EmittedChars *int
}

// StatsBucket is one stats row's in-memory counters: this process, or one
// daemon client process (keyed by CallContext.ClientPID).
// port of src/compression/utils.ts:StatsBucket
type StatsBucket struct {
	PID                 int64
	SessionStart        int64
	cacheHits           int64
	cacheMisses         int64
	dedupSavedChars     int64
	rangeEchoSavedChars int64
	rangeEditCount      int64
	rtkRewrites         int64
	rtkAlreadyWrapped   int64
	rtkPassthrough      int64
	rtkTopPassthrough   *orderedCounts
	totalBounces        int64
	totalWastedChars    int64
	perExtension        map[string]storage.BounceStats
	// perExtensionOrder is TS's Map insertion order for bounce extensions.
	perExtensionOrder []string
	savingsByTool     map[string]storage.ToolSavings
}

// newBucket builds a zeroed stats bucket.
// port of src/compression/utils.ts:newBucket
func newBucket(pid int64) *StatsBucket {
	return &StatsBucket{
		PID:               pid,
		SessionStart:      time.Now().UnixMilli(),
		rtkTopPassthrough: newOrderedCounts(),
		perExtension:      map[string]storage.BounceStats{},
		savingsByTool:     map[string]storage.ToolSavings{},
	}
}

// orderedCounts is an insertion-ordered map[string]int64 (JS Map).
type orderedCounts struct {
	order  []string
	values map[string]int64
}

func newOrderedCounts() *orderedCounts {
	return &orderedCounts{values: map[string]int64{}}
}

func (o *orderedCounts) get(key string) (int64, bool) {
	value, ok := o.values[key]
	return value, ok
}

func (o *orderedCounts) set(key string, value int64) {
	if _, ok := o.values[key]; !ok {
		o.order = append(o.order, key)
	}
	o.values[key] = value
}

func (o *orderedCounts) delete(key string) {
	if _, ok := o.values[key]; !ok {
		return
	}
	delete(o.values, key)
	for i, candidate := range o.order {
		if candidate == key {
			o.order = append(o.order[:i], o.order[i+1:]...)
			break
		}
	}
}

func (o *orderedCounts) size() int { return len(o.order) }

// port of src/compression/utils.ts:trackedFileCount
func trackedFileCountLocked() int {
	n := 0
	for _, sessionMap := range fileCache {
		n += len(sessionMap)
	}
	return n
}

// port of src/compression/utils.ts:suppressSubagentEnvLog
func SuppressSubagentEnvLog() {
	utilsMu.Lock()
	unknownSubagentEnvLogged = true
	utilsMu.Unlock()
}

// port of src/compression/utils.ts:envNonEmpty
func envNonEmpty(value string) bool { return value != "" }

// port of src/compression/utils.ts:isKnownSubagentKey
func isKnownSubagentKey(key string) bool {
	switch key {
	case "CLINE_TASK_ID", "CLAUDE_CODE_SUBAGENT", "ANTHROPIC_SUBAGENT", "CURSOR_TASK_ID":
		return true
	}
	return taskIDStyleRE.MatchString(key)
}

// port of src/compression/utils.ts:logUnknownSubagentEnvOnce
func logUnknownSubagentEnvOnce(triggeredKeys []string, env map[string]string) {
	if unknownSubagentEnvLogged {
		return
	}
	unknownSubagentEnvLogged = true
	triggered := make(map[string]struct{}, len(triggeredKeys))
	for _, key := range triggeredKeys {
		triggered[key] = struct{}{}
	}
	unknown := make([]string, 0)
	for key := range env {
		if _, ok := triggered[key]; ok {
			continue
		}
		if !unknownSubagentCandidateRE.MatchString(key) {
			continue
		}
		if !envNonEmpty(env[key]) {
			continue
		}
		unknown = append(unknown, key)
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		fmt.Fprintf(os.Stderr,
			"cave-tools: unknown subagent-like env keys (ignored; not forcing fresh): %s\n",
			strings.Join(unknown, ", "))
	}
}

// ResetSubagentContextCache clears the subagent detection cache (test hook).
// port of src/compression/utils.ts:resetSubagentContextCache
func ResetSubagentContextCache() {
	utilsMu.Lock()
	subagentContextCache = map[string]bool{}
	unknownSubagentEnvLogged = false
	utilsMu.Unlock()
}

// IsSubagentContext reports whether the caller's env marks it as a subagent.
// port of src/compression/utils.ts:isSubagentContext
func IsSubagentContext(ctx context.Context) bool {
	clientID := rt.GetClientID(ctx)
	utilsMu.Lock()
	cached, ok := subagentContextCache[clientID]
	utilsMu.Unlock()
	if ok {
		return cached
	}

	env := rt.GetEnv(ctx)
	force := env["CAVE_TOOLS_FORCE_FRESH"]
	triggered := make([]string, 0, 2)
	detected := false

	if force == "1" || force == "true" {
		detected = true
		triggered = append(triggered, "CAVE_TOOLS_FORCE_FRESH")
	}

	for key := range env {
		if !envNonEmpty(env[key]) {
			continue
		}
		if !isKnownSubagentKey(key) {
			continue
		}
		detected = true
		triggered = append(triggered, key)
	}

	utilsMu.Lock()
	logUnknownSubagentEnvOnce(triggered, env)
	subagentContextCache[clientID] = detected
	utilsMu.Unlock()
	return detected
}

// statsBucketLocked returns the stats bucket for ctx's client process.
// port of src/compression/utils.ts:statsBucket
func statsBucketLocked(ctx context.Context) *StatsBucket {
	if call := rt.GetContext(ctx); call != nil && call.ClientPID != nil {
		pid := int64(*call.ClientPID)
		bucket, ok := clientBuckets[pid]
		if !ok {
			bucket = newBucket(pid)
			clientBuckets[pid] = bucket
		}
		return bucket
	}
	return localBucket
}

// ReleaseClientStats drops a disconnected daemon client's bucket after
// writing its row.
// port of src/compression/utils.ts:releaseClientStats
func ReleaseClientStats(ctx context.Context, pid int) {
	utilsMu.Lock()
	bucket, ok := clientBuckets[int64(pid)]
	if !ok {
		utilsMu.Unlock()
		return
	}
	stats := buildStatsLocked(ctx, bucket)
	utilsMu.Unlock()
	_ = storage.UpsertSessionStats(stats)
	utilsMu.Lock()
	delete(clientBuckets, int64(pid))
	utilsMu.Unlock()
}

// BounceEvent is one read in the recent-reads bounce window.
// port of src/compression/utils.ts:BounceEvent
type bounceEvent struct {
	seq           int64
	wasCompressed bool
	charsSent     int64
}

// estimateTokens is the 4-chars-per-token estimate.
// port of src/compression/utils.ts:estimateTokens
func estimateTokens(charCount int64) int64 {
	return int64(math.Round(float64(charCount) / 4))
}

// BuildStats renders one bucket as the PersistedStats row. A nil bucket means
// the current call context's bucket (TS's default argument).
// port of src/compression/utils.ts:buildStats
func BuildStats(ctx context.Context, b *StatsBucket) storage.PersistedStats {
	utilsMu.Lock()
	defer utilsMu.Unlock()
	return buildStatsLocked(ctx, b)
}

func buildStatsLocked(ctx context.Context, b *StatsBucket) storage.PersistedStats {
	if b == nil {
		b = statsBucketLocked(ctx)
	}
	cacheTotal := b.cacheHits + b.cacheMisses
	var rawChars, compressedChars, compressionSavedChars, totalCalls int64
	for _, tool := range b.savingsByTool {
		rawChars += tool.RawChars
		compressedChars += tool.CompressedChars
		compressionSavedChars += tool.SavedChars
		totalCalls += tool.Calls
	}
	savedChars := compressionSavedChars + b.dedupSavedChars
	hitRate := 0.0
	if cacheTotal > 0 {
		hitRate = float64(b.cacheHits) / float64(cacheTotal)
	}
	byTool := make(map[string]storage.ToolSavings, len(b.savingsByTool))
	for name, tool := range b.savingsByTool {
		byTool[name] = tool
	}
	return storage.PersistedStats{
		Pid:          b.PID,
		SessionStart: b.SessionStart,
		UpdatedAt:    time.Now().UnixMilli(),
		Cache: storage.CacheStats{
			Hits:         b.cacheHits,
			Misses:       b.cacheMisses,
			Total:        cacheTotal,
			HitRate:      hitRate,
			FilesTracked: int64(trackedFileCountLocked()),
		},
		Savings: storage.SavingsStats{
			TotalCalls:            totalCalls,
			RawChars:              rawChars,
			CompressedChars:       compressedChars,
			CompressionSavedChars: compressionSavedChars,
			DedupSavedChars:       b.dedupSavedChars,
			SavedChars:            savedChars,
			EstimatedTokensSaved:  estimateTokens(savedChars),
			ByTool:                byTool,
		},
		Rtk: storage.RtkStats{
			Rewrites:       b.rtkRewrites,
			AlreadyWrapped: b.rtkAlreadyWrapped,
			Passthrough:    b.rtkPassthrough,
		},
		Budgets: getAllBudgetsLocked(),
		Bounces: getBounceStatsLocked(b),
	}
}

// persistStatsLocked coalesces stats writes and schedules one background
// upsert per burst, mirroring the setImmediate batching in TS.
// port of src/compression/utils.ts:persistStats
func persistStatsLocked(ctx context.Context) {
	bucket := statsBucketLocked(ctx)
	found := false
	for _, dirty := range dirtyBuckets {
		if dirty == bucket {
			found = true
			break
		}
	}
	if !found {
		dirtyBuckets = append(dirtyBuckets, bucket)
	}
	if statsPersistScheduled {
		return
	}
	statsPersistScheduled = true
	persistWG.Add(1)
	go func() {
		defer persistWG.Done()
		utilsMu.Lock()
		buckets := dirtyBuckets
		dirtyBuckets = nil
		statsPersistScheduled = false
		utilsMu.Unlock()
		for _, pending := range buckets {
			_ = storage.UpsertSessionStats(buildStatsAt(ctx, pending))
		}
	}()
}

// buildStatsAt renders a specific bucket without taking utilsMu.
func buildStatsAt(ctx context.Context, b *StatsBucket) storage.PersistedStats {
	utilsMu.Lock()
	defer utilsMu.Unlock()
	return buildStatsLocked(ctx, b)
}

// FlushStats writes the local bucket's stats snapshot synchronously.
// port of src/compression/utils.ts:flushStats
func FlushStats() {
	utilsMu.Lock()
	stats := buildStatsLocked(context.Background(), localBucket)
	utilsMu.Unlock()
	_ = storage.UpsertSessionStats(stats)
}

// waitPendingPersists blocks until scheduled stats writes finish (tests).
func waitPendingPersists() { persistWG.Wait() }

// GetLifetimeStats returns banked lifetime totals, or zeros when unavailable.
// port of src/compression/utils.ts:getLifetimeStats
func GetLifetimeStats() storage.LifetimeStats {
	stats, err := storage.GetLifetimeStats()
	if err != nil {
		return storage.LifetimeStats{}
	}
	return stats
}

// ListSessionStats lists stats rows, optionally including ended sessions.
// port of src/compression/utils.ts:listSessionStats
func ListSessionStats(includeEnded bool) []storage.SessionStatsRow {
	stats, err := storage.ListSessionStats(includeEnded)
	if err != nil {
		return []storage.SessionStatsRow{}
	}
	return stats
}

// PruneDeadSessions marks rows whose process is gone as ended.
// port of src/compression/utils.ts:pruneDeadSessions
func PruneDeadSessions() int {
	removed := 0
	for _, session := range ListSessionStats(false) {
		pid := session.Pid
		if pid == int64(os.Getpid()) {
			continue
		}
		if !processAlive(int(pid)) {
			if changed, err := storage.MarkSessionEnded(pid); err == nil && changed {
				removed++
			}
		}
	}
	return removed
}

// HashContent is sha256(content) in lowercase hex, like Node's createHash.
// port of src/compression/utils.ts:hashContent
func HashContent(content []byte) string {
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])
}

// GetFileHash returns the sha256 of a file, or nil when it cannot be read.
// port of src/compression/utils.ts:getFileHash
func GetFileHash(filePath string) *string {
	content, err := os.ReadFile(filePath)
	if err != nil {
		return nil
	}
	hash := HashContent(content)
	return &hash
}

// mtimeMsMillis renders a file mtime the way Node's stat.mtimeMs does.
func mtimeMsMillis(info os.FileInfo) float64 {
	return float64(info.ModTime().UnixNano()) / 1e6
}

// IsFileUnchanged reports whether this session already delivered this exact
// representation of an unchanged file.
// port of src/compression/utils.ts:isFileUnchanged
func IsFileUnchanged(ctx context.Context, filePath string, sessionID string, deliveryKey string) bool {
	if IsSubagentContext(ctx) {
		return false
	}

	utilsMu.Lock()
	sessionMap, ok := fileCache[sessionID]
	if !ok {
		utilsMu.Unlock()
		return false
	}
	entry, ok := sessionMap[filePath]
	if !ok {
		utilsMu.Unlock()
		return false
	}
	entryHash := entry.hash
	entryMtime := entry.mtime
	utilsMu.Unlock()

	// stat + re-hash run outside utilsMu: hashing a large file must not block
	// every other tool call. TS awaits these without a global lock.
	info, err := os.Stat(filePath)
	if err != nil {
		return false
	}
	mtime := mtimeMsMillis(info)
	if mtime != entryMtime {
		currentHash := GetFileHash(filePath)
		if currentHash == nil || *currentHash != entryHash {
			return false
		}
	}

	utilsMu.Lock()
	if mtime != entryMtime {
		entry.mtime = mtime
	}
	wouldEmit, ok := entry.deliveries.get(deliveryKey)
	utilsMu.Unlock()
	if !ok {
		return false
	}

	// Same coalesced registry barrier as TS: never stub before the strict-mode
	// read registry has the path.
	rt.RegisterReadPathCompat(ctx, filePath)

	utilsMu.Lock()
	bucket := statsBucketLocked(ctx)
	bucket.cacheHits++
	bucket.dedupSavedChars += int64(max(0, wouldEmit-utf16Length(ReadStub)))
	persistStatsLocked(ctx)
	utilsMu.Unlock()
	return true
}

// UpdateFileCache records a delivered file representation for a session.
// port of src/compression/utils.ts:updateFileCache
func UpdateFileCache(ctx context.Context, filePath string, sessionID string, update FileCacheUpdate) {
	content := update.Content
	hash := update.Hash
	mtime := update.MtimeMs

	if hash == "" {
		if content == nil {
			data, err := os.ReadFile(filePath)
			if err != nil {
				return
			}
			content = data
		}
		hash = HashContent(content)
	}
	if mtime == nil {
		value := 0.0
		if info, err := os.Stat(filePath); err == nil {
			value = mtimeMsMillis(info)
		}
		mtime = &value
	}
	emittedChars := 0
	if update.EmittedChars != nil {
		emittedChars = *update.EmittedChars
	} else if content != nil {
		// BudgetedText reads the budget table; compute it before taking utilsMu
		// (GetBudget locks the same mutex).
		emittedChars = utf16Length(BudgetedText(string(content), "read"))
	}
	deliveryKey := DefaultDeliveryKey
	if update.DeliveryKey != nil {
		deliveryKey = *update.DeliveryKey
	}

	utilsMu.Lock()
	sessionMap, ok := fileCache[sessionID]
	if !ok {
		sessionMap = map[string]*cacheEntry{}
		fileCache[sessionID] = sessionMap
	}
	existing := sessionMap[filePath]
	readCount := 1
	if existing != nil {
		readCount = existing.readCount + 1
	}
	var deliveriesForFile *deliveries
	if existing != nil && existing.hash == hash {
		deliveriesForFile = existing.deliveries
	} else {
		deliveriesForFile = newDeliveries()
	}
	if !deliveriesForFile.has(deliveryKey) && deliveriesForFile.size() >= maxDeliveriesPerFile {
		deliveriesForFile.deleteOldest()
	}
	deliveriesForFile.set(deliveryKey, emittedChars)
	sessionMap[filePath] = &cacheEntry{
		hash:       hash,
		mtime:      *mtime,
		deliveries: deliveriesForFile,
		readCount:  readCount,
	}
	statsBucketLocked(ctx).cacheMisses++
	persistStatsLocked(ctx)
	utilsMu.Unlock()

	// Await the advisory flat registry so a read is a deterministic barrier for
	// an immediately following built-in edit/write.
	rt.RegisterReadPathCompat(ctx, filePath)
	capturedHash := hash
	go func() {
		storage.RegisterReadPath(filePath, &capturedHash)
	}()
}

// MarkFullDelivered marks the default representation as delivered.
// port of src/compression/utils.ts:markFullDelivered
func MarkFullDelivered(filePath string, sessionID string) {
	utilsMu.Lock()
	defer utilsMu.Unlock()
	sessionMap, ok := fileCache[sessionID]
	if !ok {
		return
	}
	entry, ok := sessionMap[filePath]
	if ok && !entry.deliveries.has(DefaultDeliveryKey) {
		entry.deliveries.set(DefaultDeliveryKey, 0)
	}
}

// ReadStubFor renders the dynamic "unchanged since read #N" stub.
// port of src/compression/utils.ts:readStubFor
func ReadStubFor(filePath string, sessionID string) string {
	utilsMu.Lock()
	defer utilsMu.Unlock()
	readCount := 0
	if sessionMap, ok := fileCache[sessionID]; ok {
		if entry, ok := sessionMap[filePath]; ok {
			readCount = entry.readCount
		}
	}
	tag := sessionID
	if sessionID == DefaultSession {
		tag = "default"
	}
	return fmt.Sprintf("<file unchanged since read #%d (session %s). Use force=true to re-read.>", readCount, tag)
}

// InvalidateFileCache drops a path from every session's cache.
// port of src/compression/utils.ts:invalidateFileCache
func InvalidateFileCache(ctx context.Context, filePath string) {
	utilsMu.Lock()
	for _, sessionMap := range fileCache {
		delete(sessionMap, filePath)
	}
	persistStatsLocked(ctx)
	utilsMu.Unlock()
}

// GetCacheStats returns the current bucket's dedup cache counters.
// port of src/compression/utils.ts:getCacheStats
func GetCacheStats(ctx context.Context) storage.CacheStats {
	utilsMu.Lock()
	defer utilsMu.Unlock()
	bucket := statsBucketLocked(ctx)
	total := bucket.cacheHits + bucket.cacheMisses
	hitRate := 0.0
	if total > 0 {
		hitRate = float64(bucket.cacheHits) / float64(total)
	}
	return storage.CacheStats{
		Hits:         bucket.cacheHits,
		Misses:       bucket.cacheMisses,
		Total:        total,
		HitRate:      hitRate,
		FilesTracked: int64(trackedFileCountLocked()),
	}
}

// GetSavingsStats returns the current bucket's savings block.
// port of src/compression/utils.ts:getSavingsStats
func GetSavingsStats(ctx context.Context) storage.SavingsStats {
	utilsMu.Lock()
	defer utilsMu.Unlock()
	return buildStatsLocked(ctx, nil).Savings
}

// GetRtkStats returns the current bucket's rtk accounting.
// port of src/compression/utils.ts:getRtkStats
func GetRtkStats(ctx context.Context) storage.RtkStats {
	utilsMu.Lock()
	defer utilsMu.Unlock()
	return buildStatsLocked(ctx, nil).Rtk
}

// RtkPassthroughEntry is one tracked rtk passthrough prefix.
// TS: `{ prefix: string; count: number }`.
type RtkPassthroughEntry struct {
	Prefix string `json:"prefix"`
	Count  int64  `json:"count"`
}

// GetRtkTopPassthrough lists passthrough prefixes, count desc then prefix asc.
// port of src/compression/utils.ts:getRtkTopPassthrough
func GetRtkTopPassthrough(ctx context.Context) []RtkPassthroughEntry {
	utilsMu.Lock()
	defer utilsMu.Unlock()
	bucket := statsBucketLocked(ctx)
	entries := make([]RtkPassthroughEntry, 0, bucket.rtkTopPassthrough.size())
	for _, prefix := range bucket.rtkTopPassthrough.order {
		entries = append(entries, RtkPassthroughEntry{
			Prefix: prefix,
			Count:  bucket.rtkTopPassthrough.values[prefix],
		})
	}
	sort.SliceStable(entries, func(i, j int) bool {
		if entries[i].Count != entries[j].Count {
			return entries[i].Count > entries[j].Count
		}
		return entries[i].Prefix < entries[j].Prefix
	})
	return entries
}

// ReductionPercent is savedChars as a percentage of raw + dedup baseline.
// port of src/compression/utils.ts:reductionPercent
func ReductionPercent(rawChars int64, savedChars int64, dedupSavedChars ...int64) float64 {
	dedup := int64(0)
	if len(dedupSavedChars) > 0 {
		dedup = dedupSavedChars[0]
	}
	baseline := rawChars + dedup
	if baseline <= 0 {
		return 0
	}
	return (float64(savedChars) / float64(baseline)) * 100
}

// EfficiencyMeter renders the filled/empty bar meter.
// port of src/compression/utils.ts:efficiencyMeter
func EfficiencyMeter(percent float64, width ...int) string {
	w := 24
	if len(width) > 0 {
		w = width[0]
	}
	clamped := math.Max(0, math.Min(100, percent))
	filled := int(math.Round((clamped / 100) * float64(w)))
	return strings.Repeat("█", filled) + strings.Repeat("░", w-filled)
}

// ResetCache clears the dedup cache and snapshots.
// port of src/compression/utils.ts:resetCache
func ResetCache(ctx context.Context) {
	utilsMu.Lock()
	fileCache = map[string]map[string]*cacheEntry{}
	ResetSnapshots()
	persistStatsLocked(ctx)
	utilsMu.Unlock()
}

// ResetStats zeroes the current bucket's counters.
// port of src/compression/utils.ts:resetStats
func ResetStats(ctx context.Context) {
	utilsMu.Lock()
	bucket := statsBucketLocked(ctx)
	bucket.cacheHits = 0
	bucket.cacheMisses = 0
	bucket.dedupSavedChars = 0
	bucket.rangeEchoSavedChars = 0
	bucket.rangeEditCount = 0
	bucket.rtkRewrites = 0
	bucket.rtkAlreadyWrapped = 0
	bucket.rtkPassthrough = 0
	bucket.rtkTopPassthrough = newOrderedCounts()
	rtkAvailableCache = nil
	seqCounter = 0
	bucket.totalBounces = 0
	bucket.totalWastedChars = 0
	recentReads = map[string][]bounceEvent{}
	bucket.perExtension = map[string]storage.BounceStats{}
	bucket.perExtensionOrder = nil
	recentlyEdited = map[string]int64{}
	bucket.savingsByTool = map[string]storage.ToolSavings{}
	persistStatsLocked(ctx)
	utilsMu.Unlock()
}

// extensionOf returns the lowercased extension including the dot.
// port of src/compression/utils.ts:extensionOf
func extensionOf(filePath string) string {
	dot := strings.LastIndex(filePath, ".")
	if dot > 0 {
		return strings.ToLower(filePath[dot:])
	}
	return ""
}

// RecordRead records one read for bounce tracking.
// port of src/compression/utils.ts:recordRead
func RecordRead(ctx context.Context, filePath string, wasCompressed bool, charsSent int64) {
	utilsMu.Lock()
	defer utilsMu.Unlock()
	bucket := statsBucketLocked(ctx)
	seqCounter++
	events := recentReads[filePath]

	if !wasCompressed && len(events) > 0 {
		last := events[len(events)-1]
		if last.wasCompressed && seqCounter-last.seq <= bounceWindow {
			bucket.totalBounces++
			bucket.totalWastedChars += last.charsSent

			ext := extensionOf(filePath)
			if ext != "" {
				stats, ok := bucket.perExtension[ext]
				if !ok {
					bucket.perExtensionOrder = append(bucket.perExtensionOrder, ext)
				}
				stats.Bounces++
				stats.WastedChars += last.charsSent
				bucket.perExtension[ext] = stats
			}
		}
	}

	events = append(events, bounceEvent{seq: seqCounter, wasCompressed: wasCompressed, charsSent: charsSent})
	if len(events) > 10 {
		events = events[1:]
	}
	recentReads[filePath] = events

	if ext := extensionOf(filePath); ext != "" {
		stats, ok := bucket.perExtension[ext]
		if !ok {
			bucket.perExtensionOrder = append(bucket.perExtensionOrder, ext)
		}
		stats.TotalReads++
		bucket.perExtension[ext] = stats
	}
	persistStatsLocked(ctx)
}

// RecordEdit records an edit sequence for the force-full window.
// port of src/compression/utils.ts:recordEdit
func RecordEdit(ctx context.Context, filePath string) {
	utilsMu.Lock()
	defer utilsMu.Unlock()
	seqCounter++
	recentlyEdited[filePath] = seqCounter
	persistStatsLocked(ctx)
}

// RecordRangeEchoSaved records chars of an old span not re-emitted.
// port of src/compression/utils.ts:recordRangeEchoSaved
func RecordRangeEchoSaved(ctx context.Context, chars int64) {
	utilsMu.Lock()
	defer utilsMu.Unlock()
	bucket := statsBucketLocked(ctx)
	if chars > 0 {
		bucket.rangeEchoSavedChars += chars
	}
	bucket.rangeEditCount++
	persistStatsLocked(ctx)
}

// RangeEditStats is the line-range edit savings summary.
// TS: `{ edits: number; echoSavedChars: number; estimatedTokensSaved: number }`.
type RangeEditStats struct {
	Edits                int64 `json:"edits"`
	EchoSavedChars       int64 `json:"echoSavedChars"`
	EstimatedTokensSaved int64 `json:"estimatedTokensSaved"`
}

// GetRangeEditStats returns the range-edit counters.
// port of src/compression/utils.ts:getRangeEditStats
func GetRangeEditStats(ctx context.Context) RangeEditStats {
	utilsMu.Lock()
	defer utilsMu.Unlock()
	bucket := statsBucketLocked(ctx)
	return RangeEditStats{
		Edits:                bucket.rangeEditCount,
		EchoSavedChars:       bucket.rangeEchoSavedChars,
		EstimatedTokensSaved: estimateTokens(bucket.rangeEchoSavedChars),
	}
}

// ShouldForceFull reports whether the file should bypass compressed reads.
// port of src/compression/utils.ts:shouldForceFull
func ShouldForceFull(ctx context.Context, filePath string) bool {
	utilsMu.Lock()
	defer utilsMu.Unlock()
	editSeq, ok := recentlyEdited[filePath]
	if ok && seqCounter-editSeq <= editForceWindow {
		return true
	}

	ext := extensionOf(filePath)
	if ext == "" {
		return false
	}
	bucket := statsBucketLocked(ctx)
	stats, ok := bucket.perExtension[ext]
	if !ok || stats.TotalReads < 3 {
		return false
	}
	return float64(stats.Bounces)/float64(stats.TotalReads) >= bounceRateThreshold
}

// GetBounceStats returns the current bucket's bounce counters.
// port of src/compression/utils.ts:getBounceStats
func GetBounceStats(ctx context.Context) storage.BounceSummary {
	utilsMu.Lock()
	defer utilsMu.Unlock()
	return getBounceStatsLocked(statsBucketLocked(ctx))
}

func getBounceStatsLocked(bucket *StatsBucket) storage.BounceSummary {
	byExtension := make(map[string]storage.BounceStats, len(bucket.perExtension))
	for ext, stats := range bucket.perExtension {
		byExtension[ext] = stats
	}
	return storage.BounceSummary{
		TotalBounces:     bucket.totalBounces,
		TotalWastedChars: bucket.totalWastedChars,
		ByExtension:      byExtension,
	}
}

// BounceStatsView is getBounceStats plus TS's Map insertion order for the
// per-extension object. status renders Object.entries in this order and only
// sorts by bounce count, stably.
type BounceStatsView struct {
	Summary storage.BounceSummary
	Order   []string
}

// GetBounceStatsView returns the bounce counters and their TS insertion order.
func GetBounceStatsView(ctx context.Context) BounceStatsView {
	utilsMu.Lock()
	defer utilsMu.Unlock()
	bucket := statsBucketLocked(ctx)
	return BounceStatsView{
		Summary: getBounceStatsLocked(bucket),
		Order:   append([]string(nil), bucket.perExtensionOrder...),
	}
}

// GetBudget returns one tool's budget or the 100/50/50 default.
// port of src/compression/utils.ts:getBudget
func GetBudget(toolName string) storage.BudgetConfig {
	utilsMu.Lock()
	defer utilsMu.Unlock()
	return getBudgetLocked(toolName)
}

func getBudgetLocked(toolName string) storage.BudgetConfig {
	if budget, ok := budgets[toolName]; ok {
		return budget
	}
	return storage.BudgetConfig{MaxLines: 100, HeadLines: 50, TailLines: 50}
}

// SetBudget replaces one tool's budget. maxChars is stored only when TS treats
// it as truthy (0/NaN disable it, ±Infinity and negatives are kept).
// port of src/compression/utils.ts:setBudget
func SetBudget(ctx context.Context, toolName string, maxLines float64, headLines float64, tailLines float64, maxChars *float64) {
	utilsMu.Lock()
	defer utilsMu.Unlock()
	budget := storage.BudgetConfig{MaxLines: maxLines, HeadLines: headLines, TailLines: tailLines}
	if maxChars != nil && jsNumberTruthy(*maxChars) {
		value := *maxChars
		budget.MaxChars = &value
	}
	if _, ok := budgets[toolName]; !ok {
		budgetOrder = append(budgetOrder, toolName)
	}
	budgets[toolName] = budget
	persistStatsLocked(ctx)
}

// GetAllBudgets returns a copy of the budget table.
// port of src/compression/utils.ts:getAllBudgets
func GetAllBudgets() map[string]storage.BudgetConfig {
	utilsMu.Lock()
	defer utilsMu.Unlock()
	return getAllBudgetsLocked()
}

func getAllBudgetsLocked() map[string]storage.BudgetConfig {
	out := make(map[string]storage.BudgetConfig, len(budgets))
	for name, budget := range budgets {
		out[name] = budget
	}
	return out
}

// BudgetEntry is one ordered Tool => BudgetConfig pair.
type BudgetEntry struct {
	Name   string
	Budget storage.BudgetConfig
}

// GetAllBudgetEntries returns the budgets in JS Object.entries order: integer
// keys ascending first, then string keys in insertion order.
// port of src/tools/status.ts:Object.entries(budgets)
func GetAllBudgetEntries() []BudgetEntry {
	utilsMu.Lock()
	defer utilsMu.Unlock()
	return getAllBudgetEntriesLocked()
}

func getAllBudgetEntriesLocked() []BudgetEntry {
	out := make([]BudgetEntry, 0, len(budgets))
	seen := make(map[string]bool, len(budgets))

	// Integer-like property names enumerate first, ascending.
	numeric := make([]string, 0)
	rest := make([]string, 0)
	for _, name := range budgetOrder {
		if seen[name] {
			continue
		}
		if _, ok := budgets[name]; !ok {
			continue
		}
		seen[name] = true
		if isJSArrayIndex(name) {
			numeric = append(numeric, name)
			continue
		}
		rest = append(rest, name)
	}
	sort.Slice(numeric, func(i, j int) bool {
		return jsArrayIndexValue(numeric[i]) < jsArrayIndexValue(numeric[j])
	})
	for _, name := range append(numeric, rest...) {
		out = append(out, BudgetEntry{Name: name, Budget: budgets[name]})
	}
	return out
}

// isJSArrayIndex reports whether key is an ECMAScript array index: the
// canonical decimal form of a uint32 other than 2^32-1.
func isJSArrayIndex(key string) bool {
	if key == "" || (len(key) > 1 && key[0] == '0') {
		return false
	}
	for i := 0; i < len(key); i++ {
		if key[i] < '0' || key[i] > '9' {
			return false
		}
	}
	value, err := strconv.ParseUint(key, 10, 64)
	return err == nil && value < 4294967295
}

func jsArrayIndexValue(key string) uint64 {
	value, _ := strconv.ParseUint(key, 10, 64)
	return value
}

// jsNumberTruthy is JS ToBoolean for a number.
func jsNumberTruthy(value float64) bool {
	return value != 0 && !math.IsNaN(value)
}

// StripAnsi removes SGR, cursor, screen and C1 control sequences.
// port of src/compression/utils.ts:stripAnsi
func StripAnsi(text string) string {
	return ansiRE.ReplaceAllString(text, "")
}

// CollapseBlankLines caps runs of blank lines at one.
// port of src/compression/utils.ts:collapseBlankLines
func CollapseBlankLines(text string) string {
	return blankLinesRE.ReplaceAllString(text, "\n\n")
}

// maskDigitRuns replaces every digit run with '#'.
// port of src/compression/utils.ts:maskDigitRuns
func maskDigitRuns(line string) string {
	return digitsRunRE.ReplaceAllString(line, "#")
}

// CollapseRepeatedLines elides digit-masked repeated runs.
// port of src/compression/utils.ts:collapseRepeatedLines
func CollapseRepeatedLines(text string) string {
	lines := strings.Split(text, "\n")
	out := make([]string, 0, len(lines))
	collapsed := false

	i := 0
	for i < len(lines) {
		key := maskDigitRuns(lines[i])
		j := i + 1
		for j < len(lines) && maskDigitRuns(lines[j]) == key {
			j++
		}
		runLen := j - i
		out = append(out, lines[i])
		if runLen >= runLengthMin &&
			hasLetterRE.MatchString(lines[i]) &&
			jsre.Trim(lines[i]) != "" &&
			!runMarkerRE.MatchString(lines[i]) &&
			!strings.HasPrefix(lines[i], runMarkerPrefix) {
			out = append(out, fmt.Sprintf("[... %d similar lines omitted (cave: run-length) ...]", runLen-1))
			collapsed = true
		} else {
			out = append(out, lines[i+1:j]...)
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

// jsToIntegerOrInfinity is ECMAScript ToIntegerOrInfinity for slice bounds.
func jsToIntegerOrInfinity(value float64) float64 {
	if math.IsNaN(value) {
		return 0
	}
	if math.IsInf(value, 0) {
		return value
	}
	return math.Trunc(value)
}

// jsSliceBounds resolves Array.prototype.slice(start, end) to Go indices. A
// nil end is JS `undefined`. JS coerces numbers to integers only here, at the
// slice boundary; the line counts and omitted markers stay fractional.
func jsSliceBounds(n int, start float64, end *float64) (int, int) {
	relativeStart := jsToIntegerOrInfinity(start)
	var begin float64
	switch {
	case math.IsInf(relativeStart, -1):
		begin = 0
	case relativeStart < 0:
		begin = math.Max(float64(n)+relativeStart, 0)
	default:
		begin = math.Min(relativeStart, float64(n))
	}

	final := float64(n)
	if end != nil {
		relativeEnd := jsToIntegerOrInfinity(*end)
		switch {
		case math.IsInf(relativeEnd, -1):
			final = 0
		case relativeEnd < 0:
			final = math.Max(float64(n)+relativeEnd, 0)
		default:
			final = math.Min(relativeEnd, float64(n))
		}
	}
	if final < begin {
		final = begin
	}
	return int(begin), int(final)
}

// jsArraySlice mirrors Array.prototype.slice(start, end) for string slices;
// end nil is JS `undefined`.
func jsArraySlice(lines []string, start float64, end *float64) []string {
	begin, final := jsSliceBounds(len(lines), start, end)
	return lines[begin:final]
}

// TruncateLines keeps head and tail lines around a truncation marker.
// port of src/compression/utils.ts:truncateLines
func TruncateLines(text string, maxLines float64, headLines float64, tailLines float64) string {
	lines := strings.Split(text, "\n")
	if float64(len(lines)) <= maxLines {
		return text
	}
	head := jsArraySlice(lines, 0, &headLines)
	tailStart := -tailLines
	tail := jsArraySlice(lines, tailStart, nil)
	omitted := float64(len(lines)) - headLines - tailLines
	joined := make([]string, 0, len(head)+1+len(tail))
	joined = append(joined, head...)
	joined = append(joined, "\n... ("+jsNumberToString(omitted)+" lines truncated) ...\n")
	joined = append(joined, tail...)
	return strings.Join(joined, "\n")
}

// TruncateLongOutput applies the global 500/200/100 safety net.
// port of src/compression/utils.ts:truncateLongOutput
func TruncateLongOutput(text string) string {
	return TruncateLines(text, globalMaxLines, globalHeadLines, globalTailLines)
}

// jsStringSlice mirrors String.prototype.slice over UTF-16 code units; end nil
// is JS `undefined`. Unpaired surrogates serialize as U+FFFD, like Node
// writing UTF-8.
func jsStringSlice(s string, start float64, end *float64) string {
	units := utf16.Encode([]rune(s))
	begin, final := jsSliceBounds(len(units), start, end)
	return string(utf16.Decode(units[begin:final]))
}

// jsNumberToString mirrors ECMAScript Number::toString(10), which is what
// string interpolation and template literals use.
func jsNumberToString(value float64) string {
	if math.IsNaN(value) {
		return "NaN"
	}
	if math.IsInf(value, 1) {
		return "Infinity"
	}
	if math.IsInf(value, -1) {
		return "-Infinity"
	}
	if value == 0 {
		return "0"
	}
	negative := value < 0
	if negative {
		value = -value
	}

	repr := strconv.FormatFloat(value, 'e', -1, 64)
	mantissa, exponentPart, _ := strings.Cut(repr, "e")
	exponent, _ := strconv.Atoi(exponentPart)
	digits := strings.Replace(mantissa, ".", "", 1)
	n := len(digits)
	k := exponent + 1

	var out string
	switch {
	case k > 0 && k <= 21:
		if n <= k {
			out = digits + strings.Repeat("0", k-n)
		} else {
			out = digits[:k] + "." + digits[k:]
		}
	case k <= 0 && k > -6:
		out = "0." + strings.Repeat("0", -k) + digits
	default:
		if n == 1 {
			out = digits
		} else {
			out = digits[:1] + "." + digits[1:]
		}
		exponentOut := k - 1
		if exponentOut >= 0 {
			out += "e+" + strconv.Itoa(exponentOut)
		} else {
			out += "e-" + strconv.Itoa(-exponentOut)
		}
	}
	if negative {
		out = "-" + out
	}
	return out
}

// TruncateChars applies a character cap after the line budget, cutting at line
// boundaries unless a single line is larger than the cap.
// port of src/compression/utils.ts:truncateChars
func TruncateChars(text string, maxChars float64, headLines float64, tailLines float64) string {
	if float64(utf16Length(text)) <= maxChars {
		return text
	}
	keep := math.Max(0, maxChars-charMarkerReserve)
	ratio := 0.5
	if headLines+tailLines > 0 {
		ratio = headLines / (headLines + tailLines)
	}
	headBudget := math.Floor(keep * ratio)
	tailBudget := keep - headBudget
	lines := strings.Split(text, "\n")
	n := len(lines)

	h := 0
	used := 0.0
	for h < n && used+float64(utf16Length(lines[h])+1) <= headBudget {
		used += float64(utf16Length(lines[h]) + 1)
		h++
	}
	t := n
	used = 0
	for t-1 >= h && used+float64(utf16Length(lines[t-1])+1) <= tailBudget {
		used += float64(utf16Length(lines[t-1]) + 1)
		t--
	}

	head := strings.Join(lines[:h], "\n")
	tail := strings.Join(lines[t:], "\n")
	if h == 0 && float64(utf16Length(lines[0])) > maxChars {
		head = jsStringSlice(lines[0], 0, &headBudget)
	}
	if t == n && float64(utf16Length(lines[n-1])) > maxChars {
		start := -tailBudget
		tail = jsStringSlice(lines[n-1], start, nil)
	}

	dropped := float64(utf16Length(text)) - float64(utf16Length(head)) - float64(utf16Length(tail))
	marker := "... (" + jsNumberToString(dropped) + " chars truncated) ..."
	parts := make([]string, 0, 3)
	for _, part := range []string{head, marker, tail} {
		if part != "" {
			parts = append(parts, part)
		}
	}
	return strings.Join(parts, "\n")
}

// countLines counts '\n' code units plus one.
// port of src/compression/utils.ts:countLines
func countLines(text string) int {
	n := 1
	for i := 0; i < len(text); i++ {
		if text[i] == '\n' {
			n++
		}
	}
	return n
}

// FitsBudget reports whether text already fits a tool's budget.
// port of src/compression/utils.ts:fitsBudget
func FitsBudget(text string, toolName string, maxBytes ...int) bool {
	limit := BudgetFastPathMaxBytes
	if len(maxBytes) > 0 {
		limit = maxBytes[0]
	}
	if len(text) > limit {
		return false
	}
	budget := GetBudget(toolName)
	if budget.MaxChars != nil && jsNumberTruthy(*budget.MaxChars) &&
		float64(utf16Length(text)) > *budget.MaxChars {
		return false
	}
	return float64(countLines(text)) <= budget.MaxLines
}

// BudgetedText applies ANSI stripping, blank-line collapse, line truncation,
// the global cap and the optional char cap.
// port of src/compression/utils.ts:budgetedText
func BudgetedText(text string, toolName string) string {
	if FitsBudget(text, toolName) {
		return text
	}
	budget := GetBudget(toolName)
	stripped := StripAnsi(text)
	collapsed := CollapseBlankLines(stripped)
	truncated := TruncateLines(
		collapsed,
		budget.MaxLines,
		budget.HeadLines,
		budget.TailLines,
	)
	capped := TruncateLongOutput(truncated)
	if budget.MaxChars != nil && jsNumberTruthy(*budget.MaxChars) {
		return TruncateChars(capped, *budget.MaxChars, budget.HeadLines, budget.TailLines)
	}
	return capped
}

// ApplyBudget budgets text and records the tool's savings.
// port of src/compression/utils.ts:applyBudget
func ApplyBudget(ctx context.Context, text string, toolName string) string {
	rawLength := utf16Length(text)
	result := BudgetedText(text, toolName)
	utilsMu.Lock()
	bucket := statsBucketLocked(ctx)
	tool := bucket.savingsByTool[toolName]
	tool.Calls++
	tool.RawChars += int64(rawLength)
	tool.CompressedChars += int64(utf16Length(result))
	tool.SavedChars += int64(max(0, rawLength-utf16Length(result)))
	bucket.savingsByTool[toolName] = tool
	persistStatsLocked(ctx)
	utilsMu.Unlock()
	return result
}

// stripJSONInsignificantWhitespace drops whitespace outside JSON strings.
// port of src/compression/utils.ts:stripJsonInsignificantWhitespace
func stripJSONInsignificantWhitespace(input string) string {
	var out strings.Builder
	out.Grow(len(input))
	inString := false
	escaped := false
	for i := 0; i < len(input); i++ {
		c := input[i]
		if inString {
			out.WriteByte(c)
			if escaped {
				escaped = false
			} else if c == '\\' {
				escaped = true
			} else if c == '"' {
				inString = false
			}
			continue
		}
		if c == '"' {
			inString = true
			out.WriteByte(c)
		} else if c == ' ' || c == '\t' || c == '\n' || c == '\r' {
			// drop insignificant whitespace outside strings
		} else {
			out.WriteByte(c)
		}
	}
	return out.String()
}

// CompactJSON minifies valid object/array JSON, or returns nil when the input
// is not JSON or is already compact.
// port of src/compression/utils.ts:compactJson
func CompactJSON(text string) *string {
	if utf16Length(text) > maxStructuredInputBytes {
		return nil
	}
	trimmed := jsre.TrimStart(text)
	if !strings.HasPrefix(trimmed, "{") && !strings.HasPrefix(trimmed, "[") {
		return nil
	}
	// Validate before mutating — never reshape malformed JSON.
	if !isValidJSON(text) {
		return nil
	}
	compact := stripJSONInsignificantWhitespace(text)
	if utf16Length(compact) < utf16Length(text) {
		return &compact
	}
	return nil
}

// CompactJSONL minifies valid JSON Lines, or returns nil when any line is not
// JSON or the result is not smaller.
// port of src/compression/utils.ts:compactJsonl
func CompactJSONL(text string) *string {
	if utf16Length(text) > maxStructuredInputBytes {
		return nil
	}
	lines := strings.Split(text, "\n")
	out := make([]string, 0, len(lines))
	for _, raw := range lines {
		line := jsre.Trim(raw)
		if line == "" {
			continue
		}
		if !isValidJSON(line) {
			return nil
		}
		out = append(out, stripJSONInsignificantWhitespace(line))
	}
	if len(out) == 0 {
		return nil
	}
	compact := strings.Join(out, "\n")
	if utf16Length(compact) < utf16Length(text) {
		return &compact
	}
	return nil
}

// ExtractStructuredData chooses compacted JSON/JSONL or whitespace-only XML
// compaction.
// port of src/compression/utils.ts:extractStructuredData
func ExtractStructuredData(text string, commandHint string) string {
	if compacted := CompactJSON(text); compacted != nil {
		return *compacted
	}
	if compacted := CompactJSONL(text); compacted != nil {
		return *compacted
	}
	if strings.HasPrefix(jsre.Trim(text), "<") {
		tagCollapsed := betweenTagsRE.ReplaceAllString(text, "><")
		return spaceRunRE.ReplaceAllString(tagCollapsed, " ")
	}
	return text
}

// ResetRTKAvailabilityCache clears the cached rtk probe.
// port of src/compression/utils.ts:resetRtkAvailabilityCache
func ResetRTKAvailabilityCache() {
	utilsMu.Lock()
	rtkAvailableCache = nil
	utilsMu.Unlock()
}

// isQuoteByte reports whether c is one of " ' `.
func isQuoteByte(c byte) bool { return c == '"' || c == '\'' || c == '`' }

// jsSpaceAt reports whether the rune at byte offset i is JS whitespace.
func jsSpaceAt(s string, i int) bool {
	r, _ := utf8.DecodeRuneInString(s[i:])
	return jsre.IsSpace(r)
}

// matchRtkToken matches /^(?:["'`]rtk(?:\.exe)?["'`]|rtk(?:\.exe)?)(?=\s|$)/i
// by hand (RE2 has no lookahead) and returns the matched byte length.
func matchRtkToken(s string) (int, bool) {
	quoted := false
	rest := s
	if rest != "" && isQuoteByte(rest[0]) {
		quoted = true
		rest = rest[1:]
	}
	if len(rest) < 3 || !strings.EqualFold(rest[:3], "rtk") {
		return 0, false
	}
	nameLen := 3
	if len(rest) >= nameLen+4 && strings.EqualFold(rest[nameLen:nameLen+4], ".exe") {
		nameLen += 4
	}
	end := nameLen
	if quoted {
		if end >= len(rest) || !isQuoteByte(rest[end]) {
			return 0, false
		}
		end++
		end++ // the opening quote
	}
	absoluteEnd := end
	if absoluteEnd < len(s) && !jsSpaceAt(s, absoluteEnd) {
		return 0, false
	}
	return absoluteEnd, true
}

// StripLeadingRtk peels exactly one leading `rtk` argv token, including an
// optional quote pair and ENV=value prefixes.
// port of src/compression/utils.ts:stripLeadingRtk
func StripLeadingRtk(command string) (string, bool) {
	s := jsre.Trim(command)
	if s == "" {
		return command, false
	}

	if len(s) >= 2 && isQuoteByte(s[0]) && s[len(s)-1] == s[0] {
		s = jsre.Trim(s[1 : len(s)-1])
	}

	prefix := ""
	rest := s
	for {
		loc := envAssignPrefixRE.FindStringIndex(rest)
		if loc == nil || loc[0] != 0 {
			break
		}
		prefix += rest[:loc[1]]
		rest = rest[loc[1]:]
	}

	tokenLen, ok := matchRtkToken(rest)
	if !ok {
		return command, false
	}

	after := jsre.TrimStart(rest[tokenLen:])
	rebuilt := jsre.Trim(prefix + after)
	if rebuilt == "" {
		return command, false
	}
	return rebuilt, true
}

// isPathToken reports whether a passthrough token looks like a path.
// port of src/compression/utils.ts:isPathToken
func isPathToken(tok string) bool {
	if strings.HasPrefix(tok, "~") || strings.HasPrefix(tok, "/") || strings.HasPrefix(tok, "\\") {
		return true
	}
	if drivePathRE.MatchString(tok) {
		return true
	}
	return strings.Contains(tok, "/") || strings.Contains(tok, "\\")
}

// redactPassthroughToken clips and redacts one passthrough token.
// port of src/compression/utils.ts:redactPassthroughToken
func redactPassthroughToken(tok string) string {
	clipped := tok
	if utf16Length(tok) > rtkTokenMaxLen {
		clipped = utf16Prefix(tok, rtkTokenMaxLen)
	}
	if isPathToken(tok) {
		return "<path>"
	}
	return RedactSecrets(clipped)
}

// passthroughPrefix renders the first two non-ENV tokens of a command.
// port of src/compression/utils.ts:passthroughPrefix
func passthroughPrefix(command string) string {
	tokens := make([]string, 0, 2)
	for _, part := range jsre.SplitWS(jsre.Trim(command)) {
		if part == "" {
			continue
		}
		if envNameEqRE.MatchString(part) {
			continue
		}
		tokens = append(tokens, redactPassthroughToken(part))
		if len(tokens) == 2 {
			break
		}
	}
	return strings.Join(tokens, " ")
}

// notePassthrough counts one rtk passthrough and tracks its prefix.
// port of src/compression/utils.ts:notePassthrough
func notePassthrough(ctx context.Context, command string) {
	utilsMu.Lock()
	defer utilsMu.Unlock()
	bucket := statsBucketLocked(ctx)
	bucket.rtkPassthrough++
	key := passthroughPrefix(command)
	if key != "" {
		prev, ok := bucket.rtkTopPassthrough.get(key)
		if ok {
			bucket.rtkTopPassthrough.set(key, prev+1)
		} else if bucket.rtkTopPassthrough.size() < rtkTopPassthroughCap {
			bucket.rtkTopPassthrough.set(key, 1)
		} else {
			minKey := ""
			minCount := int64(math.MaxInt64)
			for _, candidate := range bucket.rtkTopPassthrough.order {
				count := bucket.rtkTopPassthrough.values[candidate]
				if count < minCount {
					minCount = count
					minKey = candidate
				}
			}
			if minKey != "" {
				bucket.rtkTopPassthrough.delete(minKey)
				bucket.rtkTopPassthrough.set(key, 1)
			}
		}
		if rt.GetEnv(ctx)["CAVE_TOOLS_DEBUG"] == "1" {
			count, _ := bucket.rtkTopPassthrough.get(key)
			fmt.Fprintf(os.Stderr, "[cave-tools] rtk passthrough %s n=%d\n", key, count)
		}
	}
	persistStatsLocked(ctx)
}

// envList renders an env map for exec.Cmd.
func envList(env map[string]string) []string {
	keys := make([]string, 0, len(env))
	for key := range env {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	out := make([]string, 0, len(keys))
	for _, key := range keys {
		out = append(out, key+"="+env[key])
	}
	return out
}

// runRtkRewrite runs `rtk rewrite <command>` with the 3s timeout.
// port of src/compression/utils.ts:runRtkRewrite
func runRtkRewrite(ctx context.Context, command string) (string, bool) {
	childCtx, cancel := context.WithTimeout(context.Background(), rtkRewriteTimeoutMS*time.Millisecond)
	defer cancel()
	cmd := exec.CommandContext(childCtx, "rtk", "rewrite", command)
	cmd.Env = envList(rt.GetEnv(ctx))
	configureHidden(cmd)
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	err := cmd.Run()
	out := stdout.String()
	if err != nil {
		if childCtx.Err() == context.DeadlineExceeded {
			return "", true
		}
		return out, false
	}
	return out, false
}

// IsRtkAvailable probes `rtk --version` once per process.
// port of src/compression/utils.ts:isRtkAvailable
func IsRtkAvailable() bool {
	utilsMu.Lock()
	if rtkAvailableCache != nil {
		value := *rtkAvailableCache
		utilsMu.Unlock()
		return value
	}
	utilsMu.Unlock()

	probeCtx, cancel := context.WithTimeout(context.Background(), rtkRewriteTimeoutMS*time.Millisecond)
	defer cancel()
	cmd := exec.CommandContext(probeCtx, "rtk", "--version")
	configureHidden(cmd)
	err := cmd.Run()

	utilsMu.Lock()
	value := err == nil
	rtkAvailableCache = &value
	utilsMu.Unlock()
	return value
}

// RewriteCommandWithRtk rewrites a command through rtk, or falls back to the
// peeled command with passthrough accounting.
// port of src/compression/utils.ts:rewriteCommandWithRtk
func RewriteCommandWithRtk(ctx context.Context, command string, incomingAlreadyWrapped bool) string {
	peeledCommand, peeled := StripLeadingRtk(command)
	toRewrite := peeledCommand
	incomingWrapped := incomingAlreadyWrapped || peeled

	if !IsRtkAvailable() {
		notePassthrough(ctx, toRewrite)
		return toRewrite
	}

	stdout, timedOut := runRtkRewrite(ctx, toRewrite)
	rewritten := jsre.Trim(stdout)

	if timedOut || rewritten == "" {
		notePassthrough(ctx, toRewrite)
		return toRewrite
	}

	utilsMu.Lock()
	bucket := statsBucketLocked(ctx)
	if incomingWrapped || rewritten == jsre.Trim(toRewrite) || rewritten == jsre.Trim(command) {
		bucket.rtkAlreadyWrapped++
	} else {
		bucket.rtkRewrites++
	}
	persistStatsLocked(ctx)
	utilsMu.Unlock()
	return rewritten
}

// numberPtr returns a pointer to v (for optional BudgetConfig.MaxChars).
func numberPtr(v float64) *float64 { return &v }
