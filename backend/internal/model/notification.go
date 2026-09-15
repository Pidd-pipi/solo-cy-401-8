package model

import "time"

// Notification is an in-app message delivered to one user when a business
// event happens (bid submitted/accepted, contract signed/completed).
type Notification struct {
	ID          uint `gorm:"primaryKey" json:"id"`
	RecipientID uint `gorm:"index;not null" json:"recipientId"`
	// BizType is the business type, see constants.Notification*.
	BizType string `gorm:"size:32;index;not null" json:"bizType"`
	// BizID is the primary business entity id (bid id or contract id).
	BizID uint `gorm:"not null" json:"bizId"`
	// BizNo is the human-readable business number (bid #id or contract no).
	BizNo string `gorm:"size:64" json:"bizNo"`
	// RefID points at the requirement used for frontend navigation.
	RefID   uint   `gorm:"index" json:"refId"`
	Title   string `gorm:"size:128;not null" json:"title"`
	Content string `gorm:"size:512" json:"content"`
	// EventKey makes (bizType, bizId) idempotent; retries never duplicate.
	EventKey   string    `gorm:"size:96;uniqueIndex;not null" json:"-"`
	IsRead     bool      `gorm:"index;not null;default:false" json:"isRead"`
	OccurredAt time.Time `gorm:"index;not null" json:"occurredAt"`
	CreatedAt  time.Time `json:"createdAt"`
}
