package callcdr

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/shridarpatil/tring/internal/media/record"
)

func (l *Log) processRecording(ctx context.Context, j *recordingJob) error {
	format := j.Format
	if len(j.Video) > 0 {
		format = "mp4"
	}
	out := filepath.Join(l.dir, j.LegID+"."+format)
	if !j.Published {
		if !j.Rendered {
			var customer string
			var agents []record.AgentLeg
			for _, t := range j.Audio {
				switch t.Role {
				case "customer":
					customer = t.Path
				case "agent":
					agents = append(agents, record.AgentLeg{Path: t.Path, Delay: t.Offset})
				}
			}
			if customer == "" && len(j.Video) == 0 {
				l.log.Info("cdr: no customer media captured, skipping recording", "call", j.CallID)
				return nil
			}
			sort.Slice(agents, func(i, k int) bool { return agents[i].Delay < agents[k].Delay })
			partial := filepath.Join(l.dir, j.LegID+".partial."+format)
			defer os.Remove(partial)
			// The timeout starts when a worker picks the job, never while queued.
			timeout := 60 * time.Second
			if len(j.Video) > 0 {
				timeout = min(2*time.Minute+2*j.Duration, 30*time.Minute)
			}
			renderCtx, cancel := context.WithTimeout(ctx, timeout)
			var err error
			switch {
			case len(j.Video) > 0:
				err = record.ComposeVideo(renderCtx, partial, j.Audio, j.Video, j.Duration, j.Rate)
			case len(agents) == 0:
				err = record.Encode(renderCtx, partial, customer, j.Rate)
			case len(agents) == 1:
				err = record.MergeStereo(renderCtx, partial, agents[0].Path, customer, j.Rate)
			default:
				err = record.MergeCall(renderCtx, partial, customer, agents, j.Rate)
			}
			cancel()
			if err != nil {
				return fmt.Errorf("render recording: %w", err)
			}
			if err = os.Rename(partial, out); err != nil {
				return err
			}
			j.Rendered = true
			if err = l.recordings.save(j); err != nil {
				return err
			}
		}
		uploaded, err := l.publishRecording(ctx, j, out)
		if err != nil {
			return err
		}
		j.Published = true
		j.RemoveOutput = uploaded
		if err := l.recordings.save(j); err != nil {
			return err
		}
	}
	// Keep every source until publication and its durable checkpoint succeed.
	// A failed merge never substitutes a customer-only file for a stereo call.
	for _, t := range j.Audio {
		if err := removeRecordingFile(t.Path); err != nil {
			return err
		}
	}
	for _, t := range j.Video {
		if err := removeRecordingFile(t.Path); err != nil {
			return err
		}
	}
	if j.RemoveOutput {
		return removeRecordingFile(out)
	}
	return nil
}

func removeRecordingFile(path string) error {
	err := os.Remove(path)
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

// publishRecording makes the rendered file servable: it stamps the call's
// recording URL and, when the tenant has a bucket, uploads it and stamps the
// key. It reports whether it uploaded (the local copy can then go).
func (l *Log) publishRecording(parent context.Context, j *recordingJob, out string) (bool, error) {
	ctx, cancel := context.WithTimeout(parent, 60*time.Second)
	defer cancel()
	if _, err := os.Stat(out); err != nil {
		return false, fmt.Errorf("recording output missing: %w", err)
	}
	// A recovered job must still belong to an existing call before it can
	// publish locally or upload (the tenant/call may have been deleted).
	var leg string
	var key *string
	if err := l.db.Pool.QueryRow(ctx, l.q.GetCallRecording, j.CallID, j.Tenant).Scan(&leg, &key); err != nil {
		return false, fmt.Errorf("recording call lookup: %w", err)
	}
	if leg != j.LegID {
		return false, fmt.Errorf("recording leg does not match call")
	}
	if _, err := l.db.Pool.Exec(ctx, l.q.SetCallRecording, j.LegID); err != nil {
		return false, fmt.Errorf("set recording URL: %w", err)
	}
	// A config the tenant can't use (e.g. sealed secret, no key) fails the job
	// so it is retried with its files kept, rather than silently staying local.
	store, err := l.blob.For(ctx, j.Tenant.String())
	if err != nil {
		return false, fmt.Errorf("recording store: %w", err)
	}
	if !store.Enabled() {
		return false, nil
	}
	objKey := store.Key(j.Tenant.String(), j.StartedAt, filepath.Base(out))
	if err := store.PutFile(ctx, objKey, out, recordingMIME[filepath.Ext(out)]); err != nil {
		return false, fmt.Errorf("upload recording: %w", err)
	}
	if _, err := l.db.Pool.Exec(ctx, l.q.SetCallRecordingS3, j.LegID, objKey); err != nil {
		return false, fmt.Errorf("stamp recording S3 key: %w", err)
	}
	return true, nil
}
