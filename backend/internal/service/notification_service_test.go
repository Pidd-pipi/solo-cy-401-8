package service

import (
	"errors"
	"strings"
	"testing"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"github.com/gigmatch/gigmatch/internal/constants"
	"github.com/gigmatch/gigmatch/internal/dto"
	"github.com/gigmatch/gigmatch/internal/model"
	"github.com/gigmatch/gigmatch/internal/repository"
)

func newNotificationTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := "file:" + strings.ReplaceAll(t.Name(), "/", "_") + "_notif?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(
		&model.User{}, &model.Requirement{}, &model.Bid{}, &model.Contract{},
		&model.OperationLog{}, &model.Notification{},
	); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

func sampleNotifyCommand(recipient uint) NotifyCommand {
	return NotifyCommand{
		RecipientID: recipient,
		BizType:     constants.NotificationBidSubmitted,
		BizID:       42,
		BizNo:       "报价 #42",
		RefID:       7,
		Title:       "收到新报价",
		Content:     "测试通知内容",
	}
}

func TestNotificationIdempotent(t *testing.T) {
	db := newNotificationTestDB(t)
	svc := NewNotificationService(repository.NewNotificationRepository(db), discardLogger())
	cmd := sampleNotifyCommand(100)

	first, err := svc.NotifyErr(cmd)
	if err != nil {
		t.Fatalf("first NotifyErr: %v", err)
	}
	// Retrying the same event must not create a duplicate and must return
	// the same notification.
	second, err := svc.NotifyErr(cmd)
	if err != nil {
		t.Fatalf("second NotifyErr: %v", err)
	}
	if first.ID != second.ID {
		t.Fatalf("ids differ: %d vs %d, duplicate notification created", first.ID, second.ID)
	}
	items, total, err := svc.List(100, false, 1, 20)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if total != 1 || len(items) != 1 {
		t.Fatalf("total=%d len=%d, want 1/1", total, len(items))
	}
	if items[0].IsRead {
		t.Fatal("new notification should be unread")
	}
	if items[0].OccurredAt.IsZero() {
		t.Fatal("occurredAt must be set")
	}
	if items[0].BizNo != "报价 #42" {
		t.Fatalf("bizNo = %q", items[0].BizNo)
	}
}

func TestNotificationListScopedAndOrdered(t *testing.T) {
	db := newNotificationTestDB(t)
	svc := NewNotificationService(repository.NewNotificationRepository(db), discardLogger())

	// Two notifications for user 100, one for user 200.
	cmd := sampleNotifyCommand(100)
	if _, err := svc.NotifyErr(cmd); err != nil {
		t.Fatal(err)
	}
	cmd2 := cmd
	cmd2.BizType = constants.NotificationContractSigned
	cmd2.BizID = 50
	cmd2.BizNo = "CY-7-42"
	if _, err := svc.NotifyErr(cmd2); err != nil {
		t.Fatal(err)
	}
	other := sampleNotifyCommand(200)
	if _, err := svc.NotifyErr(other); err != nil {
		t.Fatal(err)
	}

	items, total, err := svc.List(100, false, 1, 20)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if total != 2 || len(items) != 2 {
		t.Fatalf("total=%d len=%d, want 2/2", total, len(items))
	}
	// Newest first: the later event (id 2 / contract) must come first.
	if items[0].BizID != 50 || items[1].BizID != 42 {
		t.Fatalf("order = %d, %d, want 50, 42", items[0].BizID, items[1].BizID)
	}

	unread, err := svc.CountUnread(100)
	if err != nil {
		t.Fatal(err)
	}
	if unread != 2 {
		t.Fatalf("unread = %d, want 2", unread)
	}
}

func TestNotificationMarkReadOwnership(t *testing.T) {
	db := newNotificationTestDB(t)
	svc := NewNotificationService(repository.NewNotificationRepository(db), discardLogger())
	n, err := svc.NotifyErr(sampleNotifyCommand(100))
	if err != nil {
		t.Fatal(err)
	}

	// Another user marking the notification read is explicitly forbidden
	// (403); the notification must not be modified.
	if err := svc.MarkRead(n.ID, 200); !errors.Is(err, constants.ErrForbidden) {
		t.Fatalf("cross-user MarkRead err = %v, want ErrForbidden", err)
	}
	// A missing id is refused as not found (404).
	if err := svc.MarkRead(99999, 100); !errors.Is(err, repository.ErrNotFound) {
		t.Fatalf("missing MarkRead err = %v, want ErrNotFound", err)
	}
	// Recipient state unchanged after the unauthorized attempt.
	if unread, _ := svc.CountUnread(100); unread != 1 {
		t.Fatalf("unread after cross-user attempt = %d, want 1", unread)
	}

	// The owner can mark it read; doing it twice stays idempotent.
	if err := svc.MarkRead(n.ID, 100); err != nil {
		t.Fatalf("owner MarkRead: %v", err)
	}
	if err := svc.MarkRead(n.ID, 100); err != nil {
		t.Fatalf("second MarkRead: %v", err)
	}
	if unread, _ := svc.CountUnread(100); unread != 0 {
		t.Fatalf("unread = %d, want 0", unread)
	}

	affected, err := svc.MarkAllRead(200)
	if err != nil {
		t.Fatal(err)
	}
	if affected != 0 {
		t.Fatalf("mark-all for user without notifications affected = %d, want 0", affected)
	}
}

// TestBusinessFlowEmitsNotifications verifies the four business events each
// create exactly one notification for the other party, with correct type and
// read state, and that retrying an event cannot duplicate it.
func TestBusinessFlowEmitsNotifications(t *testing.T) {
	db := newNotificationTestDB(t)
	logger := discardLogger()

	userRepo := repository.NewUserRepository(db)
	reqRepo := repository.NewRequirementRepository(db)
	bidRepo := repository.NewBidRepository(db)
	contractRepo := repository.NewContractRepository(db)
	notifRepo := repository.NewNotificationRepository(db)

	requester := &model.User{Username: "req-notif", Name: "需求方", Role: constants.RoleRequester}
	freelancer := &model.User{Username: "free-notif", Name: "自由职业者", Role: constants.RoleFreelancer}
	if err := userRepo.Create(requester); err != nil {
		t.Fatal(err)
	}
	if err := userRepo.Create(freelancer); err != nil {
		t.Fatal(err)
	}

	logSvc := NewOperationLogService(repository.NewOperationLogRepository(db), logger)
	notifSvc := NewNotificationService(notifRepo, logger)
	reqSvc := NewRequirementService(reqRepo, bidRepo, notifSvc, logSvc, logger)
	bidSvc := NewBidService(bidRepo, reqRepo, notifSvc, logSvc, logger)
	contractSvc := NewContractService(contractRepo, notifSvc, logSvc, logger)

	reqEntity, err := reqSvc.Create(dto.CreateRequirementRequest{
		Title: "通知流程需求", Description: "用于验证通知中心全流程的需求", MinBudget: 1000, MaxBudget: 5000, Skills: []string{"Go"},
	}, requester.ID, requester.Name, requester.Role)
	if err != nil {
		t.Fatalf("create requirement: %v", err)
	}
	bid, err := bidSvc.Create(dto.CreateBidRequest{
		RequirementID: reqEntity.ID, Amount: 4000, DurationDays: 20, Proposal: "我可以按要求完成该需求开发工作",
	}, freelancer.ID, freelancer.Name, freelancer.Role)
	if err != nil {
		t.Fatalf("create bid: %v", err)
	}

	// 1) Bid submitted -> requester gets one unread notification.
	if c, _ := notifSvc.CountUnread(requester.ID); c != 1 {
		t.Fatalf("after bid: requester unread = %d, want 1", c)
	}
	if c, _ := notifSvc.CountUnread(freelancer.ID); c != 0 {
		t.Fatalf("after bid: freelancer unread = %d, want 0", c)
	}

	contract, err := reqSvc.AcceptBid(reqEntity.ID, bid.ID, requester.ID, requester.Name, "installments", contractSvc)
	if err != nil {
		t.Fatalf("accept bid: %v", err)
	}
	// 2) Bid accepted -> freelancer notified; requester still has 1.
	if c, _ := notifSvc.CountUnread(freelancer.ID); c != 1 {
		t.Fatalf("after accept: freelancer unread = %d, want 1", c)
	}

	if _, err := contractSvc.Sign(contract.ID, requester.ID, requester.Name); err != nil {
		t.Fatalf("sign: %v", err)
	}
	// 3) Contract signed -> other party (freelancer) notified.
	if c, _ := notifSvc.CountUnread(freelancer.ID); c != 2 {
		t.Fatalf("after sign: freelancer unread = %d, want 2", c)
	}
	if c, _ := notifSvc.CountUnread(requester.ID); c != 1 {
		t.Fatalf("after sign: requester unread = %d, want 1", c)
	}

	// Retrying sign on an in_progress contract is rejected, so no duplicate.
	if _, err := contractSvc.Sign(contract.ID, freelancer.ID, freelancer.Name); err == nil {
		t.Fatal("second sign must fail with conflict")
	}
	if c, _ := notifSvc.CountUnread(requester.ID); c != 1 {
		t.Fatalf("after rejected re-sign: requester unread = %d, want 1", c)
	}

	if _, err := contractSvc.Complete(contract.ID, requester.ID, requester.Name); err != nil {
		t.Fatalf("complete: %v", err)
	}
	// 4) Contract completed -> freelancer notified.
	if c, _ := notifSvc.CountUnread(freelancer.ID); c != 3 {
		t.Fatalf("after complete: freelancer unread = %d, want 3", c)
	}
	if _, err := contractSvc.Complete(contract.ID, requester.ID, requester.Name); err == nil {
		t.Fatal("second complete must fail with conflict")
	}
	if c, _ := notifSvc.CountUnread(freelancer.ID); c != 3 {
		t.Fatalf("after rejected re-complete: freelancer unread = %d, want 3", c)
	}

	// Exactly four distinct events persisted overall, newest first.
	items, total, err := notifSvc.List(freelancer.ID, false, 1, 20)
	if err != nil {
		t.Fatal(err)
	}
	if total != 3 {
		t.Fatalf("freelancer notifications total = %d, want 3", total)
	}
	wantTypes := []string{
		constants.NotificationContractCompleted,
		constants.NotificationContractSigned,
		constants.NotificationBidAccepted,
	}
	for i, want := range wantTypes {
		if items[i].BizType != want {
			t.Fatalf("items[%d].bizType = %q, want %q", i, items[i].BizType, want)
		}
	}

	// Direct idempotency guard: replaying the accept notification by key
	// must not insert another row.
	replay := NotifyCommand{
		RecipientID: freelancer.ID,
		BizType:     constants.NotificationBidAccepted,
		BizID:       bid.ID,
		BizNo:       "报价 #1",
		RefID:       reqEntity.ID,
		Title:       "重放",
		Content:     "不应入库",
	}
	n1, err := notifSvc.NotifyErr(replay)
	if err != nil {
		t.Fatal(err)
	}
	if _, totalAll, _ := notifSvc.List(freelancer.ID, false, 1, 20); totalAll != 3 {
		t.Fatalf("after replay total = %d, want 3 (id=%d)", totalAll, n1.ID)
	}

	// Reading notifications must not mutate business entities.
	reloaded, err := contractRepo.FindByID(contract.ID)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.Status != constants.ContractCompleted {
		t.Fatalf("contract status changed to %q, want completed", reloaded.Status)
	}
	for _, n := range items {
		if err := notifSvc.MarkRead(n.ID, freelancer.ID); err != nil {
			t.Fatal(err)
		}
	}
	reloaded2, err := contractRepo.FindByID(contract.ID)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded2.Status != constants.ContractCompleted {
		t.Fatalf("contract status changed after notifications read: %q", reloaded2.Status)
	}
	reloadedBid, err := bidRepo.FindByID(bid.ID)
	if err != nil {
		t.Fatal(err)
	}
	if reloadedBid.Status != constants.BidAccepted {
		t.Fatalf("bid status changed after notifications read: %q", reloadedBid.Status)
	}
}
