package handler

import (
	"log/slog"
	"strconv"

	"github.com/gin-gonic/gin"

	"github.com/gigmatch/gigmatch/internal/middleware"
	"github.com/gigmatch/gigmatch/internal/service"
	"github.com/gigmatch/gigmatch/internal/util"
)

// NotificationHandler exposes the in-app notification center endpoints.
type NotificationHandler struct {
	svc    *service.NotificationService
	logger *slog.Logger
}

// NewNotificationHandler builds a NotificationHandler.
func NewNotificationHandler(svc *service.NotificationService, logger *slog.Logger) *NotificationHandler {
	return &NotificationHandler{svc: svc, logger: logger}
}

// List handles GET /notifications. Every query is implicitly scoped to the
// authenticated user; unreadOnly=1 returns unread notifications only.
func (h *NotificationHandler) List(c *gin.Context) {
	u := middleware.GetCurrentUser(c)
	unreadOnly := c.Query("unreadOnly") == "1" || c.Query("unreadOnly") == "true"
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "20"))
	items, total, err := h.svc.List(u.ID, unreadOnly, page, pageSize)
	if err != nil {
		util.Fail(c, err)
		return
	}
	util.OK(c, gin.H{"items": items, "total": total, "page": page, "page_size": pageSize})
}

// UnreadCount handles GET /notifications/unread-count.
func (h *NotificationHandler) UnreadCount(c *gin.Context) {
	u := middleware.GetCurrentUser(c)
	count, err := h.svc.CountUnread(u.ID)
	if err != nil {
		util.Fail(c, err)
		return
	}
	util.OK(c, gin.H{"unread": count})
}

// MarkRead handles POST /notifications/:id/read. It rejects (404) both
// missing notifications and notifications owned by another user.
func (h *NotificationHandler) MarkRead(c *gin.Context) {
	id, ok := parseUintParam(c, "id")
	if !ok {
		return
	}
	u := middleware.GetCurrentUser(c)
	if err := h.svc.MarkRead(id, u.ID); err != nil {
		util.Fail(c, err)
		return
	}
	util.OK(c, gin.H{"id": id, "isRead": true})
}

// MarkAllRead handles POST /notifications/read-all.
func (h *NotificationHandler) MarkAllRead(c *gin.Context) {
	u := middleware.GetCurrentUser(c)
	affected, err := h.svc.MarkAllRead(u.ID)
	if err != nil {
		util.Fail(c, err)
		return
	}
	util.OK(c, gin.H{"updated": affected})
}
