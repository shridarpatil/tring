package callcdr

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/tring/internal/media/record"
)

// Option configures recording finalization before any workers start.
type Option func(workers *int)

// WithRecordingWorkers bounds concurrent finalizations, including uploads.
// One is the default: simultaneous hangups must not start unbounded encoders.
func WithRecordingWorkers(n int) Option {
	return func(workers *int) {
		if n > 0 {
			*workers = n
		}
	}
}

// recordingJob contains everything needed after the live room is gone. The
// manifest and raw captures share the persistent recording volume. Stage flags
// prevent re-encoding on upload retries and make cleanup restartable.
type recordingJob struct {
	Version      int                 `json:"version"`
	CallID       uuid.UUID           `json:"call_id"`
	LegID        string              `json:"leg_id"`
	Tenant       uuid.UUID           `json:"tenant_id"`
	StartedAt    time.Time           `json:"started_at"`
	QueuedAt     time.Time           `json:"queued_at"`
	Rate         int                 `json:"rate"`
	Format       string              `json:"format"`
	Duration     time.Duration       `json:"duration"`
	Audio        []record.Track      `json:"audio"`
	Video        []record.VideoTrack `json:"video"`
	Rendered     bool                `json:"rendered"`
	Published    bool                `json:"published"`
	RemoveOutput bool                `json:"remove_output"`
}

type recordingQueue struct {
	dir     string
	limit   int
	log     *slog.Logger
	run     func(context.Context, *recordingJob) error
	ctx     context.Context
	cancel  context.CancelFunc
	mu      sync.Mutex
	pending []*recordingJob
	known   map[string]bool
	workers int
	idle    chan struct{}
	closing bool
}

func newRecordingQueue(dir string, limit int, run func(context.Context, *recordingJob) error, logger *slog.Logger) *recordingQueue {
	if limit < 1 {
		limit = 1
	}
	ctx, cancel := context.WithCancel(context.Background())
	idle := make(chan struct{})
	close(idle)
	return &recordingQueue{dir: filepath.Join(dir, ".pending-recordings"), limit: limit,
		log: logger, run: run, ctx: ctx, cancel: cancel, known: make(map[string]bool), idle: idle}
}

func (q *recordingQueue) path(j *recordingJob) string {
	return filepath.Join(q.dir, j.LegID+".json")
}

// Save each stage atomically, including a directory sync for restart recovery.
func (q *recordingQueue) save(j *recordingJob) error {
	if err := os.MkdirAll(q.dir, 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(q.dir, ".job-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	err = json.NewEncoder(f).Encode(j)
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err = os.Rename(f.Name(), q.path(j)); err != nil {
		return err
	}
	d, err := os.Open(q.dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

func (q *recordingQueue) enqueue(j *recordingJob) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.known[j.LegID] {
		return nil
	}
	if err := q.save(j); err != nil {
		return err
	}
	q.addLocked(j)
	return nil
}

func (q *recordingQueue) addLocked(j *recordingJob) {
	if q.closing || q.known[j.LegID] {
		return
	}
	q.known[j.LegID] = true
	q.pending = append(q.pending, j)
	q.log.Info("cdr: recording queued", "call", j.CallID, "pending", len(q.pending), "workers", q.limit)
	if q.workers < q.limit {
		if q.workers == 0 {
			q.idle = make(chan struct{})
		}
		q.workers++
		go q.work()
	}
}

func (q *recordingQueue) work() {
	for {
		q.mu.Lock()
		if len(q.pending) == 0 || q.ctx.Err() != nil {
			q.workers--
			if q.workers == 0 {
				close(q.idle)
			}
			q.mu.Unlock()
			return
		}
		j := q.pending[0]
		q.pending[0] = nil
		q.pending = q.pending[1:]
		q.mu.Unlock()
		start := time.Now()
		q.log.Info("cdr: recording processing", "call", j.CallID, "wait", start.Sub(j.QueuedAt))
		err := q.run(q.ctx, j)
		if err == nil {
			err = os.Remove(q.path(j))
		}
		if err != nil {
			// Do not spin on bad media or a failing store. Leave the durable job
			// and captures for the next gateway start to retry.
			q.log.Error("cdr: recording job retained for restart recovery", "call", j.CallID, "err", err)
		} else {
			q.log.Info("cdr: recording finalized", "call", j.CallID, "elapsed", time.Since(start))
		}
		q.mu.Lock()
		delete(q.known, j.LegID)
		q.mu.Unlock()
	}
}

// ResumeRecordings retries jobs left by a stopped gateway. Call once on startup,
// after dependencies are ready. Each gateway must own its recording spool;
// sharing it with the read-only recording API is fine.
func (l *Log) ResumeRecordings() { l.recordings.resume() }

func (q *recordingQueue) resume() {
	entries, err := os.ReadDir(q.dir)
	if errors.Is(err, os.ErrNotExist) {
		return
	}
	if err != nil {
		q.log.Error("cdr: read pending recordings", "err", err)
		return
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		var j recordingJob
		b, err := os.ReadFile(filepath.Join(q.dir, entry.Name()))
		if err == nil {
			err = json.Unmarshal(b, &j)
		}
		if err == nil {
			err = q.validate(&j, entry.Name())
		}
		if err != nil {
			q.log.Error("cdr: invalid pending recording; retained", "file", entry.Name(), "err", err)
			continue
		}
		q.addLocked(&j)
	}
}

func (q *recordingQueue) validate(j *recordingJob, name string) error {
	id, err := uuid.Parse(j.LegID)
	if err != nil || id.String() != j.LegID || name != j.LegID+".json" || j.Version != 1 || j.Tenant == uuid.Nil {
		return errors.New("invalid recording identity/version")
	}
	if j.Format != "ogg" && j.Format != "mp3" && j.Format != "wav" {
		return errors.New("invalid recording format")
	}
	root, err := filepath.Abs(filepath.Dir(q.dir))
	if err != nil {
		return err
	}
	var paths []string
	for _, t := range j.Audio {
		paths = append(paths, t.Path)
	}
	for _, t := range j.Video {
		paths = append(paths, t.Path)
	}
	for _, p := range paths {
		abs, err := filepath.Abs(p)
		if err != nil || filepath.Dir(abs) != root || !strings.HasPrefix(filepath.Base(abs), j.LegID+"-") {
			return fmt.Errorf("capture outside call spool: %q", p)
		}
	}
	return nil
}

// CloseRecordings drains queued work until ctx expires, then cancels active
// encoders/uploads. Unfinished jobs remain on disk. Call after call admission
// and live rooms have stopped, before closing the database.
func (l *Log) CloseRecordings(ctx context.Context) error { return l.recordings.close(ctx) }

func (q *recordingQueue) close(ctx context.Context) error {
	q.mu.Lock()
	q.closing = true
	idle := q.idle
	q.mu.Unlock()
	defer q.cancel()
	select {
	case <-idle:
		return nil
	case <-ctx.Done():
		q.cancel()
		<-idle // command/DB contexts are canceled before dependencies close
		return ctx.Err()
	}
}

func (l *Log) queueRecording(ac *activeCall) {
	j := &recordingJob{Version: 1, CallID: ac.id, LegID: ac.legID, Tenant: ac.tenant,
		StartedAt: ac.startedAt, QueuedAt: time.Now(), Rate: ac.recRate, Format: ac.recFormat,
		Audio: ac.rec.Tracks(), Video: ac.rec.VideoTracks(), Duration: ac.rec.Duration()}
	if len(j.Audio) == 0 && len(j.Video) == 0 {
		return // unanswered/no-media calls need no job or filesystem work
	}
	if err := l.recordings.enqueue(j); err != nil {
		l.log.Error("cdr: cannot persist recording job; captures retained", "call", ac.id, "err", err)
	}
}
