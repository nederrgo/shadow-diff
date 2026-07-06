package engine

import (
	"context"
	"hash/fnv"
	"log"
	"time"

	"github.com/shadow-diff/beru/internal/storage"
	"github.com/shadow-diff/beru/internal/v2/diff"
	v2storage "github.com/shadow-diff/beru/internal/v2/storage"
)

type TraceRouter struct {
	workers  []chan *v2storage.RawReport
	repo     v2storage.TraceRepository
	runs     *storage.DB
}

func NewTraceRouter(workerCount int, repo v2storage.TraceRepository, runs *storage.DB) *TraceRouter {
	if workerCount < 1 {
		workerCount = 1
	}
	tr := &TraceRouter{
		workers: make([]chan *v2storage.RawReport, workerCount),
		repo:    repo,
		runs:    runs,
	}
	for i := 0; i < workerCount; i++ {
		tr.workers[i] = make(chan *v2storage.RawReport, 2048)
		go tr.startWorker(tr.workers[i])
	}
	return tr
}

func (tr *TraceRouter) Route(report *v2storage.RawReport) {
	if report == nil {
		return
	}
	hasher := fnv.New32a()
	hasher.Write([]byte(report.TraceID))
	workerIdx := hasher.Sum32() % uint32(len(tr.workers))
	tr.workers[workerIdx] <- report
}

func (tr *TraceRouter) startWorker(ch chan *v2storage.RawReport) {
	for report := range ch {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)

		if tr.runs != nil && report.ShadowTestName != "" {
			_ = tr.runs.EnsureShadowTest(ctx, report.ShadowTestName)
		}

		history, err := tr.repo.AppendReport(ctx, report)
		if err != nil {
			log.Printf("[Engine] Database append fault for trace %s: %v", report.TraceID, err)
			cancel()
			continue
		}

		verdict := diff.EvaluateTraceHistory(history)

		if err := tr.repo.SaveDiffVerdict(ctx, report.TraceID, verdict); err != nil {
			log.Printf("[Engine] State execution save fault for trace %s: %v", report.TraceID, err)
		} else {
			mirrorLegacyLogs(report.TraceID, history, verdict)
		}
		cancel()
	}
}
