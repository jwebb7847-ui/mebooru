package main

import (
	"context"
	"encoding/json"
	"path/filepath"
	"sync"

	"github.com/jwebb7847-ui/mebooru/shared/logx"
	"github.com/jwebb7847-ui/mebooru/shared/workerproto"
	"github.com/monbooru/monloader/internal/config"
	"github.com/monbooru/monloader/internal/ptr"
)

type managedPTR struct {
	mu         sync.Mutex
	configPath string
	lifetime   context.Context
	gate       *workerproto.DrainGate
	engine     *ptr.Engine
}

func (p *managedPTR) stop() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.engine != nil {
		p.engine.Disable()
		p.engine = nil
	}
}

func (p *managedPTR) wrap(next workerproto.Handler) workerproto.Handler {
	return func(ctx context.Context, op workerproto.Operation, body json.RawMessage) (json.RawMessage, *workerproto.Fault) {
		switch op {
		case workerproto.PTRTags:
			return p.tags(ctx, body)
		case workerproto.PTRSync:
			return p.sync(ctx, body)
		case workerproto.ReserveIdle:
			p.mu.Lock()
			defer p.mu.Unlock()
			if p.engine != nil && len(body) == 0 {
				return json.RawMessage(`{"reserved":false}`), nil
			}
		}
		result, fault := next(ctx, op, body)
		if fault == nil {
			switch op {
			case workerproto.SetOffline:
				if v, err := workerproto.DecodeOfflinePolicy(body); err == nil && v.Offline {
					p.stop()
				}
			case workerproto.Shutdown:
				p.stop()
			}
		}
		return result, fault
	}
}

func (p *managedPTR) sync(ctx context.Context, body json.RawMessage) (json.RawMessage, *workerproto.Fault) {
	req, err := workerproto.DecodePTRSyncRequest(body)
	if err != nil {
		return nil, &workerproto.Fault{Code: "invalid"}
	}
	ok := json.RawMessage(`{"ok":true}`)
	if !req.Run {
		p.stop()
		return ok, nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	online, finish, err := p.gate.Admit(ctx)
	if err != nil {
		return nil, &workerproto.Fault{Code: "offline"}
	}
	defer finish(nil)
	if online.Err() != nil {
		return nil, &workerproto.Fault{Code: "offline"}
	}
	if p.engine != nil {
		p.engine.Resume()
		return ok, nil
	}
	cfg, err := config.LoadFromFileIn(p.configPath, filepath.Dir(p.configPath))
	if err != nil || !cfg.PTR.Enabled {
		return nil, &workerproto.Fault{Code: "ptr_index_unavailable"}
	}
	cfg.PTR.Paused = false
	engine, err := ptr.NewManagedEngine(cfg.PTR, p.gate)
	if err == nil {
		err = engine.StartManagedSync(p.lifetime)
	}
	if err != nil {
		logx.Warnf("managed PTR sync: %v", err)
		return nil, &workerproto.Fault{Code: "ptr_sync_unavailable"}
	}
	p.engine = engine
	return ok, nil
}

func (p *managedPTR) tags(ctx context.Context, raw json.RawMessage) (json.RawMessage, *workerproto.Fault) {
	req, err := workerproto.DecodePTRLocalTagsRequest(raw)
	if err != nil {
		return nil, &workerproto.Fault{Code: "invalid"}
	}
	cfg, err := config.LoadFromFileIn(p.configPath, filepath.Dir(p.configPath))
	if err != nil {
		return nil, &workerproto.Fault{Code: "ptr_index_unavailable"}
	}
	result, err := ptr.ReadLocalTags(ctx, cfg.PTR, req.SHA256)
	if err != nil {
		return nil, &workerproto.Fault{Code: "ptr_index_unavailable"}
	}
	p.mu.Lock()
	if p.engine != nil {
		result.SyncState = string(p.engine.Status().State)
	}
	p.mu.Unlock()
	body, err := json.Marshal(result)
	if err != nil || len(body) > 256<<10 {
		return nil, &workerproto.Fault{Code: "ptr_lookup_too_large"}
	}
	return body, nil
}
