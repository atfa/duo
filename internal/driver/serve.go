package driver

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

// Handler is what a plugin must implement: the Host Adapter's required surface.
// A handler is served one request at a time from a single goroutine, so an
// implementation never needs its own locking around these calls. It may still
// start background work — an Agent Adapter attaching to Duo's bridge, for
// example — as soon as it learns the launch context from Prepare.
//
// A Handler that starts background work should also implement io.Closer, so the
// work stops when the session ends. An out-of-process plugin would be killed with
// its process anyway, but an in-process one has nothing else to stop it.
type Handler interface {
	Describe() (*Manifest, error)
	Probe() (*ProbeResult, error)
	Prepare(LaunchRequest) (*LaunchPlan, error)
}

// ModelLister is the optional model-catalog method. Implement it only when
// Capabilities.Models is set; otherwise Core never calls it and the plugin
// answers `unsupported` if it somehow does.
//
// It carries no context, matching Handler: a plugin runs one request at a time, and
// the per-call timeout belongs to the transport, not to the handler.
type ModelLister interface {
	Models() (*ModelList, error)
}

// ThinkingProvider is the optional reasoning-effort method, gated on
// Capabilities.Thinking.
type ThinkingProvider interface {
	Thinking() (*ThinkingOptions, error)
}

// Server answers Core's calls over a byte stream. A plugin's main is two lines:
//
//	if err := driver.Serve(os.Stdin, os.Stdout, myPlugin{}); err != nil {
//		log.Fatal(err)
//	}
func Serve(in io.Reader, out io.Writer, h Handler) error {
	reader := bufio.NewReaderSize(in, 64*1024)
	writer := bufio.NewWriter(out)
	for {
		line, err := readLine(reader)
		if err != nil {
			if errors.Is(err, io.EOF) {
				// Core closed the channel: shut down cleanly.
				return nil
			}
			return err
		}
		if strings.TrimSpace(line) == "" {
			continue
		}
		var req Request
		if err := json.Unmarshal([]byte(line), &req); err != nil {
			if writeErr(writer, &Response{Protocol: ProtocolVersion, Error: &RPCError{
				Code:    CodeInvalid,
				Message: fmt.Sprintf("malformed request: %v", err),
			}}) != nil {
				return nil
			}
			continue
		}
		if err := writeErr(writer, handle(h, &req)); err != nil {
			return err
		}
	}
}

func handle(h Handler, req *Request) *Response {
	resp := &Response{Protocol: ProtocolVersion, ID: req.ID}
	if req.Protocol != ProtocolVersion {
		resp.Error = &RPCError{
			Code:    CodeProtocol,
			Message: fmt.Sprintf("request declares protocol version %d; this plugin speaks %d", req.Protocol, ProtocolVersion),
		}
		return resp
	}
	result, err := dispatch(h, req)
	switch {
	case err == nil:
		resp.OK = true
		if result != nil {
			raw, marshalErr := json.Marshal(result)
			if marshalErr != nil {
				resp.OK = false
				resp.Error = &RPCError{Code: CodeInternal, Message: marshalErr.Error()}
				break
			}
			resp.Result = raw
		}
	case errors.Is(err, errNotImplemented):
		resp.Error = &RPCError{Code: CodeUnsupported, Message: err.Error()}
	default:
		var rpcErr *RPCError
		if errors.As(err, &rpcErr) {
			resp.Error = rpcErr
			break
		}
		resp.Error = &RPCError{Code: CodeInternal, Message: err.Error()}
	}
	return resp
}

var errNotImplemented = errors.New("method not implemented by this plugin")

func dispatch(h Handler, req *Request) (any, error) {
	switch req.Method {
	case MethodDescribe:
		return h.Describe()
	case MethodProbe:
		return h.Probe()
	case MethodPrepare:
		var in LaunchRequest
		if len(req.Params) > 0 {
			if err := json.Unmarshal(req.Params, &in); err != nil {
				return nil, &RPCError{Code: CodeInvalid, Message: err.Error()}
			}
		}
		return h.Prepare(in)
	case MethodModels:
		lister, ok := h.(ModelLister)
		if !ok {
			return nil, errNotImplemented
		}
		return lister.Models()
	case MethodThinking:
		provider, ok := h.(ThinkingProvider)
		if !ok {
			return nil, errNotImplemented
		}
		return provider.Thinking()
	case MethodClose:
		return struct{}{}, nil
	default:
		return nil, &RPCError{Code: CodeUnknownMethod, Message: "unknown method " + req.Method}
	}
}

func writeErr(w *bufio.Writer, resp *Response) error {
	raw, err := json.Marshal(resp)
	if err != nil {
		return err
	}
	if _, err := w.Write(append(raw, '\n')); err != nil {
		return err
	}
	return w.Flush()
}

// readLine reads one newline-delimited request, tolerating a line longer than
// the reader's buffer. A plugin must never mis-frame a request: one malformed
// read would desynchronise every call that follows.
func readLine(r *bufio.Reader) (string, error) {
	var buf []byte
	for {
		chunk, isPrefix, err := r.ReadLine()
		if err != nil {
			return "", err
		}
		buf = append(buf, chunk...)
		if !isPrefix {
			return string(buf), nil
		}
	}
}
