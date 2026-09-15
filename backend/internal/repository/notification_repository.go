package repository

import (
	"errors"
	"fmt"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/gigmatch/gigmatch/internal/model"
)

// NotificationRepository persists in-app notifications.
type NotificationRepository struct {
	db *gorm.DB
}

// NewNotificationRepository builds a NotificationRepository.
func NewNotificationRepository(db *gorm.DB) *NotificationRepository {
	return &NotificationRepository{db: db}
}

// Create inserts a notification. The unique event_key makes creation
// idempotent: a retried event is a no-op (INSERT ... ON CONFLICT DO NOTHING
// / INSERT IGNORE) and surfaces ErrDuplicateEvent so callers can treat it as
// success without creating duplicates.
func (r *NotificationRepository) Create(n *model.Notification) error {
	tx := r.db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "event_key"}},
		DoNothing: true,
	}).Create(n)
	if tx.Error != nil {
		return fmt.Errorf("create notification: %w", tx.Error)
	}
	if tx.RowsAffected == 0 {
		return ErrDuplicateEvent
	}
	return nil
}

// FindByEventKey loads one notification by its idempotency event key.
func (r *NotificationRepository) FindByEventKey(eventKey string) (*model.Notification, error) {
	var n model.Notification
	if err := r.db.Where("event_key = ?", eventKey).First(&n).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("find notification by event key: %w", err)
	}
	return &n, nil
}

// ListByRecipient returns a recipient's notifications, newest first.
// unreadOnly=true filters to unread notifications; pagination is applied.
func (r *NotificationRepository) ListByRecipient(recipientID uint, unreadOnly bool, offset, limit int) ([]model.Notification, int64, error) {
	q := r.db.Model(&model.Notification{}).Where("recipient_id = ?", recipientID)
	if unreadOnly {
		q = q.Where("is_read = ?", false)
	}
	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, fmt.Errorf("count notifications: %w", err)
	}
	var items []model.Notification
	if err := q.Order("occurred_at DESC, id DESC").
		Offset(offset).
		Limit(limit).
		Find(&items).Error; err != nil {
		return nil, 0, fmt.Errorf("list notifications: %w", err)
	}
	return items, total, nil
}

// FindByID loads one notification regardless of owner. The service layer is
// responsible for enforcing recipient ownership.
func (r *NotificationRepository) FindByID(id uint) (*model.Notification, error) {
	var n model.Notification
	if err := r.db.First(&n, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("find notification by id: %w", err)
	}
	return &n, nil
}

// MarkRead flags one notification as read. It is scoped to the recipient so
// that callers can never touch another user's notifications.
func (r *NotificationRepository) MarkRead(id, recipientID uint) (int64, error) {
	tx := r.db.Model(&model.Notification{}).
		Where("id = ? AND recipient_id = ? AND is_read = ?", id, recipientID, false).
		Update("is_read", true)
	if tx.Error != nil {
		return 0, fmt.Errorf("mark notification read: %w", tx.Error)
	}
	return tx.RowsAffected, nil
}

// MarkAllRead flags every unread notification of a recipient as read.
func (r *NotificationRepository) MarkAllRead(recipientID uint) (int64, error) {
	tx := r.db.Model(&model.Notification{}).
		Where("recipient_id = ? AND is_read = ?", recipientID, false).
		Update("is_read", true)
	if tx.Error != nil {
		return 0, fmt.Errorf("mark all notifications read: %w", tx.Error)
	}
	return tx.RowsAffected, nil
}

// CountUnread returns the number of unread notifications of a recipient.
func (r *NotificationRepository) CountUnread(recipientID uint) (int64, error) {
	var count int64
	if err := r.db.Model(&model.Notification{}).
		Where("recipient_id = ? AND is_read = ?", recipientID, false).
		Count(&count).Error; err != nil {
		return 0, fmt.Errorf("count unread notifications: %w", err)
	}
	return count, nil
}
