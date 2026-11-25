package queue

import (
	"context"
	"testing"
	"time"

	"github.com/monbooru/monloader/internal/outcome"
)

type restartTestProcessor func(context.Context, *Job) error

func (p restartTestProcessor) Process(ctx context.Context, job *Job) error {
	return p(ctx, job)
}

func TestQueueRestartPreservesContinuationAndExplicitRetry(t *testing.T) {
	const search = "https://e621.net/posts?tags=fixture_bulk"
	const gallery = "Audit_A"
	const windowSize = 200
	now := time.Now()
	dir := t.TempDir()
	store, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	storeClosed := false
	defer func() {
		if !storeClosed {
			if err := store.Close(); err != nil {
				t.Error(err)
			}
		}
	}()

	root := newJob(1, search, Options{Gallery: gallery, Root: 1, MaxItems: windowSize}, now)
	root.SetCapped(windowSize)
	root.SetItems([]Item{{
		PostID: "42", Status: ItemDone, Outcome: outcome.Created, MonbooruID: 7, SHA256: "fixture-hash",
	}})
	root.Finalize(now)

	// A crash leaves the last running snapshot in SQLite.
	interrupted := newJob(2, search, Options{
		Gallery: gallery, Root: root.ID, MaxItems: windowSize, Offset: windowSize,
	}, now)
	if err := interrupted.start(now); err != nil {
		t.Fatal(err)
	}
	queued := newJob(3, "https://e621.net/posts/99", Options{Gallery: "Audit_B", Root: 3}, now)
	for _, job := range []*Job{root, interrupted, queued} {
		if err := store.SaveJob(job.Snapshot()); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	storeClosed = true

	reopened, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := reopened.Close(); err != nil {
			t.Error(err)
		}
	}()
	started := make(chan *Job, 3)
	q := New(restartTestProcessor(func(_ context.Context, job *Job) error {
		started <- job.Snapshot()
		if job.ID == interrupted.ID {
			job.SetCapped(windowSize)
		}
		return nil
	}), 1, 10)
	defer q.Close()
	q.UseStore(reopened)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	completed, err := q.Wait(ctx, root.ID)
	if err != nil {
		t.Fatal(err)
	}
	if completed.Status != JobSucceeded || !completed.Capped || completed.Summary.Created != 1 ||
		len(completed.Items) != 1 || completed.Items[0].MonbooruID != 7 || completed.Items[0].SHA256 != "fixture-hash" {
		t.Fatalf("completed window did not survive restart: %+v", completed)
	}
	stopped, err := q.Wait(ctx, interrupted.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stopped.Status != JobInterrupted || stopped.Root != root.ID || stopped.Offset != windowSize || stopped.Gallery != gallery {
		t.Fatalf("interrupted window did not retain its position: %+v", stopped)
	}
	if running, pending := q.InFlight(); running != 0 || pending != 1 {
		t.Fatalf("after restart got %d running and %d queued, want 0 and 1", running, pending)
	}

	q.Start()
	waitForStart := func() *Job {
		t.Helper()
		select {
		case job := <-started:
			return job
		case <-ctx.Done():
			t.Fatal(ctx.Err())
			return nil
		}
	}
	if job := waitForStart(); job.ID != queued.ID || job.Gallery != "Audit_B" || job.Force {
		t.Fatalf("worker should run the queued job first: %+v", job)
	}
	if err := q.Retry(interrupted.ID, false); err != nil {
		t.Fatal(err)
	}
	if job := waitForStart(); job.ID != interrupted.ID || job.Root != root.ID ||
		job.Offset != windowSize || job.MaxItems != windowSize || job.Gallery != gallery || !job.Force {
		t.Fatalf("retry should replay the interrupted window with archive bypass: %+v", job)
	}
	if job, err := q.Wait(ctx, interrupted.ID); err != nil || job.Status != JobSucceeded {
		t.Fatalf("retry did not finish successfully: job=%+v err=%v", job, err)
	}

	q.Pause()
	nextID, err := q.Continue(root.ID)
	if err != nil {
		t.Fatal(err)
	}
	next, err := q.Get(nextID)
	if err != nil {
		t.Fatal(err)
	}
	if nextID <= queued.ID || next.Root != root.ID || next.Offset != 2*windowSize ||
		next.MaxItems != windowSize || next.Gallery != gallery || next.Force {
		t.Fatalf("continuation should advance to the third window: %+v", next)
	}
}
