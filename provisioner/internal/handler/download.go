package handler

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"time"

	"nimda-judge-provisioner/internal/aws"
	"nimda-judge-provisioner/internal/config"
	"nimda-judge-provisioner/internal/queue"
)

const challengePrefix = "challenges/"

// 문제 코드 화이트 리스트
var challengeCodePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,49}$`)

// 이 핸들러가 S3에서 필요한 기능만 추린 인터페이스.
type S3Presigner interface {
	PresignGetObject(ctx context.Context, objectKey string, ttl time.Duration) (string, time.Time, error)
}

// 첨부파일 다운로드 핸들러.
type DownloadHandler struct {
	s3  S3Presigner
	cfg config.Config
	log *slog.Logger
}

func NewDownloadHandler(s3 S3Presigner, cfg config.Config, log *slog.Logger) *DownloadHandler {
	return &DownloadHandler{s3: s3, cfg: cfg, log: log}
}

func (h *DownloadHandler) Handle(ctx context.Context, msg *GetPresignedURL) (*queue.Result, error) {
	h.log.Info("download 처리 시작",
		"uuid", msg.UUID, "challengeCode", msg.ChallengeCode,
		"challengeCategory", msg.ChallengeCategory)

	objectKey, err := resolveObjectKey(msg.ChallengeCode)
	if err != nil {
		return h.downloadFailed(msg, err.Error()), nil
	}

	url, expiresAt, err := h.s3.PresignGetObject(ctx, objectKey, h.cfg.PresignTTL)
	if err != nil {
		// 파일이 존재하지 않는 경우
		if errors.Is(err, aws.ErrObjectNotFound) {
			return h.downloadFailed(msg, "첨부파일이 없습니다: "+objectKey), nil
		}

		h.log.Warn("presign 실패, 재시도 예정", "uuid", msg.UUID, "error", err)
		return nil, err
	}

	h.log.Info("presigned URL 발급 완료",
		"uuid", msg.UUID, "objectKey", objectKey, "expiresAt", expiresAt.Format(time.RFC3339))

	return &queue.Result{
		UUID:        msg.UUID,
		Status:      queue.StatusReady,
		DownloadURL: url,
		ExpiresAt:   expiresAt.Format(time.RFC3339),
	}, nil
}

// PresignedUrl 반환
func resolveObjectKey(challengeCode string) (string, error) {
	if !challengeCodePattern.MatchString(challengeCode) {
		return "", fmt.Errorf("허용되지 않는 문제 코드: %q", challengeCode)
	}
	return challengePrefix + challengeCode + "/" + challengeCode + ".zip", nil
}

func (h *DownloadHandler) downloadFailed(msg *GetPresignedURL, reason string) *queue.Result {
	h.log.Error("download 실패", "uuid", msg.UUID, "reason", reason)
	return &queue.Result{
		UUID:    msg.UUID,
		Status:  queue.StatusFailed,
		Message: reason,
	}
}
