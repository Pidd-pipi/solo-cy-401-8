package service

import (
	"errors"
	"sync"
	"testing"

	"gorm.io/gorm"

	"github.com/gigmatch/gigmatch/internal/constants"
	"github.com/gigmatch/gigmatch/internal/dto"
	"github.com/gigmatch/gigmatch/internal/model"
)

// This suite runs against a REAL InnoDB server over independent TCP
// connections (GIGMATCH_TEST_MYSQL=1). It must never use SQLite, mocks,
// custom fakes, or a single-connection pool. Every failure message carries
// the failing stage and the actual read-back state.

func bidProposalPayload(requirementID uint, amount float64) dto.CreateBidRequest {
	return dto.CreateBidRequest{
		RequirementID: requirementID, Amount: amount, DurationDays: 15,
		Proposal: "我可以按要求完成该需求开发工作并保证质量",
	}
}

// ---------------------------------------------------------------------------
// 1. Notification write failure -> business rollback; recovery -> one retry
// ---------------------------------------------------------------------------

func TestRealMySQL_BidCreateRollsBackWhenNotificationFails(t *testing.T) {
	f := newRealFixture(t)
	r := f.publishRequirement()
	before, _ := f.bidRepo.ListByRequirement(r.ID)

	f.breakNotifications("bid-submit")
	_, err := f.bidSvc.Create(bidProposalPayload(r.ID, 3000),
		f.freelancer.ID, f.freelancer.Name, f.freelancer.Role)
	if err == nil {
		t.Fatal("[stage:bid-submit] expected error when notification table is unavailable, got nil")
	}

	// Read-back: no business terminal state may exist.
	after, _ := f.bidRepo.ListByRequirement(r.ID)
	if len(after) != len(before) {
		t.Fatalf("[stage:bid-submit][readback] bid count = %d, want %d (rollback lost)", len(after), len(before))
	}
	r2, err := f.reqRepo.FindByID(r.ID)
	if err != nil {
		t.Fatalf("[stage:bid-submit][readback] reload requirement: %v", err)
	}
	if r2.Status != constants.RequirementOpen {
		t.Fatalf("[stage:bid-submit][readback] requirement status = %s, want open", r2.Status)
	}

	f.restoreNotifications("bid-submit")
	bid := f.submitBid(r.ID, 3000)
	if c := f.countNotifications(constants.NotificationBidSubmitted); c != 1 {
		t.Fatalf("[stage:bid-submit-retry][readback] bid_submitted notifications = %d, want 1", c)
	}
	// The notification must be readable by the other party with full payload.
	items, total, err := f.notifSvc.List(f.requester.ID, false, 1, 20)
	if err != nil || total != 1 || len(items) != 1 {
		t.Fatalf("[stage:bid-submit-retry][readback] list total=%d len=%d err=%v", total, len(items), err)
	}
	n := items[0]
	if n.RecipientID != f.requester.ID || n.BizID != bid.ID || n.BizType != constants.NotificationBidSubmitted ||
		n.IsRead || n.OccurredAt.IsZero() || n.BizNo == "" {
		t.Fatalf("[stage:bid-submit-retry][readback] notification payload wrong: %+v", n)
	}
}

func TestRealMySQL_AcceptRollsBackWhenNotificationFails(t *testing.T) {
	f := newRealFixture(t)
	r := f.publishRequirement()
	bid := f.submitBid(r.ID, 3000)

	f.breakNotifications("accept")
	_, err := f.reqSvc.AcceptBid(r.ID, bid.ID, f.requester.ID, f.requester.Name, "installments", f.contractSvc)
	if err == nil {
		t.Fatal("[stage:accept] expected error when notification table is unavailable, got nil")
	}

	// Read-back: bid pending, requirement open, no contract.
	staleBid, _ := f.bidRepo.FindByID(bid.ID)
	if staleBid.Status != constants.BidPending {
		t.Fatalf("[stage:accept][readback] bid status = %s, want pending", staleBid.Status)
	}
	staleReq, _ := f.reqRepo.FindByID(r.ID)
	if staleReq.Status != constants.RequirementOpen || staleReq.WinnerID != 0 {
		t.Fatalf("[stage:accept][readback] requirement status=%s winner=%d, want open/0", staleReq.Status, staleReq.WinnerID)
	}
	var contracts int64
	f.db.Model(&model.Contract{}).Count(&contracts)
	if contracts != 0 {
		t.Fatalf("[stage:accept][readback] contracts = %d, want 0", contracts)
	}

	f.restoreNotifications("accept")
	contract := f.accept(r.ID, bid.ID)
	if contract.Status != constants.ContractPendingSignature {
		t.Fatalf("[stage:accept-retry][readback] contract status = %s, want pending_signature", contract.Status)
	}
	if c := f.countNotifications(constants.NotificationBidAccepted); c != 1 {
		t.Fatalf("[stage:accept-retry][readback] bid_accepted notifications = %d, want 1", c)
	}
	items, total, _ := f.notifSvc.List(f.freelancer.ID, false, 1, 20)
	if total != 1 || items[0].BizID != bid.ID {
		t.Fatalf("[stage:accept-retry][readback] freelancer list total=%d items=%+v", total, items)
	}
}

func TestRealMySQL_SignRollsBackWhenNotificationFails(t *testing.T) {
	f := newRealFixture(t)
	r := f.publishRequirement()
	bid := f.submitBid(r.ID, 3000)
	contract := f.accept(r.ID, bid.ID)

	f.breakNotifications("sign")
	_, err := f.contractSvc.Sign(contract.ID, f.requester.ID, f.requester.Name)
	if err == nil {
		t.Fatal("[stage:sign] expected error when notification table is unavailable, got nil")
	}
	pending, _ := f.contractRepo.FindByID(contract.ID)
	if pending.Status != constants.ContractPendingSignature {
		t.Fatalf("[stage:sign][readback] contract status = %s, want pending_signature", pending.Status)
	}

	f.restoreNotifications("sign")
	signed, err := f.contractSvc.Sign(contract.ID, f.requester.ID, f.requester.Name)
	if err != nil {
		t.Fatalf("[stage:sign-retry] retry sign: %v", err)
	}
	if signed.Status != constants.ContractInProgress {
		t.Fatalf("[stage:sign-retry][readback] contract status = %s, want in_progress", signed.Status)
	}
	if c := f.countNotifications(constants.NotificationContractSigned); c != 1 {
		t.Fatalf("[stage:sign-retry][readback] contract_signed notifications = %d, want 1", c)
	}
	// The other party receives exactly one signed notification for this
	// contract (they may also already hold the earlier bid-accepted one).
	items, total, _ := f.notifSvc.List(f.freelancer.ID, false, 1, 20)
	var signedForContract []model.Notification
	for _, it := range items {
		if it.BizType == constants.NotificationContractSigned && it.BizID == contract.ID {
			signedForContract = append(signedForContract, it)
		}
	}
	if total < 1 || len(signedForContract) != 1 {
		t.Fatalf("[stage:sign-retry][readback] other-party signed notifications = %d (total=%d items=%+v)", len(signedForContract), total, items)
	}
	if signedForContract[0].IsRead || signedForContract[0].OccurredAt.IsZero() {
		t.Fatalf("[stage:sign-retry][readback] signed notification payload wrong: %+v", signedForContract[0])
	}
}

func TestRealMySQL_CompleteRollsBackWhenNotificationFails(t *testing.T) {
	f := newRealFixture(t)
	r := f.publishRequirement()
	bid := f.submitBid(r.ID, 3000)
	contract := f.accept(r.ID, bid.ID)
	if _, err := f.contractSvc.Sign(contract.ID, f.requester.ID, f.requester.Name); err != nil {
		t.Fatalf("[stage:complete-setup] sign: %v", err)
	}

	f.breakNotifications("complete")
	_, err := f.contractSvc.Complete(contract.ID, f.requester.ID, f.requester.Name)
	if err == nil {
		t.Fatal("[stage:complete] expected error when notification table is unavailable, got nil")
	}
	inProgress, _ := f.contractRepo.FindByID(contract.ID)
	if inProgress.Status != constants.ContractInProgress {
		t.Fatalf("[stage:complete][readback] contract status = %s, want in_progress", inProgress.Status)
	}
	allDone := true
	for _, st := range inProgress.Stages {
		if st.Status != "done" {
			allDone = false
		}
	}
	if allDone {
		t.Fatal("[stage:complete][readback] stages all marked done despite rollback")
	}

	f.restoreNotifications("complete")
	done, err := f.contractSvc.Complete(contract.ID, f.requester.ID, f.requester.Name)
	if err != nil {
		t.Fatalf("[stage:complete-retry] retry complete: %v", err)
	}
	if done.Status != constants.ContractCompleted {
		t.Fatalf("[stage:complete-retry][readback] contract status = %s, want completed", done.Status)
	}
	if c := f.countNotifications(constants.NotificationContractCompleted); c != 1 {
		t.Fatalf("[stage:complete-retry][readback] contract_completed notifications = %d, want 1", c)
	}
}

// ---------------------------------------------------------------------------
// 2. Real concurrency over independent connections
// ---------------------------------------------------------------------------

type raceOutcome struct {
	success bool
	code    int
	err     error
}

func runConcurrent(t *testing.T, n int, action func(worker int) error) []raceOutcome {
	t.Helper()
	start := make(chan struct{})
	var wg sync.WaitGroup
	outcomes := make([]raceOutcome, n)
	wg.Add(n)
	for i := 0; i < n; i++ {
		i := i
		go func() {
			defer wg.Done()
			<-start
			err := action(i)
			outcomes[i] = raceOutcome{success: err == nil, code: appErrorCode(err), err: err}
		}()
	}
	close(start)
	wg.Wait()
	return outcomes
}

func summarize(outcomes []raceOutcome) (successes, conflicts, other int) {
	for _, o := range outcomes {
		switch {
		case o.success:
			successes++
		case o.code == constants.CodeConflict:
			conflicts++
		default:
			other++
		}
	}
	return
}

func TestRealMySQL_ConcurrentAcceptSameBid(t *testing.T) {
	f := newRealFixture(t)
	r := f.publishRequirement()
	bid := f.submitBid(r.ID, 3000)

	const racers = 10
	outcomes := runConcurrent(t, racers, func(int) error {
		_, e := f.reqSvc.AcceptBid(r.ID, bid.ID, f.requester.ID, f.requester.Name, "installments", f.contractSvc)
		return e
	})
	successes, conflicts, other := summarize(outcomes)
	if successes != 1 {
		t.Fatalf("[stage:accept-race] successes = %d, want 1 (outcomes=%v)", successes, outcomes)
	}
	if conflicts != racers-1 {
		t.Fatalf("[stage:accept-race] conflicts = %d, want %d (outcomes=%v)", conflicts, racers-1, outcomes)
	}
	if other != 0 {
		for _, o := range outcomes {
			if !o.success && o.code != constants.CodeConflict {
				t.Errorf("[stage:accept-race][readback] losing worker got non-conflict error: %v", o.err)
			}
		}
	}
	// Read-back: one contract, one accepted bid, one notification.
	var contracts int64
	f.db.Model(&model.Contract{}).Count(&contracts)
	if contracts != 1 {
		t.Fatalf("[stage:accept-race][readback] contracts = %d, want 1", contracts)
	}
	accepted, _ := f.bidRepo.FindByID(bid.ID)
	if accepted.Status != constants.BidAccepted {
		t.Fatalf("[stage:accept-race][readback] bid status = %s, want accepted", accepted.Status)
	}
	if c := f.countNotifications(constants.NotificationBidAccepted); c != 1 {
		t.Fatalf("[stage:accept-race][readback] bid_accepted notifications = %d, want 1", c)
	}
}

// Two distinct pending bids on one requirement: only one may ever be accepted.
func TestRealMySQL_ConcurrentAcceptDifferentBids(t *testing.T) {
	f := newRealFixture(t)
	r := f.publishRequirement()
	bid1 := f.submitBid(r.ID, 3000)
	other := &model.User{Username: uniqueName("free2"), Name: "自由职业者乙", Role: constants.RoleFreelancer}
	if err := f.userRepo.Create(other); err != nil {
		t.Fatalf("create second freelancer: %v", err)
	}
	bid2, err := f.bidSvc.Create(bidProposalPayload(r.ID, 3200), other.ID, other.Name, other.Role)
	if err != nil {
		t.Fatalf("second bid: %v", err)
	}

	outcomes := runConcurrent(t, 8, func(worker int) error {
		bidID := bid1.ID
		if worker%2 == 0 {
			bidID = bid2.ID
		}
		_, e := f.reqSvc.AcceptBid(r.ID, bidID, f.requester.ID, f.requester.Name, "installments", f.contractSvc)
		return e
	})
	successes, conflicts, otherN := summarize(outcomes)
	if successes != 1 {
		t.Fatalf("[stage:accept-two-bids-race] successes = %d, want 1 (outcomes=%v)", successes, outcomes)
	}
	if conflicts != 7 {
		t.Fatalf("[stage:accept-two-bids-race] conflicts = %d, want 7 (other=%d outcomes=%v)", conflicts, otherN, outcomes)
	}
	var contracts int64
	f.db.Model(&model.Contract{}).Count(&contracts)
	if contracts != 1 {
		t.Fatalf("[stage:accept-two-bids-race][readback] contracts = %d, want 1", contracts)
	}
	if c := f.countNotifications(constants.NotificationBidAccepted); c != 1 {
		t.Fatalf("[stage:accept-two-bids-race][readback] bid_accepted notifications = %d, want 1", c)
	}
	r2, _ := f.reqRepo.FindByID(r.ID)
	if r2.Status != constants.RequirementInProgress || r2.WinnerID == 0 {
		t.Fatalf("[stage:accept-two-bids-race][readback] requirement status=%s winner=%d", r2.Status, r2.WinnerID)
	}
}

func TestRealMySQL_ConcurrentSignBothParties(t *testing.T) {
	f := newRealFixture(t)
	r := f.publishRequirement()
	bid := f.submitBid(r.ID, 3000)
	contract := f.accept(r.ID, bid.ID)

	const racers = 10
	outcomes := runConcurrent(t, racers, func(worker int) error {
		if worker%2 == 0 {
			_, e := f.contractSvc.Sign(contract.ID, f.requester.ID, f.requester.Name)
			return e
		}
		_, e := f.contractSvc.Sign(contract.ID, f.freelancer.ID, f.freelancer.Name)
		return e
	})
	successes, conflicts, other := summarize(outcomes)
	if successes != 1 {
		t.Fatalf("[stage:sign-race] successes = %d, want 1 (outcomes=%v)", successes, outcomes)
	}
	if conflicts != racers-1 {
		t.Fatalf("[stage:sign-race] conflicts = %d, want %d (other=%d outcomes=%v)", conflicts, racers-1, other, outcomes)
	}
	c, _ := f.contractRepo.FindByID(contract.ID)
	if c.Status != constants.ContractInProgress {
		t.Fatalf("[stage:sign-race][readback] contract status = %s, want in_progress", c.Status)
	}
	if n := f.countNotifications(constants.NotificationContractSigned); n != 1 {
		t.Fatalf("[stage:sign-race][readback] contract_signed notifications = %d, want 1", n)
	}
}

func TestRealMySQL_ConcurrentComplete(t *testing.T) {
	f := newRealFixture(t)
	r := f.publishRequirement()
	bid := f.submitBid(r.ID, 3000)
	contract := f.accept(r.ID, bid.ID)
	if _, err := f.contractSvc.Sign(contract.ID, f.freelancer.ID, f.freelancer.Name); err != nil {
		t.Fatalf("[stage:complete-setup] sign: %v", err)
	}

	const racers = 10
	outcomes := runConcurrent(t, racers, func(int) error {
		_, e := f.contractSvc.Complete(contract.ID, f.requester.ID, f.requester.Name)
		return e
	})
	successes, conflicts, other := summarize(outcomes)
	if successes != 1 {
		t.Fatalf("[stage:complete-race] successes = %d, want 1 (outcomes=%v)", successes, outcomes)
	}
	if conflicts != racers-1 {
		t.Fatalf("[stage:complete-race] conflicts = %d, want %d (other=%d outcomes=%v)", conflicts, racers-1, other, outcomes)
	}
	c, _ := f.contractRepo.FindByID(contract.ID)
	if c.Status != constants.ContractCompleted {
		t.Fatalf("[stage:complete-race][readback] contract status = %s, want completed", c.Status)
	}
	if n := f.countNotifications(constants.NotificationContractCompleted); n != 1 {
		t.Fatalf("[stage:complete-race][readback] contract_completed notifications = %d, want 1", n)
	}
}

// ---------------------------------------------------------------------------
// 3. Forbidden / rejected requests never notify; replay keeps read state
// ---------------------------------------------------------------------------

func TestRealMySQL_RejectedRequestsNeverNotify(t *testing.T) {
	f := newRealFixture(t)
	r := f.publishRequirement()
	bid := f.submitBid(r.ID, 3000)
	contract := f.accept(r.ID, bid.ID)
	if _, err := f.contractSvc.Sign(contract.ID, f.requester.ID, f.requester.Name); err != nil {
		t.Fatalf("[stage:setup] sign: %v", err)
	}
	if _, err := f.contractSvc.Complete(contract.ID, f.requester.ID, f.requester.Name); err != nil {
		t.Fatalf("[stage:setup] complete: %v", err)
	}
	baseline := f.countNotifications("") // 4: submitted, accepted, signed, completed
	if baseline != 4 {
		t.Fatalf("[stage:setup][readback] baseline notifications = %d, want 4", baseline)
	}

	stranger := &model.User{Username: uniqueName("stranger"), Name: "无关用户", Role: constants.RoleFreelancer}
	if err := f.userRepo.Create(stranger); err != nil {
		t.Fatalf("create stranger: %v", err)
	}

	rejected := []struct {
		stage string
		fn    func() error
		want  int
	}{
		{"forbidden-sign-stranger", func() error {
			_, e := f.contractSvc.Sign(contract.ID, stranger.ID, stranger.Name)
			return e
		}, constants.CodeForbidden},
		{"forbidden-complete-party-b", func() error {
			_, e := f.contractSvc.Complete(contract.ID, f.freelancer.ID, f.freelancer.Name)
			return e
		}, constants.CodeForbidden},
		{"forbidden-accept-by-freelancer", func() error {
			_, e := f.reqSvc.AcceptBid(r.ID, bid.ID, f.freelancer.ID, f.freelancer.Name, "installments", f.contractSvc)
			return e
		}, constants.CodeForbidden},
		{"conflict-resign-terminal", func() error {
			_, e := f.contractSvc.Sign(contract.ID, f.requester.ID, f.requester.Name)
			return e
		}, constants.CodeConflict},
		{"conflict-recomplete-terminal", func() error {
			_, e := f.contractSvc.Complete(contract.ID, f.requester.ID, f.requester.Name)
			return e
		}, constants.CodeConflict},
		{"conflict-reaccept-bid", func() error {
			_, e := f.reqSvc.AcceptBid(r.ID, bid.ID, f.requester.ID, f.requester.Name, "installments", f.contractSvc)
			return e
		}, constants.CodeConflict},
	}
	for _, c := range rejected {
		err := c.fn()
		if appErrorCode(err) != c.want {
			t.Fatalf("[stage:%s][readback] code = %d, want %d (err=%v)", c.stage, appErrorCode(err), c.want, err)
		}
	}

	if got := f.countNotifications(""); got != baseline {
		t.Fatalf("[stage:rejected][readback] notifications = %d, want unchanged %d", got, baseline)
	}
	// Business terminal states stay intact.
	finalContract, _ := f.contractRepo.FindByID(contract.ID)
	if finalContract.Status != constants.ContractCompleted {
		t.Fatalf("[stage:rejected][readback] contract status = %s, want completed", finalContract.Status)
	}
	finalBid, _ := f.bidRepo.FindByID(bid.ID)
	if finalBid.Status != constants.BidAccepted {
		t.Fatalf("[stage:rejected][readback] bid status = %s, want accepted", finalBid.Status)
	}
}

func TestRealMySQL_ReadStateSurvivesReplay(t *testing.T) {
	f := newRealFixture(t)
	r := f.publishRequirement()
	bid := f.submitBid(r.ID, 3000)

	items, total, err := f.notifSvc.List(f.requester.ID, false, 1, 20)
	if err != nil || total != 1 {
		t.Fatalf("[stage:replay-setup][readback] total=%d err=%v", total, err)
	}
	n := items[0]
	if err := f.notifSvc.MarkRead(n.ID, f.requester.ID); err != nil {
		t.Fatalf("[stage:replay-mark-read] %v", err)
	}

	replay := func() error {
		return f.db.Transaction(func(tx *gorm.DB) error {
			return f.notifSvc.NotifyTx(tx, NotifyCommand{
				RecipientID: f.requester.ID,
				BizType:     constants.NotificationBidSubmitted,
				BizID:       bid.ID,
				BizNo:       "重放编号",
				RefID:       r.ID,
				Title:       "重放标题",
				Content:     "不应覆盖原记录",
			})
		})
	}
	for i := 0; i < 5; i++ {
		if err := replay(); err != nil {
			t.Fatalf("[stage:replay-%d] %v", i, err)
		}
	}

	if c := f.countNotifications(constants.NotificationBidSubmitted); c != 1 {
		t.Fatalf("[stage:replay][readback] notifications = %d, want 1", c)
	}
	items, total, _ = f.notifSvc.List(f.requester.ID, false, 1, 20)
	if total != 1 || len(items) != 1 {
		t.Fatalf("[stage:replay][readback] total=%d len=%d, want 1/1", total, len(items))
	}
	if items[0].ID != n.ID || !items[0].IsRead {
		t.Fatalf("[stage:replay][readback] row changed: id=%d read=%v, want id=%d read=true", items[0].ID, items[0].IsRead, n.ID)
	}
	if items[0].Title == "重放标题" {
		t.Fatal("[stage:replay][readback] replay overwrote the original notification content")
	}
	if unread, _ := f.notifSvc.CountUnread(f.requester.ID); unread != 0 {
		t.Fatalf("[stage:replay][readback] unread = %d, want 0", unread)
	}
	// Non-owner still cannot touch the read state.
	if err := f.notifSvc.MarkRead(n.ID, f.freelancer.ID); !errors.Is(err, constants.ErrForbidden) {
		t.Fatalf("[stage:replay][readback] cross-user markread err = %v, want forbidden", err)
	}
	if _, err := f.notifSvc.MarkAllRead(f.freelancer.ID); err != nil {
		t.Fatalf("[stage:replay] mark all read for other user: %v", err)
	}
	items, _, _ = f.notifSvc.List(f.requester.ID, false, 1, 20)
	if !items[0].IsRead {
		t.Fatal("[stage:replay][readback] read state lost after other user's mark-all")
	}
	reqEntity, _ := f.reqRepo.FindByID(r.ID)
	if reqEntity.Status != constants.RequirementOpen {
		t.Fatalf("[stage:replay][readback] requirement status = %s, want open", reqEntity.Status)
	}
}
