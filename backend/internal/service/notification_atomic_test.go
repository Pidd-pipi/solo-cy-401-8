package service

import (
	"errors"
	"sync"
	"testing"

	"gorm.io/gorm"

	"github.com/gigmatch/gigmatch/internal/constants"
	"github.com/gigmatch/gigmatch/internal/dto"
	"github.com/gigmatch/gigmatch/internal/model"
	"github.com/gigmatch/gigmatch/internal/repository"
)

type atomicFixture struct {
	db           *gorm.DB
	logSvc       *OperationLogService
	notifSvc     *NotificationService
	reqSvc       *RequirementService
	bidSvc       *BidService
	contractSvc  *ContractService
	reqRepo      *repository.RequirementRepository
	bidRepo      *repository.BidRepository
	contractRepo *repository.ContractRepository
	notifRepo    *repository.NotificationRepository
	requester    *model.User
	freelancer   *model.User
}

func newAtomicFixture(t *testing.T) *atomicFixture {
	t.Helper()
	db := newNotificationTestDB(t)
	logger := discardLogger()

	reqRepo := repository.NewRequirementRepository(db)
	bidRepo := repository.NewBidRepository(db)
	contractRepo := repository.NewContractRepository(db)
	notifRepo := repository.NewNotificationRepository(db)

	requester := &model.User{Username: "req-atomic", Name: "需求方", Role: constants.RoleRequester}
	freelancer := &model.User{Username: "free-atomic", Name: "自由职业者", Role: constants.RoleFreelancer}
	if err := repository.NewUserRepository(db).Create(requester); err != nil {
		t.Fatal(err)
	}
	if err := repository.NewUserRepository(db).Create(freelancer); err != nil {
		t.Fatal(err)
	}

	logSvc := NewOperationLogService(repository.NewOperationLogRepository(db), logger)
	notifSvc := NewNotificationService(notifRepo, logger)

	return &atomicFixture{
		db:           db,
		logSvc:       logSvc,
		notifSvc:     notifSvc,
		reqSvc:       NewRequirementService(db, reqRepo, bidRepo, notifSvc, logSvc, logger),
		bidSvc:       NewBidService(db, bidRepo, reqRepo, notifSvc, logSvc, logger),
		contractSvc:  NewContractService(db, contractRepo, notifSvc, logSvc, logger),
		reqRepo:      reqRepo,
		bidRepo:      bidRepo,
		contractRepo: contractRepo,
		notifRepo:    notifRepo,
		requester:    requester,
		freelancer:   freelancer,
	}
}

func (f *atomicFixture) publishRequirement(t *testing.T) *model.Requirement {
	t.Helper()
	r, err := f.reqSvc.Create(dto.CreateRequirementRequest{
		Title: "原子性需求", Description: "用于验证通知与业务同事务的需求描述", MinBudget: 1000, MaxBudget: 5000, Skills: []string{"Go"},
	}, f.requester.ID, f.requester.Name, f.requester.Role)
	if err != nil {
		t.Fatalf("create requirement: %v", err)
	}
	return r
}

func (f *atomicFixture) submitBid(t *testing.T, requirementID uint) *model.Bid {
	t.Helper()
	b, err := f.bidSvc.Create(dto.CreateBidRequest{
		RequirementID: requirementID, Amount: 3000, DurationDays: 15, Proposal: "我可以按要求完成该需求开发工作",
	}, f.freelancer.ID, f.freelancer.Name, f.freelancer.Role)
	if err != nil {
		t.Fatalf("create bid: %v", err)
	}
	return b
}

func (f *atomicFixture) accept(t *testing.T, requirementID, bidID uint) *model.Contract {
	t.Helper()
	c, err := f.reqSvc.AcceptBid(requirementID, bidID, f.requester.ID, f.requester.Name, "installments", f.contractSvc)
	if err != nil {
		t.Fatalf("accept bid: %v", err)
	}
	return c
}

func (f *atomicFixture) totalNotifications(t *testing.T) int64 {
	t.Helper()
	var n int64
	if err := f.db.Model(&model.Notification{}).Count(&n).Error; err != nil {
		t.Fatalf("count notifications: %v", err)
	}
	return n
}

func (f *atomicFixture) countByType(t *testing.T, bizType string) int64 {
	t.Helper()
	var n int64
	if err := f.db.Model(&model.Notification{}).Where("biz_type = ?", bizType).Count(&n).Error; err != nil {
		t.Fatalf("count notifications by type: %v", err)
	}
	return n
}

func (f *atomicFixture) dropNotifications(t *testing.T) {
	t.Helper()
	if err := f.db.Migrator().DropTable("notifications"); err != nil {
		t.Fatalf("drop notifications: %v", err)
	}
}

func (f *atomicFixture) restoreNotifications(t *testing.T) {
	t.Helper()
	if err := f.db.AutoMigrate(&model.Notification{}); err != nil {
		t.Fatalf("restore notifications: %v", err)
	}
}

// TestNotificationFailureRollsBackAllFlows verifies the core atomicity rule:
// if notification persistence fails, no business state reaches its terminal
// state; after recovery, retrying succeeds with exactly one notification.
func TestNotificationFailureRollsBackAllFlows(t *testing.T) {
	f := newAtomicFixture(t)

	// --- 1) Bid submission ---
	r := f.publishRequirement(t)
	f.dropNotifications(t)
	_, err := f.bidSvc.Create(dto.CreateBidRequest{
		RequirementID: r.ID, Amount: 3000, DurationDays: 15, Proposal: "我可以按要求完成该需求开发工作",
	}, f.freelancer.ID, f.freelancer.Name, f.freelancer.Role)
	if err == nil {
		t.Fatal("bid create must fail when notification write fails")
	}
	bids, _ := f.bidRepo.ListByRequirement(r.ID)
	if len(bids) != 0 {
		t.Fatalf("bid persisted despite notification failure: %d rows", len(bids))
	}
	f.restoreNotifications(t)
	bid := f.submitBid(t, r.ID)
	if c := f.countByType(t, constants.NotificationBidSubmitted); c != 1 {
		t.Fatalf("bid_submitted notifications = %d, want 1", c)
	}
	// The notification must be readable by the publisher right away.
	items, total, err := f.notifSvc.List(f.requester.ID, false, 1, 20)
	if err != nil || total != 1 || len(items) != 1 {
		t.Fatalf("readable notification after recovery: total=%d len=%d err=%v", total, len(items), err)
	}
	if items[0].BizID != bid.ID || items[0].BizType != constants.NotificationBidSubmitted || items[0].OccurredAt.IsZero() {
		t.Fatalf("notification payload mismatch: %+v", items[0])
	}

	// --- 2) Accept bid ---
	f.dropNotifications(t)
	_, err = f.reqSvc.AcceptBid(r.ID, bid.ID, f.requester.ID, f.requester.Name, "installments", f.contractSvc)
	if err == nil {
		t.Fatal("accept must fail when notification write fails")
	}
	staleBid, _ := f.bidRepo.FindByID(bid.ID)
	if staleBid.Status != constants.BidPending {
		t.Fatalf("bid status = %s, want rolled-back pending", staleBid.Status)
	}
	staleReq, _ := f.reqRepo.FindByID(r.ID)
	if staleReq.Status != constants.RequirementOpen {
		t.Fatalf("requirement status = %s, want rolled-back open", staleReq.Status)
	}
	var contractCount int64
	f.db.Model(&model.Contract{}).Count(&contractCount)
	if contractCount != 0 {
		t.Fatalf("contract created despite notification failure: %d", contractCount)
	}
	f.restoreNotifications(t)
	contract := f.accept(t, r.ID, bid.ID)
	if c := f.countByType(t, constants.NotificationBidAccepted); c != 1 {
		t.Fatalf("bid_accepted notifications = %d, want 1", c)
	}
	// The accepted-bid notification must be readable by the freelancer.
	accepted, acceptedTotal, err := f.notifSvc.List(f.freelancer.ID, false, 1, 20)
	if err != nil || acceptedTotal != 1 || accepted[0].BizID != bid.ID {
		t.Fatalf("accepted notification not readable: total=%d err=%v", acceptedTotal, err)
	}

	// --- 3) Sign ---
	f.dropNotifications(t)
	_, err = f.contractSvc.Sign(contract.ID, f.requester.ID, f.requester.Name)
	if err == nil {
		t.Fatal("sign must fail when notification write fails")
	}
	pendingContract, _ := f.contractRepo.FindByID(contract.ID)
	if pendingContract.Status != constants.ContractPendingSignature {
		t.Fatalf("contract status = %s, want rolled-back pending_signature", pendingContract.Status)
	}
	f.restoreNotifications(t)
	signed, err := f.contractSvc.Sign(contract.ID, f.requester.ID, f.requester.Name)
	if err != nil {
		t.Fatalf("retry sign: %v", err)
	}
	if signed.Status != constants.ContractInProgress {
		t.Fatalf("signed status = %s", signed.Status)
	}
	if c := f.countByType(t, constants.NotificationContractSigned); c != 1 {
		t.Fatalf("contract_signed notifications = %d, want 1", c)
	}

	// --- 4) Complete ---
	f.dropNotifications(t)
	_, err = f.contractSvc.Complete(contract.ID, f.requester.ID, f.requester.Name)
	if err == nil {
		t.Fatal("complete must fail when notification write fails")
	}
	inProgressContract, _ := f.contractRepo.FindByID(contract.ID)
	if inProgressContract.Status != constants.ContractInProgress {
		t.Fatalf("contract status = %s, want rolled-back in_progress", inProgressContract.Status)
	}
	f.restoreNotifications(t)
	if _, err := f.contractSvc.Complete(contract.ID, f.requester.ID, f.requester.Name); err != nil {
		t.Fatalf("retry complete: %v", err)
	}
	if c := f.countByType(t, constants.NotificationContractCompleted); c != 1 {
		t.Fatalf("contract_completed notifications = %d, want 1", c)
	}
	// Each event type exists exactly once; DROP/restore cycles removed prior
	// rows, so the latest table holds only the completion notification.
	if f.totalNotifications(t) != 1 {
		t.Fatalf("total notifications = %d, want exactly 1", f.totalNotifications(t))
	}
}

// TestConcurrentReplayKeepsOneNotification races every state transition.
// A single-connection pool serializes transactions (like the MySQL FOR UPDATE
// row lock does in production): exactly one replay wins, and exactly one
// notification of each event exists afterwards.
func TestConcurrentReplayKeepsOneNotification(t *testing.T) {
	f := newAtomicFixture(t)
	sqlDB, err := f.db.DB()
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(1)

	runRace := func(name string, n int, action func(worker int) error) int {
		t.Helper()
		start := make(chan struct{})
		var wg sync.WaitGroup
		var mu sync.Mutex
		var errs []error
		successes := 0
		wg.Add(n)
		for i := 0; i < n; i++ {
			i := i
			go func() {
				defer wg.Done()
				<-start
				if err := action(i); err != nil {
					mu.Lock()
					errs = append(errs, err)
					mu.Unlock()
					return
				}
				mu.Lock()
				successes++
				mu.Unlock()
			}()
		}
		close(start)
		wg.Wait()
		for _, e := range errs {
			var appErr *constants.AppError
			if !errors.As(e, &appErr) && !errors.Is(e, constants.ErrForbidden) {
				t.Fatalf("%s: unexpected internal error from a losing replay: %v", name, e)
			}
		}
		return successes
	}

	// Race: accept the same bid concurrently.
	r := f.publishRequirement(t)
	bid := f.submitBid(t, r.ID)
	const racers = 16
	ok := runRace("accept", racers, func(int) error {
		_, e := f.reqSvc.AcceptBid(r.ID, bid.ID, f.requester.ID, f.requester.Name, "installments", f.contractSvc)
		return e
	})
	if ok != 1 {
		t.Fatalf("accept successes = %d, want 1", ok)
	}
	if c := f.countByType(t, constants.NotificationBidAccepted); c != 1 {
		t.Fatalf("bid_accepted notifications after race = %d, want 1", c)
	}
	var contracts []model.Contract
	f.db.Find(&contracts)
	if len(contracts) != 1 {
		t.Fatalf("contracts after race = %d, want 1", len(contracts))
	}
	contract := contracts[0]

	// Race: sign the same pending contract from both parties concurrently.
	ok = runRace("sign", racers, func(worker int) error {
		if worker%2 == 0 {
			_, e := f.contractSvc.Sign(contract.ID, f.requester.ID, f.requester.Name)
			return e
		}
		_, e := f.contractSvc.Sign(contract.ID, f.freelancer.ID, f.freelancer.Name)
		return e
	})
	if ok != 1 {
		t.Fatalf("sign successes = %d, want 1", ok)
	}
	if c := f.countByType(t, constants.NotificationContractSigned); c != 1 {
		t.Fatalf("contract_signed notifications after race = %d, want 1", c)
	}

	// Race: complete the same in-progress contract concurrently (party A only).
	ok = runRace("complete", racers, func(int) error {
		_, e := f.contractSvc.Complete(contract.ID, f.requester.ID, f.requester.Name)
		return e
	})
	if ok != 1 {
		t.Fatalf("complete successes = %d, want 1", ok)
	}
	if c := f.countByType(t, constants.NotificationContractCompleted); c != 1 {
		t.Fatalf("contract_completed notifications after race = %d, want 1", c)
	}

	finalContract, _ := f.contractRepo.FindByID(contract.ID)
	if finalContract.Status != constants.ContractCompleted {
		t.Fatalf("final contract status = %s, want completed", finalContract.Status)
	}
}

// TestRejectedActionsNeverNotify covers forbidden/invalid requests: they must
// not insert any notification, and retrying a completed transition must not
// add a notification either.
func TestRejectedActionsNeverNotify(t *testing.T) {
	f := newAtomicFixture(t)
	r := f.publishRequirement(t)
	bid := f.submitBid(t, r.ID)
	contract := f.accept(t, r.ID, bid.ID)
	if _, err := f.contractSvc.Sign(contract.ID, f.requester.ID, f.requester.Name); err != nil {
		t.Fatal(err)
	}
	if _, err := f.contractSvc.Complete(contract.ID, f.requester.ID, f.requester.Name); err != nil {
		t.Fatal(err)
	}
	baseline := f.totalNotifications(t) // submitted, accepted, signed, completed = 4

	// Non-party cannot sign; party B cannot complete.
	stranger := &model.User{Username: "stranger", Name: "无关用户", Role: constants.RoleFreelancer}
	if err := repository.NewUserRepository(f.db).Create(stranger); err != nil {
		t.Fatal(err)
	}
	if _, err := f.contractSvc.Sign(contract.ID, stranger.ID, stranger.Name); !errors.Is(err, constants.ErrForbidden) {
		t.Fatalf("stranger sign err = %v, want forbidden", err)
	}
	if _, err := f.contractSvc.Complete(contract.ID, f.freelancer.ID, f.freelancer.Name); !errors.Is(err, constants.ErrForbidden) {
		t.Fatalf("party B complete err = %v, want forbidden", err)
	}
	// Already-terminal transitions are rejected without new notifications.
	if _, err := f.contractSvc.Sign(contract.ID, f.requester.ID, f.requester.Name); err == nil {
		t.Fatal("re-sign completed contract must be rejected")
	}
	if _, err := f.contractSvc.Complete(contract.ID, f.requester.ID, f.requester.Name); err == nil {
		t.Fatal("re-complete must be rejected")
	}
	// Non-publisher cannot accept.
	if _, err := f.reqSvc.AcceptBid(r.ID, bid.ID, f.freelancer.ID, f.freelancer.Name, "installments", f.contractSvc); err == nil {
		t.Fatal("freelancer accept must be rejected")
	}
	// Bidding on a nonexistent requirement is rejected.
	if _, err := f.bidSvc.Create(dto.CreateBidRequest{
		RequirementID: 99999, Amount: 100, DurationDays: 5, Proposal: "无效需求的报价提案不应被受理",
	}, f.freelancer.ID, f.freelancer.Name, f.freelancer.Role); err == nil {
		t.Fatal("bid on missing requirement must be rejected")
	}
	if f.totalNotifications(t) != baseline {
		t.Fatalf("notifications changed after rejected actions: %d -> %d", baseline, f.totalNotifications(t))
	}

	// Business terminal states stay intact.
	finalContract, _ := f.contractRepo.FindByID(contract.ID)
	if finalContract.Status != constants.ContractCompleted {
		t.Fatalf("contract status = %s, want completed", finalContract.Status)
	}
	finalBid, _ := f.bidRepo.FindByID(bid.ID)
	if finalBid.Status != constants.BidAccepted {
		t.Fatalf("bid status = %s, want accepted", finalBid.Status)
	}
}

// TestReadStateSurvivesReplay marks a notification read and replays the same
// event: the row stays unique and read, business state unchanged.
func TestReadStateSurvivesReplay(t *testing.T) {
	f := newAtomicFixture(t)
	r := f.publishRequirement(t)
	bid := f.submitBid(t, r.ID)

	// Direct in-transaction replay (internal retry after recovery): duplicate
	// event key is a no-op, not an error.
	replay := func() error {
		return f.db.Transaction(func(tx *gorm.DB) error {
			return f.notifSvc.NotifyTx(tx, NotifyCommand{
				RecipientID: f.requester.ID,
				BizType:     constants.NotificationBidSubmitted,
				BizID:       bid.ID,
				BizNo:       "报价 #1",
				RefID:       r.ID,
				Title:       "重放",
				Content:     "不应覆盖",
			})
		})
	}
	if err := replay(); err != nil {
		t.Fatalf("first replay: %v", err)
	}
	if err := replay(); err != nil {
		t.Fatalf("second replay: %v", err)
	}
	if c := f.countByType(t, constants.NotificationBidSubmitted); c != 1 {
		t.Fatalf("notifications = %d, want 1", c)
	}

	items, _, err := f.notifSvc.List(f.requester.ID, false, 1, 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Title != "收到新报价" {
		t.Fatalf("replay overwrote original notification: %+v", items)
	}
	if err := f.notifSvc.MarkRead(items[0].ID, f.requester.ID); err != nil {
		t.Fatal(err)
	}
	if err := replay(); err != nil {
		t.Fatalf("replay after read: %v", err)
	}
	items, _, _ = f.notifSvc.List(f.requester.ID, false, 1, 20)
	if len(items) != 1 || !items[0].IsRead {
		t.Fatalf("read state lost after replay: %+v", items)
	}
	unread, _ := f.notifSvc.CountUnread(f.requester.ID)
	if unread != 0 {
		t.Fatalf("unread = %d, want 0", unread)
	}
	// Requirement remains untouched by notification operations.
	reqEntity, _ := f.reqRepo.FindByID(r.ID)
	if reqEntity.Status != constants.RequirementOpen {
		t.Fatalf("requirement status = %s, want open", reqEntity.Status)
	}
}
