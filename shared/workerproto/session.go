package workerproto

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"sync"
	"time"
)

const (
	MaxPendingCalls = 8
	MaxQueuedFrames = 24
	MaxQueuedBytes = 32 << 20
	MaxInboundBytes = 32 << 20
	MaxEventBytes = 64 << 10
	EventBuffer = 8
	cancelGrace = 5 * time.Second
)

var (
	ErrBusy = errors.New("private worker transport busy")
	ErrDirection = errors.New("private worker operation direction refused")
	ErrClosed = errors.New("private worker transport closed")
	ErrCorrelation = errors.New("private worker request correlation failed")
	ErrBackpressure = errors.New("private worker output budget exhausted")
	ErrCancelUnacknowledged = errors.New("private worker cancellation not acknowledged")
)

const ProgressEvent Operation = "worker.progress"

// Handler must honor context cancellation. It runs outside the reader and
// may call back through the session. Production adapters must additionally
// enforce typed validation, policy, consent and job-scoped authorization.
type Handler func(context.Context, Operation, json.RawMessage) (json.RawMessage, *Fault)

type pendingCall struct {
	result chan Envelope
	timer *time.Timer
}
type inboundCall struct {
	cancel context.CancelFunc
	bytes int
}

// Session owns both pipe ends. The reader never waits for a handler or
// network operation. A single writer serializes frames. Admission and
// retained payload budgets are bounded; no media travels here.
type Session struct {
	role Role
	r io.ReadCloser
	w io.WriteCloser
	handler Handler
	ctx context.Context
	cancel context.CancelFunc
	done chan struct{}
	once sync.Once
	err error

	mu sync.Mutex
	nextID uint64
	lastPeerID uint64
	pending map[uint64]*pendingCall
	inbound map[uint64]inboundCall
	inboundBytes int
	queuedBytes int
	out chan []byte
	events chan Envelope
}

// OpenSession takes ownership of both ends even on handshake failure.
// Closing them must unblock concurrent I/O. The parent context bounds the
// handshake and session lifetime; production startup deadlines are separate.
func OpenSession(ctx context.Context, r io.ReadCloser, w io.WriteCloser, role Role, handler Handler) (*Session, error) {
	lifetime, cancel := context.WithCancel(ctx)
	s := &Session{
		role: role, r: r, w: w, handler: handler, ctx: lifetime, cancel: cancel,
		done: make(chan struct{}), pending: make(map[uint64]*pendingCall),
		inbound: make(map[uint64]inboundCall), out: make(chan []byte, MaxQueuedFrames),
		events: make(chan Envelope, EventBuffer),
	}
	go func() {
		<-lifetime.Done()
		s.fail(lifetime.Err())
	}()
	if err := Handshake(r, w, role); err != nil {
		s.fail(err)
		return nil, err
	}
	if err := lifetime.Err(); err != nil {
		s.fail(err)
		return nil, err
	}
	go s.readLoop()
	go s.writeLoop()
	return s, nil
}

func (s *Session) Done() <-chan struct{} { return s.done }

// EOF does not prove a remote mutation did not commit.
func (s *Session) Err() error {
	select {
	case <-s.done:
		return s.err
	default:
		return nil
	}
}

func (s *Session) Close() { s.fail(ErrClosed) }

// Progress is lossy. Consumers must also select Done; the event channel
// is intentionally not closed while the reader might be sending.
func (s *Session) Events() <-chan Envelope { return s.events }

func (s *Session) fail(err error) {
	s.once.Do(func() {
		s.err = err
		s.cancel()
		close(s.done)
		_ = s.r.Close()
		_ = s.w.Close()
		s.mu.Lock()
		for _, call := range s.pending {
			if call.timer != nil {
				call.timer.Stop()
			}
		}
		clear(s.pending)
		for _, call := range s.inbound {
			call.cancel()
		}
		clear(s.inbound)
		s.inboundBytes = 0
		for {
			select {
			case frame := <-s.out:
				s.queuedBytes -= len(frame)
			default:
				s.mu.Unlock()
				return
			}
		}
	})
}

func encodeMessage(e Envelope) ([]byte, error) {
	body, err := json.Marshal(e)
	if err != nil {
		return nil, ErrMessage
	}
	if len(body) > MaxControlBytes {
		return nil, ErrFrameSize
	}
	if _, err := decodeEnvelope(body); err != nil {
		return nil, err
	}
	return body, nil
}

// Caller holds mu; no I/O under the lock.
func (s *Session) enqueueLocked(body []byte) error {
	select {
	case <-s.done:
		return ErrClosed
	default:
	}
	if len(body) > MaxQueuedBytes - s.queuedBytes {
		return ErrBackpressure
	}
	select {
	case s.out <- body:
		s.queuedBytes += len(body)
		return nil
	default:
		return ErrBackpressure
	}
}

func (s *Session) writeLoop() {
	for {
		select {
		case <-s.done:
			return
		case body := <-s.out:
			err := writeFrame(s.w, body)
			s.mu.Lock()
			s.queuedBytes -= len(body)
			s.mu.Unlock()
			if err != nil {
				s.fail(err)
				return
			}
		}
	}
}

// Call returns a terminal envelope, including remote domain faults. A
// canceled call reserves its ID until a response or connection closure.
// If cancellation is not acknowledged, close after a bounded grace period.
func (s *Session) Call(ctx context.Context, op Operation, payload json.RawMessage) (Envelope, error) {
	if !AllowedDirection(s.role, op) {
		return Envelope{}, ErrDirection
	}
	if err := ctx.Err(); err != nil {
		return Envelope{}, err
	}
	s.mu.Lock()
	select {
	case <-s.done:
		s.mu.Unlock()
		return Envelope{}, s.err
	default:
	}
	if len(s.pending) >= MaxPendingCalls || s.nextID == ^uint64(0) {
		s.mu.Unlock()
		return Envelope{}, ErrBusy
	}
	id := s.nextID + 1
	body, err := encodeMessage(Envelope{Kind: Request, ID: id, Op: op, Payload: payload})
	if err != nil {
		s.mu.Unlock()
		return Envelope{}, err
	}
	call := &pendingCall{result: make(chan Envelope, 1)}
	if err := s.enqueueLocked(body); err != nil {
		s.mu.Unlock()
		return Envelope{}, err
	}
	s.nextID = id
	s.pending[id] = call
	s.mu.Unlock()

	select {
	case response := <-call.result:
		return response, nil
	case <-s.done:
		return Envelope{}, s.err
	case <-ctx.Done():
		s.cancelCall(id, call)
		return Envelope{}, ctx.Err()
	}
}

func (s *Session) cancelCall(id uint64, call *pendingCall) {
	body, err := encodeMessage(Envelope{Kind: Cancel, ID: id})
	if err != nil {
		s.fail(err)
		return
	}
	s.mu.Lock()
	if s.pending[id] != call {
		s.mu.Unlock()
		return
	}
	err = s.enqueueLocked(body)
	if err == nil {
		call.timer = time.AfterFunc(cancelGrace, func() {
			s.mu.Lock()
			pending := s.pending[id] == call
			s.mu.Unlock()
			if pending {
				s.fail(ErrCancelUnacknowledged)
			}
		})
	}
	s.mu.Unlock()
	if err != nil {
		s.fail(err)
	}
}

// EmitProgress may return backpressure; callers can discard that event.
func (s *Session) EmitProgress(payload json.RawMessage) error {
	if s.role != Worker {
		return ErrDirection
	}
	body, err := encodeMessage(Envelope{Kind: Event, Op: ProgressEvent, Payload: payload})
	if err != nil {
		return err
	}
	if len(body) > MaxEventBytes {
		return ErrFrameSize
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	// Reserve capacity for responses and cancellation rather than allowing
	// progress to fill the writer queue. Full event delivery is not required.
	if len(s.out) >= EventBuffer || s.queuedBytes >= MaxQueuedBytes/2 {
		return ErrBackpressure
	}
	return s.enqueueLocked(body)
}

func (s *Session) readLoop() {
	for {
		e, err := ReadEnvelope(s.r)
		if err == nil {
			err = s.receive(e)
		}
		if err != nil {
			s.fail(err)
			return
		}
	}
}

func (s *Session) receive(e Envelope) error {
	switch e.Kind {
	case Response:
		s.mu.Lock()
		defer s.mu.Unlock()
		call := s.pending[e.ID]
		if call == nil {
			return ErrCorrelation
		}
		delete(s.pending, e.ID)
		if call.timer != nil {
			call.timer.Stop()
		}
		call.result <- e
	case Cancel:
		s.mu.Lock()
		defer s.mu.Unlock()
		if e.ID > s.lastPeerID {
			return ErrCorrelation
		}
		// A cancel can cross an already completed response in flight.
		if call, ok := s.inbound[e.ID]; ok {
			call.cancel()
		}
	case Event:
		if s.role != Library || e.Op != ProgressEvent || len(e.Payload) > MaxEventBytes {
			return ErrDirection
		}
		select {
		case s.events <- e:
		default:
		}
	case Request:
		return s.receiveRequest(e)
	default:
		return ErrMessage
	}
	return nil
}

func (s *Session) receiveRequest(e Envelope) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	select {
	case <-s.done:
		return ErrClosed
	default:
	}
	if e.ID <= s.lastPeerID {
		return ErrCorrelation
	}
	s.lastPeerID = e.ID
	peer := counterpart(s.role)
	var code string
	if _, ok := RuleFor(e.Op); !ok {
		code = "unsupported"
	} else if !AllowedDirection(peer, e.Op) {
		code = "unauthorized"
	} else if s.handler == nil {
		code = "unsupported"
	} else if len(s.inbound) >= MaxPendingCalls || len(e.Payload) > MaxInboundBytes - s.inboundBytes {
		code = "busy"
	}
	if code != "" {
		body, err := encodeMessage(Envelope{Kind: Response, ID: e.ID, Error: &Fault{Code: code}})
		if err != nil {
			return err
		}
		return s.enqueueLocked(body)
	}
	ctx, cancel := context.WithCancel(s.ctx)
	s.inbound[e.ID] = inboundCall{cancel: cancel, bytes: len(e.Payload)}
	s.inboundBytes += len(e.Payload)
	go s.handle(ctx, e)
	return nil
}

func (s *Session) handle(ctx context.Context, e Envelope) {
	payload, fault := s.handler(ctx, e.Op, e.Payload)
	// Cooperative cancellation cannot rewrite a successful durable mutation
	// into a claim that nothing happened.
	if fault != nil {
		payload = nil
	}
	body, err := encodeMessage(Envelope{Kind: Response, ID: e.ID, Payload: payload, Error: fault})
	if err != nil {
		body, err = encodeMessage(Envelope{Kind: Response, ID: e.ID, Error: &Fault{Code: "internal_error"}})
	}
	s.mu.Lock()
	call, ok := s.inbound[e.ID]
	if !ok {
		s.mu.Unlock()
		return
	}
	delete(s.inbound, e.ID)
	s.inboundBytes -= call.bytes
	call.cancel()
	if err == nil {
		err = s.enqueueLocked(body)
	}
	s.mu.Unlock()
	if err != nil {
		s.fail(err)
	}
}
