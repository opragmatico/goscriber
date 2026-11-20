package worker

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"sync"
	"time"

	"github.com/opragmatico/goscriber/engine"
)

// Serve is the worker side of the protocol: it reads requests from r,
// carries them out on eng and writes the responses to w, until r ends,
// which means the supervisor closed the pipe or went away. It then unloads
// the model and returns nil; a broken frame returns an error.
//
// Each request runs on its own goroutine, so a ping is answered while a
// decode runs and requests may overlap as the engine allows. A stream's
// requests arrive one at a time, since the supervisor waits for each
// answer, so they stay in order. Requests carry no context: NeMo-Speech.cpp
// cannot stop a decode once it has started, so the supervisor stops a
// worker by ending the process instead.
func Serve(r io.Reader, w io.Writer, eng engine.Engine, log *slog.Logger) error {
	if log == nil {
		log = slog.Default()
	}
	s := &server{eng: eng, w: w, log: log, streams: map[uint64]engine.Stream{}}
	err := s.loop(r)
	s.shutdown()
	return err
}

type server struct {
	eng engine.Engine
	log *slog.Logger

	wmu sync.Mutex // serialises frames written to w
	w   io.Writer

	mu         sync.Mutex // guards streams and lastStream
	streams    map[uint64]engine.Stream
	lastStream uint64

	inflight sync.WaitGroup
}

func (s *server) loop(r io.Reader) error {
	for {
		var req request
		samples, err := readFrame(r, &req)
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("reading a request: %w", err)
		}
		switch req.Op {
		case opPing, opHello:
			s.reply(s.handle(req, samples))
		default:
			s.inflight.Go(func() { s.reply(s.handle(req, samples)) })
		}
	}
}

// shutdown closes the open streams and unloads the model, giving up after
// a moment: a call stuck in the engine must not keep the worker alive once
// its supervisor has gone.
func (s *server) shutdown() {
	done := make(chan struct{})
	go func() {
		s.inflight.Wait()
		s.mu.Lock()
		streams := s.streams
		s.streams = map[uint64]engine.Stream{}
		s.mu.Unlock()
		for _, st := range streams {
			st.Close()
		}
		if err := s.eng.Unload(); err != nil {
			s.log.Warn("unloading the model failed", "err", err)
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(shutdownGrace):
		s.log.Warn("the worker is exiting with an engine call still running")
	}
}

func (s *server) reply(resp response) {
	if resp.Result != nil {
		finite(resp.Result)
	}
	s.wmu.Lock()
	defer s.wmu.Unlock()
	if err := writeFrame(s.w, resp, nil); err != nil {
		// The supervisor is gone or the pipe is broken; the read loop ends
		// soon after.
		s.log.Debug("writing a response failed", "id", resp.ID, "err", err)
	}
}

func (s *server) handle(req request, samples []float32) response {
	resp := response{ID: req.ID}
	ctx := context.Background()
	var err error
	switch req.Op {
	case opHello:
		resp.Version, resp.PID = protocolVersion, os.Getpid()
	case opPing:
	case opLoad:
		if req.Spec == nil {
			err = &engine.Error{Op: "load", Kind: engine.ErrInvalidArgument, Detail: "no model given"}
			break
		}
		var info engine.ModelInfo
		info, err = s.eng.Load(ctx, *req.Spec)
		if err == nil {
			resp.Info = &info
		}
	case opUnload:
		err = s.eng.Unload()
	case opRecognize:
		var res engine.Result
		res, err = s.eng.Recognize(ctx, samples, req.Rate, options(req))
		if err == nil {
			resp.Result = &res
		}
	case opStreamStart:
		var st engine.Stream
		st, err = s.eng.StartStream(ctx, options(req))
		if err == nil {
			s.mu.Lock()
			s.lastStream++
			resp.Stream = s.lastStream
			s.streams[resp.Stream] = st
			s.mu.Unlock()
		}
	case opStreamPush, opStreamNext, opStreamEnd, opStreamFinish, opStreamClose:
		err = s.stream(req, samples, &resp)
	default:
		err = &engine.Error{Op: string(req.Op), Kind: engine.ErrInvalidArgument, Detail: "unknown request"}
	}
	resp.Err = toWire(req.Op, err)
	return resp
}

// stream carries out a request on an open stream.
func (s *server) stream(req request, samples []float32, resp *response) error {
	s.mu.Lock()
	st, ok := s.streams[req.Stream]
	if ok && req.Op == opStreamClose {
		delete(s.streams, req.Stream)
	}
	s.mu.Unlock()
	if !ok {
		if req.Op == opStreamClose {
			return nil
		}
		return &engine.Error{Op: string(req.Op), Kind: engine.ErrInvalidArgument, Detail: "the stream is closed"}
	}
	switch req.Op {
	case opStreamPush:
		return st.Push(samples, req.Rate)
	case opStreamNext:
		r, err := st.Next()
		switch {
		case errors.Is(err, io.EOF):
			resp.EOF = true
			return nil
		case err != nil:
			return err
		}
		resp.Result = r
		return nil
	case opStreamEnd:
		return st.ForceEndpoint()
	case opStreamFinish:
		return st.Finish()
	default: // opStreamClose
		return st.Close()
	}
}

func options(req request) engine.Options {
	if req.Options == nil {
		return engine.Options{}
	}
	return *req.Options
}
