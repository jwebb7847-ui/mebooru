package main

import (
	"context"
	"time"

	"github.com/jwebb7847-ui/mebooru/shared/workerproto"
	"github.com/monbooru/monbooru/internal/web"
)

func (a *managedApplication) ListWatches(ctx context.Context) ([]web.LoaderWatch, error) {
	var result struct {
		Watches []web.LoaderWatch `json:"watches"`
	}
	err := a.call(ctx, workerproto.WatchList, workerproto.QueueCommand{}, &result)
	return result.Watches, err
}

func (a *managedApplication) AddWatch(ctx context.Context, url string, hours, limit int) error {
	err := a.call(ctx, workerproto.WatchAdd, workerproto.QueueCommand{URL: url, IntervalHours: hours, MaxItems: limit}, nil)
	if err == nil {
		a.notify()
	}
	return err
}

func (a *managedApplication) RemoveWatch(ctx context.Context, id int64) error {
	return a.call(ctx, workerproto.WatchRemove, workerproto.QueueCommand{WatchID: id}, nil)
}

func (a *managedApplication) ScheduleWatch(ctx context.Context, id int64, hours int) error {
	err := a.call(ctx, workerproto.WatchSetInterval, workerproto.QueueCommand{WatchID: id, IntervalHours: hours}, nil)
	if err == nil {
		a.notify()
	}
	return err
}

func (a *managedApplication) CheckWatch(ctx context.Context, id int64) error {
	watches, err := a.ListWatches(ctx)
	if err != nil {
		return err
	}
	for _, watch := range watches {
		if watch.ID == id && !watch.Live {
			_, err := a.submitImport(ctx, watch.URL, a.owner.set.DefaultName(), watch.MaxItems, watch.ID)
			return err
		}
	}
	return web.ErrManagedImportUnsupported
}

func (a *managedApplication) enqueueDueWatch(ctx context.Context, now time.Time) error {
	watches, err := a.ListWatches(ctx)
	if err != nil {
		return err
	}
	for _, watch := range watches {
		if watch.Live || watch.IntervalHours <= 0 || watch.MaxItems <= 0 ||
			now.Before(watch.Last.CheckedAt.Add(time.Duration(watch.IntervalHours)*time.Hour)) {
			continue
		}
		_, err := a.submitImport(ctx, watch.URL, a.owner.set.DefaultName(), watch.MaxItems, watch.ID)
		return err
	}
	return nil
}
