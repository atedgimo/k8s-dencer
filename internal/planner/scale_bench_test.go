package planner_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/atedgimo/k8s-dencer/internal/api/graph"
	"github.com/atedgimo/k8s-dencer/internal/constraints"
	"github.com/atedgimo/k8s-dencer/internal/impact"
	"github.com/atedgimo/k8s-dencer/internal/model"
	"github.com/atedgimo/k8s-dencer/internal/planner"
	"github.com/atedgimo/k8s-dencer/internal/store"
	sqlitestore "github.com/atedgimo/k8s-dencer/internal/store/sqlite"
)

// Scale benchmarks over synthesised clusters.
//
// The whole pipeline is measured from generated fixtures with no cluster
// involved, which is possible only because internal/model has zero Kubernetes
// imports — a decision from M2 that pays for itself here.
//
// Sizes are chosen to bracket the stated target of 1000 nodes / 50k pods, and
// to make the growth curve visible: if a stage is quadratic, four points will
// say so far more clearly than one.
//
// Run with `make bench`.
//
// Sizes stop at ~2500 pods deliberately. Analyze is roughly cubic today, so
// 5000 pods already costs 34 seconds and the stated target of 50k would take
// hours — and a benchmark nobody will sit through is a benchmark nobody
// maintains. Raise these once M18 has fixed the growth curve. The ceiling is
// the finding, not an accident of the harness.
//
//	SCALE_LARGE=1 make bench   adds the 5000-pod point
var sizes = func() []size {
	s := []size{
		{"120pods", 4},
		{"900pods", 34},
		{"2500pods", 100},
	}
	if os.Getenv("SCALE_LARGE") != "" {
		s = append(s, size{"5000pods", 200})
	}
	return s
}()

type size struct {
	name  string
	nodes int
}

func snapshotFor(nodes int) *model.ClusterSnapshot {
	return model.Synthesize(model.DefaultSynthetic(nodes))
}

func BenchmarkScaleAnalyze(b *testing.B) {
	for _, s := range sizes {
		snap := snapshotFor(s.nodes)
		b.Run(fmt.Sprintf("%s/%dpods", s.name, len(snap.Pods)), func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				constraints.Analyze(snap)
			}
		})
	}
}

func BenchmarkScalePlacement(b *testing.B) {
	for _, s := range sizes {
		snap := snapshotFor(s.nodes)
		b.Run(fmt.Sprintf("%s/%dpods", s.name, len(snap.Pods)), func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				constraints.NewPlacement(snap)
			}
		})
	}
}

// The one most likely to be intractable: greedy nests candidate nodes over
// occupants over candidate nodes again, with an occupant scan inside CanPlace.
func BenchmarkScalePlan(b *testing.B) {
	for _, s := range sizes {
		snap := snapshotFor(s.nodes)
		analysis := constraints.Analyze(snap)
		b.Run(fmt.Sprintf("%s/%dpods", s.name, len(snap.Pods)), func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				if _, err := (planner.Greedy{}).Plan(snap, analysis, planner.DefaultOptions()); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkScaleClassify(b *testing.B) {
	for _, s := range sizes {
		snap := snapshotFor(s.nodes)
		analysis := constraints.Analyze(snap)
		plan, err := (planner.Greedy{}).Plan(snap, analysis, planner.DefaultOptions())
		if err != nil {
			b.Fatal(err)
		}
		b.Run(fmt.Sprintf("%s/%dpods", s.name, len(snap.Pods)), func(b *testing.B) {
			classifier := impact.New(impact.DefaultThresholds())
			b.ReportAllocs()
			for b.Loop() {
				classifier.ClassifyPlan(plan, snap, analysis)
			}
		})
	}
}

// The payload the browser is asked to parse. Reported in bytes as well as
// time, because the size is the part that breaks first.
func BenchmarkScaleGraphPayload(b *testing.B) {
	for _, s := range sizes {
		snap := snapshotFor(s.nodes)
		analysis := constraints.Analyze(snap)
		plan, err := (planner.Greedy{}).Plan(snap, analysis, planner.DefaultOptions())
		if err != nil {
			b.Fatal(err)
		}
		b.Run(fmt.Sprintf("%s/%dpods", s.name, len(snap.Pods)), func(b *testing.B) {
			b.ReportAllocs()
			var size int
			for b.Loop() {
				payload := graph.Build(plan, snap, analysis)
				raw, err := json.Marshal(payload)
				if err != nil {
					b.Fatal(err)
				}
				size = len(raw)
			}
			b.ReportMetric(float64(size)/1024/1024, "MB/response")
		})
	}
}

// Every plan change writes the whole snapshot and analysis as JSON blobs.
// Reported in megabytes per row, which is the number that decides how quickly
// the volume fills.
func BenchmarkScaleStoreSave(b *testing.B) {
	for _, s := range sizes {
		snap := snapshotFor(s.nodes)
		analysis := constraints.Analyze(snap)
		plan, err := (planner.Greedy{}).Plan(snap, analysis, planner.DefaultOptions())
		if err != nil {
			b.Fatal(err)
		}
		snapJSON, _ := json.Marshal(snap)
		analysisJSON, _ := json.Marshal(analysis)

		b.Run(fmt.Sprintf("%s/%dpods", s.name, len(snap.Pods)), func(b *testing.B) {
			db, err := sqlitestore.Open(filepath.Join(b.TempDir(), "bench.db"))
			if err != nil {
				b.Fatal(err)
			}
			b.Cleanup(func() { _ = db.Close() })
			if err := db.Migrate(context.Background()); err != nil {
				b.Fatal(err)
			}

			b.ReportAllocs()

			n := 0
			for b.Loop() {
				// A distinct ID each time: Save deduplicates on content hash,
				// and measuring the dedup path would measure nothing.
				n++
				p := *plan
				p.ID = fmt.Sprintf("plan-%d", n)
				if _, err := db.Save(context.Background(), store.Record{
					Plan: &p, Snapshot: snap, Analysis: analysis, Strategy: "bench",
				}); err != nil {
					b.Fatal(err)
				}
			}

			// Report what is actually on disk, not the JSON that went in. Since
			// M17 the blobs are gzipped, and a metric that kept reporting the
			// uncompressed size would overstate retention by an order of
			// magnitude — the exact thing this benchmark exists to track.
			var stored int64
			row := db.QueryRowForTest(context.Background(),
				`SELECT length(snapshot) + length(analysis) FROM plans WHERE id = ?`,
				fmt.Sprintf("plan-%d", n))
			if err := row.Scan(&stored); err != nil {
				b.Fatal(err)
			}
			raw := len(snapJSON) + len(analysisJSON)
			b.ReportMetric(float64(stored)/1024/1024, "MB/row")
			b.ReportMetric(float64(raw)/float64(stored), "compression")
		})
	}
}
