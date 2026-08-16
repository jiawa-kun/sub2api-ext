package creative

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"
)

const (
	KeyMediaHealthJSON = "creative_media_health_json"
	KeyMediaAuditJSON  = "creative_media_audit_json"
)

// MediaHealth is a connectivity snapshot for the active media backend.
type MediaHealth struct {
	OK        bool      `json:"ok"`
	Driver    string    `json:"driver"`
	Message   string    `json:"message"`
	LatencyMS int64     `json:"latency_ms"`
	CheckedAt time.Time `json:"checked_at"`
	Source    string    `json:"source,omitempty"` // live|cache
}

// MissingMediaItem is one DB media pointer that cannot be opened from storage.
type MissingMediaItem struct {
	JobID        int64     `json:"job_id"`
	UserID       int64     `json:"user_id"`
	MediaType    string    `json:"media_type"`
	ImageIndex   int       `json:"image_index"`
	FileName     string    `json:"file_name"`
	ExpectedSize int64     `json:"expected_size"`
	JobStatus    string    `json:"job_status"`
	Reason       string    `json:"reason"`
	CreatedAt    time.Time `json:"created_at"`
}

// MediaAuditReport summarizes archived-media integrity against the active store.
type MediaAuditReport struct {
	Health       MediaHealth        `json:"health"`
	Scanned      int                `json:"scanned"`
	Present      int                `json:"present"`
	Missing      int                `json:"missing"`
	SizeMismatch int                `json:"size_mismatch"`
	MissingItems []MissingMediaItem `json:"missing_items"`
	DurationMS   int64              `json:"duration_ms"`
	CheckedAt    time.Time          `json:"checked_at"`
	Source       string             `json:"source,omitempty"`
}

type mediaAuditState struct {
	mu     sync.RWMutex
	health *MediaHealth
	audit  *MediaAuditReport
}

func (s *Service) mediaAudit() *mediaAuditState {
	// lazy init via dedicated field set in ensureMediaAudit
	return s.ensureMediaAudit()
}

func (s *Service) ensureMediaAudit() *mediaAuditState {
	if s == nil {
		return nil
	}
	s.mediaAuditOnce.Do(func() {
		s.mediaAuditState = &mediaAuditState{}
		// best-effort load cache
		if s.store != nil {
			if raw, ok, err := s.store.GetSetting(context.Background(), KeyMediaHealthJSON); err == nil && ok && strings.TrimSpace(raw) != "" {
				var h MediaHealth
				if json.Unmarshal([]byte(raw), &h) == nil {
					h.Source = "cache"
					s.mediaAuditState.health = &h
				}
			}
			if raw, ok, err := s.store.GetSetting(context.Background(), KeyMediaAuditJSON); err == nil && ok && strings.TrimSpace(raw) != "" {
				var a MediaAuditReport
				if json.Unmarshal([]byte(raw), &a) == nil {
					a.Source = "cache"
					s.mediaAuditState.audit = &a
				}
			}
		}
	})
	return s.mediaAuditState
}

// LastMediaHealth returns cached health if any.
func (s *Service) LastMediaHealth() *MediaHealth {
	st := s.ensureMediaAudit()
	if st == nil {
		return nil
	}
	st.mu.RLock()
	defer st.mu.RUnlock()
	if st.health == nil {
		return nil
	}
	cp := *st.health
	return &cp
}

// LastMediaAudit returns cached audit if any.
func (s *Service) LastMediaAudit() *MediaAuditReport {
	st := s.ensureMediaAudit()
	if st == nil {
		return nil
	}
	st.mu.RLock()
	defer st.mu.RUnlock()
	if st.audit == nil {
		return nil
	}
	cp := *st.audit
	return &cp
}

// CheckMediaHealth probes the currently applied media runtime.
func (s *Service) CheckMediaHealth(ctx context.Context) (MediaHealth, error) {
	start := time.Now()
	rt := s.MediaRuntime()
	h := MediaHealth{
		Driver:    rt.Driver,
		CheckedAt: start.UTC(),
		Source:    "live",
	}
	if !s.mediaReady() {
		h.OK = false
		h.Message = "媒体存储未配置"
		h.LatencyMS = time.Since(start).Milliseconds()
		s.persistMediaHealth(ctx, h)
		return h, fmt.Errorf("%s", h.Message)
	}
	probeCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	if err := ProbeMediaRuntime(probeCtx, rt); err != nil {
		h.OK = false
		h.Message = err.Error()
		h.LatencyMS = time.Since(start).Milliseconds()
		s.persistMediaHealth(ctx, h)
		return h, err
	}
	h.OK = true
	h.Message = "媒体存储连通性正常"
	h.LatencyMS = time.Since(start).Milliseconds()
	s.persistMediaHealth(ctx, h)
	return h, nil
}

// AuditMissingMedia walks DB media refs and stats them against active storage.
func (s *Service) AuditMissingMedia(ctx context.Context) (MediaAuditReport, error) {
	start := time.Now()
	report := MediaAuditReport{
		CheckedAt:    start.UTC(),
		MissingItems: []MissingMediaItem{},
		Source:       "live",
	}
	// always refresh health first (non-fatal for listing missing if probe fails? better include health)
	if h, err := s.CheckMediaHealth(ctx); err != nil {
		report.Health = h
		// still attempt scan; broken backend will mark most as missing/errors
	} else {
		report.Health = h
	}

	if s == nil || s.store == nil {
		return report, fmt.Errorf("store unavailable")
	}
	if !s.mediaReady() {
		report.DurationMS = time.Since(start).Milliseconds()
		s.persistMediaAudit(ctx, report)
		return report, fmt.Errorf("媒体存储未配置")
	}

	refs, err := s.store.ListCreativeMediaRefs(ctx)
	if err != nil {
		report.DurationMS = time.Since(start).Milliseconds()
		return report, err
	}
	report.Scanned = len(refs)

	s.mediaMu.Lock()
	ms := s.mediaStore
	s.mediaMu.Unlock()
	if ms == nil {
		report.DurationMS = time.Since(start).Milliseconds()
		s.persistMediaAudit(ctx, report)
		return report, fmt.Errorf("媒体存储未配置")
	}

	for _, ref := range refs {
		kind := MediaKindImage
		if strings.EqualFold(ref.MediaType, "video") {
			kind = MediaKindVideo
		}
		size, _, ok, statErr := ms.Stat(ctx, kind, ref.FileName)
		if statErr != nil {
			report.Missing++
			report.MissingItems = append(report.MissingItems, MissingMediaItem{
				JobID:        ref.JobID,
				UserID:       ref.UserID,
				MediaType:    ref.MediaType,
				ImageIndex:   ref.ImageIndex,
				FileName:     ref.FileName,
				ExpectedSize: ref.ExpectedSize,
				JobStatus:    ref.JobStatus,
				Reason:       "stat_error: " + statErr.Error(),
				CreatedAt:    ref.MediaCreatedAt,
			})
			continue
		}
		if !ok {
			report.Missing++
			report.MissingItems = append(report.MissingItems, MissingMediaItem{
				JobID:        ref.JobID,
				UserID:       ref.UserID,
				MediaType:    ref.MediaType,
				ImageIndex:   ref.ImageIndex,
				FileName:     ref.FileName,
				ExpectedSize: ref.ExpectedSize,
				JobStatus:    ref.JobStatus,
				Reason:       "missing",
				CreatedAt:    ref.MediaCreatedAt,
			})
			continue
		}
		if ref.ExpectedSize > 0 && size > 0 && size != ref.ExpectedSize {
			report.SizeMismatch++
			report.Missing++
			report.MissingItems = append(report.MissingItems, MissingMediaItem{
				JobID:        ref.JobID,
				UserID:       ref.UserID,
				MediaType:    ref.MediaType,
				ImageIndex:   ref.ImageIndex,
				FileName:     ref.FileName,
				ExpectedSize: ref.ExpectedSize,
				JobStatus:    ref.JobStatus,
				Reason:       fmt.Sprintf("size_mismatch remote=%d", size),
				CreatedAt:    ref.MediaCreatedAt,
			})
			continue
		}
		report.Present++
	}

	report.DurationMS = time.Since(start).Milliseconds()
	s.persistMediaAudit(ctx, report)
	return report, nil
}

// MediaAvailability looks up a single ref in the last audit cache.
// Returns ok=false, known=false when no cache.
func (s *Service) MediaAvailability(jobID int64, mediaType string, imageIndex int, fileName string) (available bool, known bool, reason string) {
	audit := s.LastMediaAudit()
	if audit == nil {
		return false, false, ""
	}
	fileName = strings.TrimSpace(fileName)
	for _, item := range audit.MissingItems {
		if item.JobID != jobID {
			continue
		}
		if fileName != "" && item.FileName == fileName {
			return false, true, item.Reason
		}
		if fileName == "" && strings.EqualFold(item.MediaType, mediaType) {
			// imageIndex < 0 means any image under the job
			if imageIndex < 0 || item.ImageIndex == imageIndex {
				return false, true, item.Reason
			}
		}
	}
	// if scanned and not in missing list, treat as available when file known in DB era of audit
	return true, true, ""
}

func (s *Service) persistMediaHealth(ctx context.Context, h MediaHealth) {
	st := s.ensureMediaAudit()
	if st != nil {
		st.mu.Lock()
		cp := h
		st.health = &cp
		st.mu.Unlock()
	}
	if s == nil || s.store == nil {
		return
	}
	raw, err := json.Marshal(h)
	if err != nil {
		return
	}
	_ = s.store.SetSetting(ctx, KeyMediaHealthJSON, string(raw))
}

func (s *Service) persistMediaAudit(ctx context.Context, report MediaAuditReport) {
	st := s.ensureMediaAudit()
	if st != nil {
		st.mu.Lock()
		cp := report
		st.audit = &cp
		st.mu.Unlock()
	}
	if s == nil || s.store == nil {
		return
	}
	// keep payload bounded
	if len(report.MissingItems) > 500 {
		trimmed := report
		trimmed.MissingItems = append([]MissingMediaItem(nil), report.MissingItems[:500]...)
		report = trimmed
	}
	raw, err := json.Marshal(report)
	if err != nil {
		return
	}
	_ = s.store.SetSetting(ctx, KeyMediaAuditJSON, string(raw))
}
