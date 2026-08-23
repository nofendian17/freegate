package application

import (
	"bytes"
	"io"
	"log/slog"
	"strings"
)

// WithRawUpstreamLog enables printing every upstream response line to
// stdout via slog, for diagnosing degenerate upstream behavior (e.g.
// muse-spark's EOF-truncated streams) straight from the server log. It is
// off by default; enable with UPSTREAM_CAPTURE=true.
func (s *ChatService) WithRawUpstreamLog(enabled bool) *ChatService {
	s.rawUpstreamLog = enabled
	return s
}

// rawLineLogger tees an upstream body into slog, one complete SSE line per
// log record, so the output stays readable while remaining byte-faithful.
// A final line without a trailing newline (truncated upstream streams) is
// flushed on Close.
type rawLineLogger struct {
	rc        io.ReadCloser
	requestID string
	model     string
	buf       []byte // unconsumed bytes; complete lines are drained on Read
}

func newRawLineLogger(rc io.ReadCloser, model, requestID string) *rawLineLogger {
	return &rawLineLogger{rc: rc, model: model, requestID: requestID}
}

func (l *rawLineLogger) Read(p []byte) (int, error) {
	n, err := l.rc.Read(p)
	if n > 0 {
		l.buf = append(l.buf, p[:n]...)
		for {
			i := bytes.IndexByte(l.buf, '\n')
			if i < 0 {
				break
			}
			l.emit(string(l.buf[:i]))
			l.buf = l.buf[i+1:]
		}
	}
	return n, err
}

// Close flushes any buffered partial tail and closes the upstream body.
func (l *rawLineLogger) Close() error {
	if len(l.buf) > 0 {
		l.emit(string(l.buf))
		l.buf = nil
	}
	return l.rc.Close()
}

func (l *rawLineLogger) emit(line string) {
	line = strings.TrimRight(line, "\r")
	if line == "" {
		return
	}
	slog.Info("upstream raw response",
		"request_id", l.requestID,
		"model", l.model,
		"line", line,
	)
}
