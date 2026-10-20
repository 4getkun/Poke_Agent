//go:build integration

package sqlstore

import (
	"context"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"

	d "pokegents/dashboard/server/taskdomain"
)

const (
	at24MessageCount = 100_000
	at24BodyBytes    = 2_250
	at24MarkerRows   = 101
	at24PageLimit    = 100
	at24MinBytes     = 200 * 1024 * 1024
	at24Rounds       = 3
)

type at24SearchCase struct {
	category string
	term     string
	wantIDs  []string
}

type at24Sample struct {
	duration    time.Duration
	interrupted bool
}

func TestAT24SearchPerformance(t *testing.T) {
	if os.Getenv("POKEGENTS_RUN_AT24") != "1" {
		t.Skip("set POKEGENTS_RUN_AT24=1 to build and search the AT-24 fixture")
	}

	previousProcs := runtime.GOMAXPROCS(4)
	defer runtime.GOMAXPROCS(previousProcs)
	var memory runtime.MemStats
	runtime.ReadMemStats(&memory)
	t.Logf("AT-24 environment go=%s os=%s arch=%s NumCPU=%d GOMAXPROCS=%d previousGOMAXPROCS=%d runtimeSysBytes=%d; GOMAXPROCS=4 limits Go parallelism only and is not evidence of a 4-core/16-GiB physical machine; physical RAM was not measured",
		runtime.Version(), runtime.GOOS, runtime.GOARCH, runtime.NumCPU(), runtime.GOMAXPROCS(0), previousProcs, memory.Sys)

	ctx := context.Background()
	store := openFixtureStore(t, filepath.Join(t.TempDir(), "at24.sqlite"))
	task := createFixtureTask(t, store)
	queries := at24Cases()
	markersByRow := make(map[int][]string)
	categoryOffsets := map[string]int{"long": 0, "symbol-long": 0, "japanese": 0}
	for i := range queries {
		q := &queries[i]
		switch q.category {
		case "long", "symbol-long", "japanese":
			row := categoryOffsets[q.category]
			categoryOffsets[q.category]++
			markersByRow[row] = append(markersByRow[row], q.term)
			q.wantIDs = []string{at24ID(row)}
		case "short", "symbol-short":
			q.wantIDs = make([]string, 0, at24MarkerRows)
			for row := at24MessageCount - at24MarkerRows; row < at24MessageCount; row++ {
				markersByRow[row] = append(markersByRow[row], q.term)
				q.wantIDs = append(q.wantIDs, at24ID(row))
			}
		default:
			t.Fatalf("unknown AT-24 category %q", q.category)
		}
	}

	started := time.Now()
	tx, err := store.DB().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	createdAt := d.Now()
	for row := 0; row < at24MessageCount; row++ {
		message := d.Message{
			ID:     at24ID(row),
			TaskID: task.ID,
			Role:   "user",
			Body:   at24Body(markersByRow[row]),
			At:     createdAt,
		}
		if err := AddMessage(ctx, tx, message); err != nil {
			_ = tx.Rollback()
			t.Fatalf("insert fixture row %d: %v", row, err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit AT-24 fixture: %v", err)
	}
	insertDuration := time.Since(started)

	var messageCount, bodyBytes int64
	if err := store.DB().QueryRowContext(ctx, `SELECT count(*), coalesce(sum(length(CAST(body AS BLOB))),0) FROM messages WHERE task_id=?`, task.ID).Scan(&messageCount, &bodyBytes); err != nil {
		t.Fatalf("verify fixture size: %v", err)
	}
	if messageCount != at24MessageCount {
		t.Fatalf("fixture message count=%d, want %d", messageCount, at24MessageCount)
	}
	if bodyBytes < at24MinBytes {
		t.Fatalf("fixture body bytes=%d, want at least %d", bodyBytes, at24MinBytes)
	}
	t.Logf("AT-24 fixture messages=%d body_bytes=%d body_MiB=%.2f inserted=%s", messageCount, bodyBytes, float64(bodyBytes)/(1024*1024), insertDuration)

	// Verify each query's expected IDs before recording performance samples.
	for _, q := range queries {
		page, err := store.Messages(ctx, task.ID, q.term, "", 0, at24PageLimit)
		if err != nil {
			t.Fatalf("preflight %s %q: %v", q.category, q.term, err)
		}
		if page.Interrupted {
			if !at24AllowsInterrupted(q) {
				t.Fatalf("preflight %s %q was interrupted", q.category, q.term)
			}
			assertAT24SQLIDs(t, ctx, store, task.ID, q.term, q.wantIDs)
			t.Logf("AT-24 preflight category=%s term=%q status=interrupted; direct SQL confirmed expected IDs", q.category, q.term)
			continue
		}
		assertAT24Page(t, q, page)
	}

	// Warm every query once, then measure serially to avoid self-contention.
	for _, q := range queries {
		page, err := store.Messages(ctx, task.ID, q.term, "", 0, at24PageLimit)
		if err != nil {
			t.Fatalf("warmup %s %q: %v", q.category, q.term, err)
		}
		if page.Interrupted && !at24AllowsInterrupted(q) {
			t.Fatalf("warmup %s %q was interrupted", q.category, q.term)
		}
		if !page.Interrupted {
			assertAT24Page(t, q, page)
		}
	}

	samples := make(map[string][]at24Sample)
	for round := 0; round < at24Rounds; round++ {
		for _, q := range queries {
			started := time.Now()
			page, err := store.Messages(ctx, task.ID, q.term, "", 0, at24PageLimit)
			elapsed := time.Since(started)
			if err != nil {
				t.Fatalf("measure %s %q round=%d: %v", q.category, q.term, round+1, err)
			}
			if page.Interrupted && !at24AllowsInterrupted(q) {
				t.Fatalf("measure %s %q round=%d was interrupted", q.category, q.term, round+1)
			}
			if !page.Interrupted {
				assertAT24Page(t, q, page)
			}
			samples[q.category] = append(samples[q.category], at24Sample{duration: elapsed, interrupted: page.Interrupted})
		}
	}

	thresholds := map[string]time.Duration{
		"long":         500 * time.Millisecond,
		"symbol-long":  500 * time.Millisecond,
		"japanese":     500 * time.Millisecond,
		"short":        2 * time.Second,
		"symbol-short": 2 * time.Second,
	}
	for _, category := range []string{"long", "short", "symbol-long", "symbol-short", "japanese"} {
		categorySamples := samples[category]
		median, p95 := at24Percentiles(categorySamples)
		interrupted := 0
		for _, sample := range categorySamples {
			if sample.interrupted {
				interrupted++
			}
		}
		threshold := thresholds[category]
		t.Logf("AT-24 search category=%s samples=%d interrupted=%d median=%s p95=%s threshold=%s", category, len(categorySamples), interrupted, median, p95, threshold)
		if p95 > threshold {
			if at24CategoryAllowsInterrupted(category) && interrupted == len(categorySamples) {
				t.Logf("AT-24 category=%s p95 exceeded the nominal limit only as explicit 2s interruptions", category)
				continue
			}
			t.Errorf("AT-24 %s p95=%s exceeds %s", category, p95, threshold)
		}
	}
}

func at24Cases() []at24SearchCase {
	return []at24SearchCase{
		{category: "long", term: "longneedlealpha"},
		{category: "long", term: "longneedlebravo"},
		{category: "long", term: "longneedlecharlie"},
		{category: "long", term: "longneedledelta"},
		{category: "long", term: "longneedleepsilon"},
		{category: "short", term: "猫"},
		{category: "short", term: "狐"},
		{category: "short", term: "亀"},
		{category: "short", term: "雪"},
		{category: "short", term: "犬鳥"},
		{category: "symbol-short", term: "%_"},
		{category: "symbol-long", term: `"quoted-probe-01"`},
		{category: "symbol-long", term: `(parenthesis-probe-02)`},
		{category: "symbol-long", term: `back\slash\probe03`},
		{category: "symbol-long", term: `underscore_probe04`},
		{category: "japanese", term: "日本語一致候補"},
		{category: "japanese", term: "検索性能検査"},
		{category: "japanese", term: "本文検索対象"},
		{category: "japanese", term: "試験結果照合"},
		{category: "japanese", term: "ワークスペース確認"},
	}
}

func at24AllowsInterrupted(q at24SearchCase) bool {
	return q.category == "short" || (q.category == "symbol-short" && len([]rune(q.term)) == 2)
}

func at24CategoryAllowsInterrupted(category string) bool {
	return category == "short" || category == "symbol-short"
}

func at24Body(markers []string) string {
	if len(markers) == 0 {
		return strings.Repeat("ABCD", (at24BodyBytes+3)/4)[:at24BodyBytes]
	}
	prefix := strings.Join(markers, " ")
	if len(prefix) > at24BodyBytes {
		panic("AT-24 marker body exceeds target size")
	}
	return prefix + strings.Repeat("A", at24BodyBytes-len(prefix))
}

func at24ID(row int) string { return fmt.Sprintf("at24-%06d", row) }

func assertAT24Page(t *testing.T, q at24SearchCase, page Page) {
	t.Helper()
	expectedCount := len(q.wantIDs)
	wantCursor := int64(0)
	if expectedCount > at24PageLimit {
		expectedCount = at24PageLimit
		wantCursor = 1 // Messages assigns the sequence of the last returned row.
	}
	if len(page.Items) != expectedCount {
		t.Fatalf("%s %q returned %d items, want %d", q.category, q.term, len(page.Items), expectedCount)
	}
	for i, item := range page.Items {
		if item.ID != q.wantIDs[i] {
			t.Fatalf("%s %q item[%d].ID=%q, want %q", q.category, q.term, i, item.ID, q.wantIDs[i])
		}
	}
	if (page.NextCursor == 0) != (wantCursor == 0) {
		t.Fatalf("%s %q next cursor=%d, want presence=%t", q.category, q.term, page.NextCursor, wantCursor != 0)
	}
}

func assertAT24SQLIDs(t *testing.T, ctx context.Context, store *Store, taskID, term string, want []string) {
	t.Helper()
	rows, err := store.DB().QueryContext(ctx, `SELECT id FROM messages WHERE task_id=? AND instr(normalized,?)>0 ORDER BY seq LIMIT ?`, taskID, Normalize(term), len(want)+1)
	if err != nil {
		t.Fatalf("verify interrupted short-query fixture %q: %v", term, err)
	}
	defer rows.Close()
	got := make([]string, 0, len(want))
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatalf("scan direct SQL fixture match: %v", err)
		}
		got = append(got, id)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate direct SQL fixture matches: %v", err)
	}
	if len(got) != len(want) {
		t.Fatalf("direct SQL for %q returned %d IDs, want %d", term, len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("direct SQL for %q ID[%d]=%q, want %q", term, i, got[i], want[i])
		}
	}
}

func at24Percentiles(samples []at24Sample) (time.Duration, time.Duration) {
	values := make([]time.Duration, len(samples))
	for i, sample := range samples {
		values[i] = sample.duration
	}
	sort.Slice(values, func(i, j int) bool { return values[i] < values[j] })
	median := values[len(values)/2]
	p95 := values[int(math.Ceil(0.95*float64(len(values))))-1]
	return median, p95
}
