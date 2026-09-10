package hub

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"os"
	"strconv"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/philband/callboard/internal/api"
)

// ErrAlreadyRunning means another hub holds the lock file. The CLI treats it
// as success: the other hub serves the socket.
var ErrAlreadyRunning = errors.New("hub already running")

// maxWait caps the long-poll duration a client may ask for.
const maxWait = 10 * time.Minute

// served describes the running listener; Handler reports it in /v1/health.
type served struct {
	socket  string
	version string
}

var serving atomic.Pointer[served]

// Handler routes the v1 API onto a hub.
func Handler(h *Hub) http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /v1/health", func(w http.ResponseWriter, r *http.Request) {
		res := api.HealthResponse{OK: true, Version: h.version, PID: os.Getpid()}
		if s := serving.Load(); s != nil {
			res.Socket = s.socket
			if s.version != "" {
				res.Version = s.version
			}
		}
		writeJSON(w, http.StatusOK, res)
	})

	mux.HandleFunc("POST /v1/checkin", func(w http.ResponseWriter, r *http.Request) {
		var req api.CheckinRequest
		if !decode(w, r, &req) {
			return
		}
		res, err := h.Checkin(req)
		if err != nil {
			writeErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, res)
	})

	mux.HandleFunc("POST /v1/checkout", func(w http.ResponseWriter, r *http.Request) {
		var req api.CheckoutRequest
		if !decode(w, r, &req) {
			return
		}
		if err := h.Checkout(req.As); err != nil {
			writeErr(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})

	mux.HandleFunc("POST /v1/resolve", func(w http.ResponseWriter, r *http.Request) {
		var req api.ResolveRequest
		if !decode(w, r, &req) {
			return
		}
		s, err := h.Resolve(req)
		if err != nil {
			writeErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, api.ResolveResponse{Session: s})
	})

	mux.HandleFunc("GET /v1/sessions", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, api.SessionsResponse{Sessions: h.Sessions(r.URL.Query().Get("scope"))})
	})

	mux.HandleFunc("POST /v1/messages", func(w http.ResponseWriter, r *http.Request) {
		var req api.SendRequest
		if !decode(w, r, &req) {
			return
		}
		res, err := h.Send(req)
		if err != nil {
			writeErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, res)
	})

	mux.HandleFunc("GET /v1/inbox", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		wait, err := waitParam(q.Get("wait"))
		if err != nil {
			writeErr(w, err)
			return
		}
		peek, _ := strconv.ParseBool(q.Get("peek"))
		as := q.Get("as")
		// Always peek here: messages are marked delivered only after the
		// response reached the client, so a caller killed mid-wait (tool
		// timeout, Ctrl-C) gets them again next time.
		msgs, err := h.Inbox(r.Context(), as, wait, true)
		if err != nil {
			writeErr(w, err)
			return
		}
		if err := writeJSONFlush(w, http.StatusOK, api.InboxResponse{Messages: msgs}); err != nil || peek || len(msgs) == 0 {
			return
		}
		ids := make([]string, len(msgs))
		for i, m := range msgs {
			ids[i] = m.ID
		}
		_ = h.MarkDelivered(as, ids)
	})

	mux.HandleFunc("GET /v1/history", func(w http.ResponseWriter, r *http.Request) {
		msgs, err := h.History(r.URL.Query().Get("as"))
		if err != nil {
			writeErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, api.InboxResponse{Messages: msgs})
	})

	mux.HandleFunc("POST /v1/jobs", func(w http.ResponseWriter, r *http.Request) {
		var req api.PostJobRequest
		if !decode(w, r, &req) {
			return
		}
		j, err := h.PostJob(req)
		if err != nil {
			writeErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, api.JobResponse{Job: j})
	})

	mux.HandleFunc("GET /v1/jobs", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		writeJSON(w, http.StatusOK, api.JobsResponse{Jobs: h.Jobs(q.Get("scope"), q.Get("status"))})
	})

	mux.HandleFunc("GET /v1/jobs/{id}", func(w http.ResponseWriter, r *http.Request) {
		j, err := h.Job(r.PathValue("id"))
		if err != nil {
			writeErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, api.JobResponse{Job: j})
	})

	mux.HandleFunc("POST /v1/jobs/{id}/claim", func(w http.ResponseWriter, r *http.Request) {
		var req api.JobActionRequest
		if !decode(w, r, &req) {
			return
		}
		j, err := h.ClaimJob(r.PathValue("id"), req.As)
		if err != nil {
			writeErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, api.JobResponse{Job: j})
	})

	mux.HandleFunc("POST /v1/jobs/{id}/done", func(w http.ResponseWriter, r *http.Request) {
		closeJob(w, r, h, api.JobDone)
	})

	mux.HandleFunc("POST /v1/jobs/{id}/fail", func(w http.ResponseWriter, r *http.Request) {
		closeJob(w, r, h, api.JobFailed)
	})

	mux.HandleFunc("GET /v1/events", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		wait, err := waitParam(q.Get("wait"))
		if err != nil {
			writeErr(w, err)
			return
		}
		since, err := sinceParam(q.Get("since"))
		if err != nil {
			writeErr(w, err)
			return
		}
		res, err := h.Events(r.Context(), since, wait, q.Get("scope"))
		if err != nil {
			writeErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, res)
	})

	return auth(mux)
}

// auth is the authentication boundary. On the Unix socket it is a no-op: the
// socket's filesystem permissions are the boundary. A TCP listener would
// check bearer tokens here.
func auth(next http.Handler) http.Handler { return next }

func closeJob(w http.ResponseWriter, r *http.Request, h *Hub, status string) {
	var req api.JobActionRequest
	if !decode(w, r, &req) {
		return
	}
	j, err := h.CloseJob(r.PathValue("id"), req.As, status, req.Result)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, api.JobResponse{Job: j})
}

// waitParam parses a Go duration, capped at maxWait. Empty means no wait.
func waitParam(s string) (time.Duration, error) {
	if s == "" {
		return 0, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, badRequest("bad wait %q: want a Go duration such as 540s", s)
	}
	if d < 0 {
		return 0, nil
	}
	return min(d, maxWait), nil
}

func sinceParam(s string) (uint64, error) {
	if s == "" {
		return 0, nil
	}
	n, err := strconv.ParseUint(s, 10, 64)
	if err != nil {
		return 0, badRequest("bad since %q: want an event sequence number", s)
	}
	return n, nil
}

func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		writeErr(w, badRequest("invalid JSON body: %v", err))
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

// writeJSONFlush is writeJSON that also pushes the bytes to the connection
// and reports whether that succeeded.
func writeJSONFlush(w http.ResponseWriter, code int, v any) error {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		return err
	}
	return http.NewResponseController(w).Flush()
}

func writeErr(w http.ResponseWriter, err error) {
	code := http.StatusInternalServerError
	var e *Error
	if errors.As(err, &e) {
		code = e.Code
	}
	writeJSON(w, code, api.ErrorResponse{Error: err.Error()})
}

// Serve runs the hub on a Unix socket until ctx is done. It holds an
// exclusive flock on the lock file so two auto-spawns cannot race; the loser
// gets ErrAlreadyRunning.
func Serve(ctx context.Context, h *Hub, socket, lock, version string) error {
	lf, err := os.OpenFile(lock, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return err
	}
	defer lf.Close()
	if err := syscall.Flock(int(lf.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return ErrAlreadyRunning
	}
	defer syscall.Flock(int(lf.Fd()), syscall.LOCK_UN)

	if err := os.Remove(socket); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	ln, err := net.Listen("unix", socket)
	if err != nil {
		return err
	}
	if err := os.Chmod(socket, 0o600); err != nil {
		ln.Close()
		return err
	}
	defer os.Remove(socket)

	serving.Store(&served{socket: socket, version: version})
	srv := &http.Server{
		Handler:           Handler(h),
		ReadHeaderTimeout: 10 * time.Second,
		// No WriteTimeout or IdleTimeout: long-polls hold connections open.
	}
	errc := make(chan error, 1)
	go func() { errc <- srv.Serve(ln) }()

	select {
	case err := <-errc:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
	}
	stop, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := srv.Shutdown(stop); err != nil {
		srv.Close() // long-polls still in flight
	}
	return nil
}
