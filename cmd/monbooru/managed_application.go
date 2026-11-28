package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	"github.com/jwebb7847-ui/mebooru/shared/logx"
	"github.com/jwebb7847-ui/mebooru/shared/managedqa"
	"github.com/jwebb7847-ui/mebooru/shared/windowregistry"
	"github.com/jwebb7847-ui/mebooru/shared/workerproto"
	"github.com/monbooru/monbooru/internal/acquisitionpolicy"
	"github.com/monbooru/monbooru/internal/acquisitionstage"
	"github.com/monbooru/monbooru/internal/config"
	"github.com/monbooru/monbooru/internal/library"
	"github.com/monbooru/monbooru/internal/monloader"
	"github.com/monbooru/monbooru/internal/web"
)

func disposableManagedConfig(dir string) (*config.Config, managedqa.Setup, error) {
	setup, err := managedqa.Load(dir)
	if err != nil || !setup.Application {
		return nil, setup, managedqa.ErrFixture
	}
	for _, name := range []string{"data", "gallery", "stage", "queue", "windows"} {
		info, err := os.Lstat(filepath.Join(dir, name))
		if err != nil || !info.IsDir() {
			return nil, setup, managedqa.ErrFixture
		}
	}
	cfg := config.Default()
	cfg.Server.BindAddress = "127.0.0.1:0"
	cfg.Server.BaseURL = ""
	cfg.SetupDone = true
	cfg.Log.Level = "info"
	cfg.Paths.DataPath = filepath.Join(dir, "data")
	cfg.Paths.ModelPath = filepath.Join(dir, "data", "models")
	cfg.DefaultGallery = managedqa.Gallery
	cfg.Galleries = []config.Gallery{{
		Name:           managedqa.Gallery,
		GalleryPath:    filepath.Join(dir, "gallery"),
		DBPath:         filepath.Join(dir, "data", "gallery.sqlite"),
		ThumbnailsPath: filepath.Join(dir, "data", "thumbs"),
	}}
	if setup.Destination != "" {
		root := filepath.Join(dir, "second-gallery")
		if info, err := os.Lstat(root); err != nil || !info.IsDir() {
			return nil, setup, managedqa.ErrFixture
		}
		cfg.Galleries = append(cfg.Galleries, config.Gallery{
			Name:           setup.Destination,
			GalleryPath:    root,
			DBPath:         filepath.Join(dir, "data", "second.sqlite"),
			ThumbnailsPath: filepath.Join(dir, "data", "second-thumbs"),
		})
	}
	cfg.Gallery.WatchEnabled = false
	cfg.Gallery.DefaultUploadName = "{source}-{post_id}"
	cfg.Schedule = config.ScheduleConfig{}
	return cfg, setup, nil
}

// managedApplication connects the library HTTP handlers to the same
// private queue, custodian and gallery owner used by the executable probe.
type managedApplication struct {
	manager  *acquisitionpolicy.Manager
	owner    *managedWindowOwner
	store    *acquisitionpolicy.Store
	cancel   context.CancelFunc
	done     chan struct{}
	wake     chan struct{}
	policyMu sync.Mutex
}

func openManagedApplication(dir string, setup managedqa.Setup, live *windowregistry.LiveConfig, set *library.Set, naming config.GalleryConfig) (*managedApplication, error) {
	self, err := os.Executable()
	if err != nil {
		return nil, err
	}
	lock, err := acquisitionpolicy.LockProfile(dir)
	if err != nil {
		return nil, err
	}
	store, err := acquisitionpolicy.Open(dir)
	if err != nil {
		lock.Close()
		return nil, err
	}
	closePolicy := func() { store.Close(); lock.Close() }
	policy, err := acquisitionpolicy.New(store)
	if err != nil {
		closePolicy()
		return nil, err
	}
	registry, err := windowregistry.Open(filepath.Join(dir, "windows"))
	if err != nil {
		closePolicy()
		return nil, err
	}
	o := &managedWindowOwner{dir: dir, setup: setup, live: live, set: set, allowed: make(map[string]workerproto.StageAllocateRequest), registry: registry, naming: naming}
	maxCount, maxBytes := 2, uint64(32<<20)
	if live != nil {
		maxCount, maxBytes = 16, 4<<30
	}
	o.a, err = acquisitionstage.Open(filepath.Join(dir, "stage"), maxCount, maxBytes, o.scope)
	if err != nil {
		registry.Close()
		closePolicy()
		return nil, err
	}
	if live != nil {
		if err := o.a.CheckInventory(context.Background()); err != nil {
			o.a.Close()
			registry.Close()
			closePolicy()
			return nil, err
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	m, err := acquisitionpolicy.NewManager(ctx, lock, policy, func(start, lifetime context.Context, f *acquisitionpolicy.WorkerFence) (acquisitionpolicy.ManagedPeer, error) {
		o.mu.Lock()
		o.generation = f.Generation()
		o.lease.Release()
		o.lease = nil
		o.window = windowregistry.Grant{}
		o.mu.Unlock()
		cmd := exec.Command(self, "internal-managed-window-custody-v1", dir, f.Generation())
		cmd.Stderr = os.Stderr
		return acquisitionpolicy.StartCustodySession(start, lifetime, cmd, f, o.handle)
	}, acquisitionpolicy.LifecycleOptions{})
	if err != nil {
		cancel()
		o.a.Close()
		registry.Close()
		closePolicy()
		return nil, err
	}
	if live != nil {
		if err := saveLiveConfig(dir, *live); err != nil {
			cancel()
			o.a.Close()
			registry.Close()
			m.Shutdown(context.Background())
			store.Close()
			return nil, err
		}
	}
	o.bindOnline = func() (*acquisitionpolicy.OnlineLease, error) { return m.ObserveOnline(ctx) }
	app := &managedApplication{manager: m, owner: o, store: store, cancel: cancel, done: make(chan struct{}), wake: make(chan struct{}, 1)}
	go app.run(ctx)
	return app, nil
}

func (a *managedApplication) run(ctx context.Context) {
	defer close(a.done)
	tick := time.NewTicker(5 * time.Second)
	defer tick.Stop()
	for {
		a.policyMu.Lock()
		call, cancel := context.WithTimeout(ctx, 10*time.Second)
		demand, err := acquisitionpolicy.ReadQueueDemand(call, filepath.Join(a.owner.dir, "queue"), time.Now(), false)
		if err == nil {
			err = a.manager.TickNetworkDemand(call, demand.Queued)
		}
		if err == nil && demand.Queued && a.manager.EffectiveOnline() {
			err = a.call(call, workerproto.QueueResume, workerproto.QueueCommand{}, nil)
		}
		cancel()
		a.policyMu.Unlock()
		if err != nil && ctx.Err() == nil {
			logx.Warnf("managed queue scheduler: %v", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-a.wake:
		case <-tick.C:
		}
	}
}

func (a *managedApplication) notify() {
	select {
	case a.wake <- struct{}{}:
	default:
	}
}

func (a *managedApplication) Close() error {
	a.cancel()
	<-a.done
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	if err := a.manager.Shutdown(ctx); err != nil {
		return err
	}
	return errors.Join(a.owner.a.Close(), a.owner.registry.Close(), a.store.Close())
}

func (a *managedApplication) call(ctx context.Context, op workerproto.Operation, cmd workerproto.QueueCommand, result any) error {
	raw, err := workerproto.EncodeQueueCommand(op, cmd)
	if err != nil {
		return err
	}
	return a.request(ctx, op, raw, result)
}

func (a *managedApplication) request(ctx context.Context, op workerproto.Operation, raw json.RawMessage, result any) error {
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
	}
	lease, err := a.manager.Acquire(ctx)
	if err != nil {
		return err
	}
	defer lease.Release()
	peer := lease.Peer.(*acquisitionpolicy.CustodySessionPeer)
	reply, err := peer.Session().Call(ctx, op, raw)
	if err != nil {
		return err
	}
	if reply.Error != nil {
		if op == workerproto.QueueGet && reply.Error.Code == "not_found" {
			return monloader.ErrImportJobNotFound
		}
		return fmt.Errorf("managed operation %s: %s", op, reply.Error.Code)
	}
	if result != nil {
		return json.Unmarshal(reply.Payload, result)
	}
	return nil
}

func (a *managedApplication) LocalPTRTags(ctx context.Context, hash string) (workerproto.PTRLocalTags, error) {
	var result workerproto.PTRLocalTags
	if a.owner.live == nil {
		return result, nil
	}
	raw, err := json.Marshal(workerproto.PTRLocalTagsRequest{SHA256: hash})
	if err != nil {
		return result, err
	}
	err = a.request(ctx, workerproto.PTRTags, raw, &result)
	return result, err
}

func (a *managedApplication) ImportQueue(ctx context.Context, page, limit int) (monloader.ImportQueuePage, error) {
	result := monloader.ImportQueuePage{Page: page, Limit: limit}
	err := a.call(ctx, workerproto.QueueList, workerproto.QueueCommand{Page: page, Limit: limit}, &result)
	return result, err
}

func (a *managedApplication) ImportResults(ctx context.Context, id int64) (monloader.ImportJobResults, error) {
	var result monloader.ImportJobResults
	err := a.call(ctx, workerproto.QueueGet, workerproto.QueueCommand{JobID: id}, &result)
	return result, err
}

func (a *managedApplication) SubmitImport(ctx context.Context, source, gallery string, limit int) (int64, error) {
	setup := a.owner.setup
	if limit == 0 && a.owner.live != nil {
		limit = a.owner.live.MaxItems
	} else if limit == 0 {
		limit = setup.Count
	}
	match := false
	for id := 1; id <= setup.Windows; id++ {
		plan := setup.MetadataFor(int64(id))
		match = match || source == plan.URL && gallery == plan.Gallery && limit == setup.Count
	}
	if a.owner.live != nil {
		match = workerproto.StrictQueueSource(source) != "" && limit > 0 && limit <= a.owner.live.MaxItems
	}
	if !match {
		return 0, web.ErrManagedImportUnsupported
	}
	identity := a.owner.galleryIdentity(gallery)
	if identity == "" {
		return 0, web.ErrManagedImportUnsupported
	}
	intent, err := a.owner.registry.NewSubmission(ctx, workerproto.QueueCommand{URL: source, Gallery: gallery, MaxItems: limit}, identity)
	if err != nil {
		return 0, err
	}
	var result struct {
		JobID int64 `json:"job_id"`
	}
	err = a.call(ctx, workerproto.QueueEnqueue, intent.Command, &result)
	if err == nil {
		a.notify()
	}
	return result.JobID, err
}

func (a *managedApplication) Cancel(ctx context.Context, id int64) error {
	return a.call(ctx, workerproto.QueueCancel, workerproto.QueueCommand{JobID: id}, nil)
}

func (a *managedApplication) CanRetry() bool { return a.owner.live != nil }

func (a *managedApplication) AcquisitionSettings() web.AcquisitionSettings {
	if a.owner.live == nil {
		return web.AcquisitionSettings{}
	}
	return web.AcquisitionSettings{Runtime: a.owner.live.Runtime, PolicyFile: a.owner.live.PolicyConfig, MaxItems: a.owner.live.MaxItems}
}

func (a *managedApplication) RetryImport(ctx context.Context, id int64) (int64, error) {
	j, err := a.ImportResults(ctx, id)
	if err != nil {
		return 0, err
	}
	switch j.Status {
	case "failed", "partial", "interrupted", "canceled":
		// Reuse the original URL, destination and bound. A new intent
		// re-evaluates policy and leaves the previous attempt in history.
		return a.SubmitImport(ctx, j.URL, j.Gallery, j.MaxItems)
	default:
		return 0, web.ErrManagedImportUnsupported
	}
}

func (a *managedApplication) Offline() bool { return a.manager.ConfiguredOffline() }

func (a *managedApplication) SetOffline(ctx context.Context, offline bool) error {
	a.policyMu.Lock()
	defer a.policyMu.Unlock()
	if !offline && a.manager.EffectiveOnline() {
		return nil
	}
	if !offline {
		// Hold dequeue until the owner has acknowledged the new epoch.
		if err := a.call(ctx, workerproto.QueuePause, workerproto.QueueCommand{}, nil); err != nil {
			return err
		}
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	if err := a.manager.SetOffline(ctx, offline); err != nil {
		return err
	}
	a.notify()
	return nil
}
