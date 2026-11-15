package worker

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"sync"
	"time"
)

// proc is one worker process, as the supervisor sees it.
type proc struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	log    *slog.Logger
	cpu    bool // the model it loads runs on the CPU
	stderr *tail

	wmu sync.Mutex // serialises frames written to stdin
	bw  *bufio.Writer

	mu      sync.Mutex // guards what follows
	pid     int
	nextID  uint64
	pending map[uint64]*call
	stop    string // why the supervisor ended the process; "" while it has not
	hung    string // why the supervisor decided it hung, if it did

	// onDeath runs once the process is gone, before the calls still waiting
	// fail, so a retry sees the supervisor already updated. busy is the
	// request one of those calls made, other than a ping, or "".
	onDeath func(p *proc, cause error, busy op)

	dead    chan struct{} // closed once the process has exited and every call failed
	deadErr error         // why it exited; set before dead closes
}

// call is a request waiting for its response.
type call struct {
	op   op
	done chan response
}

// lostError is a worker that crashed, hung or was ended while a call waited
// on it.
type lostError struct {
	op      op
	cause   error  // the process's exit, such as "signal: killed"
	hung    string // why the supervisor ended it as hung, if it did
	stopped string // why the supervisor ended it on purpose, if it did
	cpu     bool   // it ran the model on the CPU
}

func (e *lostError) Error() string {
	switch {
	case e.stopped != "":
		return fmt.Sprintf("the engine worker was stopped (%s)", e.stopped)
	case e.hung != "":
		return fmt.Sprintf("the engine worker stopped responding (%s) and was ended", e.hung)
	default:
		return fmt.Sprintf("the engine worker crashed (%v)", e.cause)
	}
}

// start launches cmd as a worker. The caller set its arguments and
// environment; start owns its pipes.
func start(cmd *exec.Cmd, cpu bool, log *slog.Logger, onDeath func(*proc, error, op)) (*proc, error) {
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	p := &proc{
		cmd:     cmd,
		stdin:   stdin,
		bw:      bufio.NewWriterSize(stdin, 64<<10),
		cpu:     cpu,
		stderr:  &tail{max: stderrTail},
		pid:     cmd.Process.Pid,
		pending: map[uint64]*call{},
		onDeath: onDeath,
		dead:    make(chan struct{}),
	}
	p.log = log.With("worker", p.pid)
	logged := make(chan struct{})
	go func() {
		defer close(logged)
		forwardLog(stderr, p.log, p.stderr)
	}()
	go p.read(stdout, logged)
	return p, nil
}

// read dispatches the worker's responses until its stdout ends, then reaps
// the process and fails the calls still waiting.
func (p *proc) read(stdout io.Reader, logged <-chan struct{}) {
	var readErr error
	for {
		var resp response
		_, err := readFrame(stdout, &resp)
		if err != nil {
			if !errors.Is(err, io.EOF) {
				readErr = err
			}
			break
		}
		p.mu.Lock()
		c := p.pending[resp.ID]
		delete(p.pending, resp.ID)
		p.mu.Unlock()
		if c != nil {
			c.done <- resp
		}
	}
	if readErr != nil {
		// A broken frame leaves the pipe unusable: end the worker.
		p.log.Error("the engine worker sent a broken response", "err", readErr)
		p.kill("")
	}
	<-logged // Wait closes the pipes, so the log must be read first
	err := p.cmd.Wait()
	if err == nil {
		err = errors.New("exited")
	}

	p.mu.Lock()
	var busy op
	for _, c := range p.pending {
		if c.op != opPing && (busy == "" || busy == opHello) {
			busy = c.op
		}
	}
	p.mu.Unlock()
	if p.onDeath != nil {
		p.onDeath(p, err, busy)
	}
	p.mu.Lock()
	p.deadErr = err
	p.pending = nil // the calls still waiting select on dead
	p.mu.Unlock()
	close(p.dead)
}

// do sends a request with samples and waits for its response, at most
// timeout. A worker that does not answer in time is ended as hung. When ctx
// is done first, do gives the worker cancelGrace to answer, then ends it,
// since a decode cannot be stopped; it returns ctx's error either way.
// Errors other than ctx's are *lostError.
func (p *proc) do(ctx context.Context, req request, samples []float32, timeout, cancelGrace time.Duration) (response, error) {
	c := &call{op: req.Op, done: make(chan response, 1)}
	p.mu.Lock()
	if p.pending == nil {
		p.mu.Unlock()
		<-p.dead
		return response{}, p.lost(req.Op)
	}
	p.nextID++
	req.ID = p.nextID
	p.pending[req.ID] = c
	p.mu.Unlock()

	if err := p.send(req, samples); err != nil {
		p.mu.Lock()
		if p.pending != nil {
			delete(p.pending, req.ID)
		}
		p.mu.Unlock()
		if errors.Is(err, errFrame) {
			return response{}, err
		}
		// The pipe broke: the process is going or gone.
		p.kill("")
		<-p.dead
		return response{}, p.lost(req.Op)
	}

	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case resp := <-c.done:
		return resp, nil
	case <-p.dead:
		return response{}, p.lost(req.Op)
	case <-timer.C:
		p.markHung(fmt.Sprintf("no answer to %s within %v", req.Op, timeout))
		<-p.dead
		return response{}, p.lost(req.Op)
	case <-ctx.Done():
	}

	grace := time.NewTimer(cancelGrace)
	defer grace.Stop()
	select {
	case <-c.done:
	case <-p.dead:
	case <-grace.C:
		p.kill(fmt.Sprintf("%s cancelled", req.Op))
		<-p.dead
	}
	return response{}, ctx.Err()
}

func (p *proc) send(req request, samples []float32) error {
	p.wmu.Lock()
	defer p.wmu.Unlock()
	if err := writeFrame(p.bw, req, samples); err != nil {
		return err
	}
	return p.bw.Flush()
}

// lost is the error for a call on op after the process died.
func (p *proc) lost(o op) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return &lostError{op: o, cause: p.deadErr, hung: p.hung, stopped: p.stop, cpu: p.cpu}
}

// markHung ends a worker that stopped answering.
func (p *proc) markHung(why string) {
	p.mu.Lock()
	if p.hung == "" && p.stop == "" {
		p.hung = why
	}
	p.mu.Unlock()
	p.signalKill()
}

// kill ends the process now. A non-empty why marks it as ended on purpose,
// which is not a crash.
func (p *proc) kill(why string) {
	p.mu.Lock()
	if why != "" && p.stop == "" && p.hung == "" {
		p.stop = why
	}
	p.mu.Unlock()
	p.signalKill()
}

func (p *proc) signalKill() {
	if err := p.cmd.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
		p.log.Debug("killing the engine worker failed", "err", err)
	}
}

// shutdown ends the worker on purpose: it closes its stdin, which makes it
// unload its model and exit, and kills it if it has not exited after grace.
func (p *proc) shutdown(why string, grace time.Duration) {
	p.mu.Lock()
	if p.stop == "" && p.hung == "" {
		p.stop = why
	}
	p.mu.Unlock()
	p.wmu.Lock()
	p.stdin.Close()
	p.wmu.Unlock()
	t := time.NewTimer(grace)
	defer t.Stop()
	select {
	case <-p.dead:
	case <-t.C:
		p.log.Warn("the engine worker did not exit; killing it")
		p.signalKill()
		<-p.dead
	}
}

// alive reports whether the process has not been ended or died.
func (p *proc) alive() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.pending != nil && p.stop == "" && p.hung == ""
}
