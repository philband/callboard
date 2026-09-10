package hub

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"

	"github.com/philband/callboard/internal/api"
)

// maxLine bounds one journal line; a longer one is skipped as unreadable.
const maxLine = 8 << 20

// journal is the append-only JSONL event log.
type journal struct {
	f *os.File
	w *bufio.Writer
}

// openJournal opens the journal at path, creating its directory and the file.
func openJournal(path string) (*journal, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return nil, err
	}
	return &journal{f: f, w: bufio.NewWriter(f)}, nil
}

// replay applies every event written so far, in order. Lines that do not
// parse - including a truncated last line from a killed hub - are skipped and
// counted.
func (j *journal) replay(apply func(api.Event)) (n, skipped int, err error) {
	if _, err := j.f.Seek(0, io.SeekStart); err != nil {
		return 0, 0, err
	}
	sc := bufio.NewScanner(j.f)
	sc.Buffer(make([]byte, 0, 64<<10), maxLine)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		var ev api.Event
		if err := json.Unmarshal(line, &ev); err != nil {
			skipped++
			continue
		}
		apply(ev)
		n++
	}
	if err := sc.Err(); err != nil {
		return n, skipped, err
	}
	return n, skipped, nil
}

// append writes one event as a JSON line and flushes it to the OS.
func (j *journal) append(ev api.Event) error {
	b, err := json.Marshal(ev)
	if err != nil {
		return err
	}
	if _, err := j.w.Write(append(b, '\n')); err != nil {
		return err
	}
	return j.w.Flush()
}

func (j *journal) close() error {
	if err := j.w.Flush(); err != nil {
		j.f.Close()
		return err
	}
	return j.f.Close()
}
