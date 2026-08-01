package sqlite

import (
	"context"
	"fmt"
	"time"

	"github.com/atedgimo/k8s-dencer/internal/store"
)

func (s *Store) SaveSample(ctx context.Context, sm store.Sample) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO samples (taken_at, nodes, pods,
			cpu_req_milli, cpu_alloc_milli, mem_req_bytes, mem_alloc_bytes,
			cpu_used_milli, mem_used_bytes, has_usage, reclaimable)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		sm.TakenAt.UTC().Format(time.RFC3339Nano), sm.Nodes, sm.Pods,
		sm.CPUReqMilli, sm.CPUAllocMilli, sm.MemReqBytes, sm.MemAllocBytes,
		sm.CPUUsedMilli, sm.MemUsedBytes, boolToInt(sm.HasUsage), sm.Reclaimable)
	if err != nil {
		return fmt.Errorf("save sample: %w", err)
	}
	return nil
}

func (s *Store) Samples(ctx context.Context, since time.Time) ([]store.Sample, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT taken_at, nodes, pods,
		       cpu_req_milli, cpu_alloc_milli, mem_req_bytes, mem_alloc_bytes,
		       cpu_used_milli, mem_used_bytes, has_usage, reclaimable
		FROM samples WHERE taken_at >= ? ORDER BY taken_at`,
		since.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return nil, fmt.Errorf("list samples: %w", err)
	}
	defer func() { _ = rows.Close() }()

	out := []store.Sample{}
	for rows.Next() {
		var sm store.Sample
		var takenAt string
		var hasUsage int
		if err := rows.Scan(&takenAt, &sm.Nodes, &sm.Pods,
			&sm.CPUReqMilli, &sm.CPUAllocMilli, &sm.MemReqBytes, &sm.MemAllocBytes,
			&sm.CPUUsedMilli, &sm.MemUsedBytes, &hasUsage, &sm.Reclaimable); err != nil {
			return nil, err
		}
		sm.TakenAt = parseTime(takenAt)
		sm.HasUsage = hasUsage != 0
		out = append(out, sm)
	}
	return out, rows.Err()
}

func (s *Store) PruneSamples(ctx context.Context, before time.Time) (int, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM samples WHERE taken_at < ?`,
		before.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return 0, fmt.Errorf("prune samples: %w", err)
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}
