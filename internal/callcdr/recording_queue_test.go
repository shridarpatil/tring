package callcdr

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/tring/internal/dbtest"
	"github.com/shridarpatil/tring/internal/media/record"
)

func queueTestJob() *recordingJob {
	return &recordingJob{Version: 1, CallID: uuid.New(), LegID: uuid.NewString(),
		Tenant: uuid.New(), QueuedAt: time.Now(), Rate: 8000, Format: "ogg"}
}

func queueTestLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func waitQueue(t *testing.T, q *recordingQueue) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := q.close(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestRecordingQueueBoundsConcurrentHangups(t *testing.T) {
	for _, limit := range []int{1, 2} {
		t.Run(fmt.Sprint(limit), func(t *testing.T) {
			var running, peak, done atomic.Int32
			gate := make(chan struct{})
			started := make(chan struct{}, 20)
			q := newRecordingQueue(t.TempDir(), limit, func(ctx context.Context, j *recordingJob) error {
				n := running.Add(1)
				for old := peak.Load(); n > old && !peak.CompareAndSwap(old, n); old = peak.Load() {
				}
				started <- struct{}{}
				select {
				case <-gate:
				case <-ctx.Done():
					return ctx.Err()
				}
				running.Add(-1)
				done.Add(1)
				return nil
			}, queueTestLogger())
			defer q.cancel()
			for range 20 {
				if err := q.enqueue(queueTestJob()); err != nil {
					t.Fatal(err)
				}
			}
			for range limit {
				select {
				case <-started:
				case <-time.After(time.Second):
					t.Fatal("workers never started")
				}
			}
			select {
			case <-started:
				t.Fatal("concurrency limit exceeded")
			case <-time.After(30 * time.Millisecond):
			}
			close(gate)
			waitQueue(t, q)
			if done.Load() != 20 || peak.Load() != int32(limit) {
				t.Fatalf("done=%d peak=%d", done.Load(), peak.Load())
			}
			entries, err := os.ReadDir(q.dir)
			if err != nil || len(entries) != 0 {
				t.Fatalf("completed manifests remain: %v %v", entries, err)
			}
		})
	}
}

func TestRecordingQueueShutdownAndRestart(t *testing.T) {
	dir := t.TempDir()
	started := make(chan struct{})
	q := newRecordingQueue(dir, 1, func(ctx context.Context, j *recordingJob) error {
		close(started)
		<-ctx.Done()
		return ctx.Err()
	}, queueTestLogger())
	for range 3 {
		if err := q.enqueue(queueTestJob()); err != nil {
			t.Fatal(err)
		}
	}
	<-started
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := q.close(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("close: %v", err)
	}
	var recovered atomic.Int32
	next := newRecordingQueue(dir, 1, func(_ context.Context, j *recordingJob) error {
		recovered.Add(1)
		return nil
	}, queueTestLogger())
	next.resume()
	waitQueue(t, next)
	if recovered.Load() != 3 {
		t.Fatalf("recovered %d/3 jobs", recovered.Load())
	}
}

func TestRecordingQueueFailureRetainsCheckpointAndSources(t *testing.T) {
	dir := t.TempDir()
	j := queueTestJob()
	source := filepath.Join(dir, j.LegID+"-customer.ogg")
	if err := os.WriteFile(source, []byte("capture"), 0o600); err != nil {
		t.Fatal(err)
	}
	j.Audio = []record.Track{{Role: "customer", Path: source}}
	var q *recordingQueue
	q = newRecordingQueue(dir, 1, func(_ context.Context, j *recordingJob) error {
		j.Rendered = true
		if err := q.save(j); err != nil {
			return err
		}
		return errors.New("upload unavailable")
	}, queueTestLogger())
	if err := q.enqueue(j); err != nil {
		t.Fatal(err)
	}
	waitQueue(t, q)
	if _, err := os.Stat(source); err != nil {
		t.Fatal("source lost", err)
	}
	info, err := os.Stat(q.path(j))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("manifest permissions: %v", info.Mode())
	}
	var recovered atomic.Bool
	next := newRecordingQueue(dir, 1, func(_ context.Context, j *recordingJob) error {
		recovered.Store(j.Rendered && !j.Published && len(j.Audio) == 1 && j.Audio[0].Path == source)
		return nil
	}, queueTestLogger())
	next.resume()
	waitQueue(t, next)
	if !recovered.Load() {
		t.Fatal("lost recording stage/capture metadata on restart")
	}
}

func TestRecordingQueueRejectsForeignCaptureOnRecovery(t *testing.T) {
	q := newRecordingQueue(t.TempDir(), 1, func(context.Context, *recordingJob) error {
		t.Error("invalid job was run")
		return nil
	}, queueTestLogger())
	j := queueTestJob()
	j.Audio = []record.Track{{Path: filepath.Join(t.TempDir(), j.LegID+"-customer.ogg")}}
	if err := q.save(j); err != nil {
		t.Fatal(err)
	}
	q.resume()
	waitQueue(t, q)
	if _, err := os.Stat(q.path(j)); err != nil {
		t.Fatal("invalid job not retained", err)
	}
}

func TestFailedRecordingPreservesBothLegs(t *testing.T) {
	dir := t.TempDir()
	l := New(nil, nil, dir, queueTestLogger(), nil)
	j := queueTestJob()
	for _, role := range []string{"agent", "customer"} {
		p := filepath.Join(dir, j.LegID+"-"+role+".ogg")
		if err := os.WriteFile(p, []byte("broken media"), 0o600); err != nil {
			t.Fatal(err)
		}
		j.Audio = append(j.Audio, record.Track{Role: role, Path: p})
	}
	if err := l.recordings.enqueue(j); err != nil {
		t.Fatal(err)
	}
	waitQueue(t, l.recordings)
	for _, track := range j.Audio {
		if _, err := os.Stat(track.Path); err != nil {
			t.Fatal("capture lost", err)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, j.LegID+".ogg")); !os.IsNotExist(err) {
		t.Fatal("failed merge published a fallback")
	}
	if _, err := os.Stat(l.recordings.path(j)); err != nil {
		t.Fatal("failed merge lost job", err)
	}
}

func TestPublishedRecordingRecoveryOnlyCleansCaptures(t *testing.T) {
	dir := t.TempDir()
	l := New(nil, nil, dir, queueTestLogger(), nil)
	j := queueTestJob()
	j.Rendered, j.Published = true, true
	path := filepath.Join(dir, j.LegID+"-customer.ogg")
	j.Audio = []record.Track{{Role: "customer", Path: path}}
	if err := os.WriteFile(path, []byte("capture"), 0o600); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, j.LegID+".ogg")
	if err := os.WriteFile(out, []byte("finished"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := l.recordings.save(j); err != nil {
		t.Fatal(err)
	}
	l.ResumeRecordings() // nil DB: reaching publication again would panic
	waitQueue(t, l.recordings)
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("capture not cleaned")
	}
	if _, err := os.Stat(out); err != nil {
		t.Fatal("local output lost", err)
	}
	if _, err := os.Stat(l.recordings.path(j)); !os.IsNotExist(err) {
		t.Fatal("completed job remains")
	}
}

func TestRenderedRecordingRecoveryPublishesWithoutEncoding(t *testing.T) {
	fx := dbtest.Setup(t)
	dir := t.TempDir()
	l := New(fx.DB, nil, dir, queueTestLogger(), nil)
	j := queueTestJob()
	j.Tenant = fx.TenantID
	j.StartedAt = time.Now()
	j.Rendered = true
	err := fx.DB.Pool.QueryRow(context.Background(), l.q.CreateCallInbound,
		j.Tenant, "caller", "agent", j.LegID, "", "web", uuid.NewString()).Scan(&j.CallID)
	if err != nil {
		t.Fatal(err)
	}
	// Invalid source media ensures that accidentally re-running FFmpeg fails.
	// An existing rendered checkpoint only needs DB/S3 publication and cleanup.
	source := filepath.Join(dir, j.LegID+"-customer.ogg")
	j.Audio = []record.Track{{Role: "customer", Path: source}}
	if err := os.WriteFile(source, []byte("unusable capture"), 0o600); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, j.LegID+".ogg")
	if err := os.WriteFile(out, []byte("already encoded output"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := l.recordings.save(j); err != nil {
		t.Fatal(err)
	}
	l.ResumeRecordings()
	waitQueue(t, l.recordings)
	var url string
	if err := fx.DB.Pool.QueryRow(context.Background(), "SELECT COALESCE(recording_url, '') FROM calls WHERE id=$1", j.CallID).Scan(&url); err != nil {
		t.Fatal(err)
	}
	if url == "" {
		t.Fatal("recovered recording was not published")
	}
	data, err := os.ReadFile(out)
	if err != nil || string(data) != "already encoded output" {
		t.Fatalf("output was re-encoded: %s %v", data, err)
	}
	if _, err := os.Stat(source); !os.IsNotExist(err) {
		t.Fatal("capture not cleaned")
	}
	if _, err := os.Stat(l.recordings.path(j)); !os.IsNotExist(err) {
		t.Fatal("completed job remains")
	}
}
