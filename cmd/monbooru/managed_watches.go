package main

import (
	"cmp"
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

func (a *managedApplication) AddWatch(ctx context.Context, url, gallery string, hours, limit int) error {
	gallery = cmp.Or(gallery, a.owner.set.DefaultName())
	if a.owner.live == nil || limit > a.owner.live.MaxItems || a.owner.galleryIdentity(gallery) == "" {
		return web.ErrManagedImportUnsupported
	}
	err := a.call(ctx, workerproto.WatchAdd, workerproto.QueueCommand{URL: url, Gallery: gallery, IntervalHours: hours, MaxItems: limit}, nil)
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
			_, err := a.submitImport(ctx, watch.URL, a.watchGallery(watch), watch.MaxItems, watch.ID)
			return err
		}
	}
	return web.ErrManagedImportUnsupported
}

func (a *managedApplication) CancelWatch(ctx context.Context, id int64) error {
	watches, err := a.ListWatches(ctx)
	if err != nil {
		return err
	}
	for _, watch := range watches {
		if watch.ID == id && watch.LiveJob > 0 {
			return a.Cancel(ctx, watch.LiveJob)
		}
	}
	return web.ErrManagedImportUnsupported
}

func (a *managedApplication) watchGallery(watch web.LoaderWatch) string {
	return cmp.Or(watch.Gallery, a.owner.set.DefaultName())
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
		destination := a.watchGallery(watch)
		source := workerproto.StrictQueueSource(watch.URL)
		if source == "" || a.owner.live.SourceOptions[source] == "-" ||
			a.owner.galleryIdentity(destination) == "" || watch.MaxItems > a.owner.live.MaxItems {
			continue
		}
		_, err := a.submitImport(ctx, watch.URL, destination, watch.MaxItems, watch.ID)
		return err
	}
	return nil
}
