package reference

import (
	"context"
	"testing"

	"github.com/proshop-app/proshop-marketplace/internal/application/ports"
	"github.com/proshop-app/proshop-marketplace/internal/domain/reference"
)

type statsReads struct {
	ports.UnitOfWork
	counts int
}

type statsReadTx struct {
	ports.Tx
	owner *statsReads
}

type statsReadRepository struct {
	reference.Repository
	owner *statsReads
}

func (s *statsReads) Do(ctx context.Context, run func(context.Context, ports.Tx) error) error {
	return s.UnitOfWork.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		return run(ctx, statsReadTx{Tx: tx, owner: s})
	})
}

func (s statsReadTx) Reference() reference.Repository {
	return statsReadRepository{Repository: s.Tx.Reference(), owner: s.owner}
}

func (s statsReadRepository) CandidateCounts(ctx context.Context, codes []string) (map[string]int64, error) {
	if len(codes) > 0 {
		s.owner.counts++
	}
	return s.Repository.CandidateCounts(ctx, codes)
}

func TestStatsOnlyCountsChangedDictionaries(t *testing.T) {
	h := newHarness(t)
	reads := &statsReads{UnitOfWork: h.store}
	h.service.uow = reads
	ctx := context.Background()
	for i := 0; i < 2; i++ {
		rows, err := h.service.Stats(ctx)
		if err != nil || len(rows) != 1 || rows[0].Candidates != 0 || reads.counts != 1 {
			t.Fatalf("rows=%v counts=%d err=%v", rows, reads.counts, err)
		}
	}
	h.create(t, "Запись", reference.StatusCandidate)
	rows, err := h.service.Stats(ctx)
	if err != nil || rows[0].Candidates != 1 || reads.counts != 2 {
		t.Fatalf("rows=%v counts=%d err=%v", rows, reads.counts, err)
	}
}
