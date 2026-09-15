package service

import (
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/gigmatch/gigmatch/internal/constants"
	"github.com/gigmatch/gigmatch/internal/model"
	"github.com/gigmatch/gigmatch/internal/repository"
)

// NotifyCommand is the input for emitting a single notification.
type NotifyCommand struct {
	RecipientID uint
	BizType     string
	BizID       uint
	BizNo       string
	RefID       uint
	Title       string
	Content     string
}

// NotificationService creates and manages in-app notifications.
type NotificationService struct {
	notifications *repository.NotificationRepository
	logger        *slog.Logger
}

// NewNotificationService builds a NotificationService.
func NewNotificationService(notifications *repository.NotificationRepository, logger *slog.Logger) *NotificationService {
	return &NotificationService{notifications: notifications, logger: logger}
}

// Notify inserts a notification for one business event. It is idempotent on
// (bizType, bizId): retrying the same event returns the existing notification
// and never creates a duplicate. A failure never propagates to callers —
// notifications are a side effect and must not break business flows.
func (s *NotificationService) Notify(cmd NotifyCommand) {
	if cmd.RecipientID == 0 || cmd.BizType == "" || cmd.BizID == 0 {
		s.logger.Warn("skip notification: invalid command", "bizType", cmd.BizType, "bizId", cmd.BizID)
		return
	}
	n, err := s.build(cmd)
	if err != nil {
		s.logger.Warn("build notification failed", "error", err)
		return
	}
	if err := s.notifications.Create(n); err != nil {
		if errors.Is(err, repository.ErrDuplicateEvent) {
			s.logger.Info("duplicate notification skipped", "eventKey", n.EventKey)
			return
		}
		s.logger.Warn("create notification failed", "error", err, "eventKey", n.EventKey)
	}
}

// NotifyErr works like Notify but returns the underlying error for callers
// (e.g. tests) that want to assert idempotent persistence directly.
func (s *NotificationService) NotifyErr(cmd NotifyCommand) (*model.Notification, error) {
	n, err := s.build(cmd)
	if err != nil {
		return nil, err
	}
	if err := s.notifications.Create(n); err != nil {
		if errors.Is(err, repository.ErrDuplicateEvent) {
			existing, findErr := s.notifications.FindByEventKey(n.EventKey)
			if findErr != nil {
				return nil, findErr
			}
			return existing, nil
		}
		return nil, err
	}
	return n, nil
}

func (s *NotificationService) build(cmd NotifyCommand) (*model.Notification, error) {
	if cmd.RecipientID == 0 {
		return nil, errors.New("recipient id is required")
	}
	if cmd.BizType == "" || cmd.BizID == 0 {
		return nil, errors.New("biz type and biz id are required")
	}
	now := time.Now()
	return &model.Notification{
		RecipientID: cmd.RecipientID,
		BizType:     cmd.BizType,
		BizID:       cmd.BizID,
		BizNo:       cmd.BizNo,
		RefID:       cmd.RefID,
		Title:       cmd.Title,
		Content:     cmd.Content,
		EventKey:    eventKey(cmd.BizType, cmd.BizID),
		IsRead:      false,
		OccurredAt:  now,
	}, nil
}

// eventKey identifies one business event for idempotency.
func eventKey(bizType string, bizID uint) string {
	return fmt.Sprintf("%s:%d", bizType, bizID)
}

// List returns the caller's notifications newest first.
func (s *NotificationService) List(recipientID uint, unreadOnly bool, page, pageSize int) ([]model.Notification, int64, error) {
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 100 {
		pageSize = 20
	}
	items, total, err := s.notifications.ListByRecipient(recipientID, unreadOnly, (page-1)*pageSize, pageSize)
	if err != nil {
		return nil, 0, fmt.Errorf("list notifications: %w", err)
	}
	return items, total, nil
}

// MarkRead flags one notification as read. Only the owner may do this:
// a missing notification returns ErrNotFound (404) and a notification owned
// by another user returns ErrForbidden (403), both rejected explicitly.
func (s *NotificationService) MarkRead(id, recipientID uint) error {
	n, err := s.notifications.FindByID(id)
	if err != nil {
		return err
	}
	if n.RecipientID != recipientID {
		return constants.ErrForbidden
	}
	if n.IsRead {
		return nil
	}
	if _, err := s.notifications.MarkRead(id, recipientID); err != nil {
		return fmt.Errorf("mark notification read: %w", err)
	}
	return nil
}

// MarkAllRead flags every unread notification of the caller as read.
func (s *NotificationService) MarkAllRead(recipientID uint) (int64, error) {
	affected, err := s.notifications.MarkAllRead(recipientID)
	if err != nil {
		return 0, fmt.Errorf("mark all notifications read: %w", err)
	}
	return affected, nil
}

// CountUnread returns the caller's unread notification count.
func (s *NotificationService) CountUnread(recipientID uint) (int64, error) {
	count, err := s.notifications.CountUnread(recipientID)
	if err != nil {
		return 0, fmt.Errorf("count unread notifications: %w", err)
	}
	return count, nil
}
