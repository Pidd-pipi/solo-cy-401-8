package service

import (
	"fmt"
	"log/slog"

	"gorm.io/gorm"

	"github.com/gigmatch/gigmatch/internal/constants"
	"github.com/gigmatch/gigmatch/internal/dto"
	"github.com/gigmatch/gigmatch/internal/model"
	"github.com/gigmatch/gigmatch/internal/repository"
)

// BidService manages bids.
type BidService struct {
	db            *gorm.DB
	bids          *repository.BidRepository
	requirements  *repository.RequirementRepository
	notifications *NotificationService
	logs          *OperationLogService
	logger        *slog.Logger
}

// NewBidService builds a BidService.
func NewBidService(db *gorm.DB, bids *repository.BidRepository, requirements *repository.RequirementRepository, notifications *NotificationService, logs *OperationLogService, logger *slog.Logger) *BidService {
	return &BidService{db: db, bids: bids, requirements: requirements, notifications: notifications, logs: logs, logger: logger}
}

// ListByRequirement returns bids of a requirement.
func (s *BidService) ListByRequirement(requirementID uint) ([]model.Bid, error) {
	list, err := s.bids.ListByRequirement(requirementID)
	if err != nil {
		return nil, fmt.Errorf("list bids: %w", err)
	}
	return list, nil
}

// Create submits a bid (freelancer role). The bid row and the notification are
// written in one transaction: if notification persistence fails the bid is
// rolled back, so a successful submission always has a readable notification.
func (s *BidService) Create(req dto.CreateBidRequest, userID uint, userName string, role string) (bidOut *model.Bid, errOut error) {
	if role == constants.RoleRequester {
		return nil, constants.NewAppError(constants.CodeForbidden, "需求方不能提交报价")
	}

	attachments := req.Attachments
	if attachments == nil {
		attachments = []string{}
	}

	errTx := s.db.Transaction(func(tx *gorm.DB) error {
		txReqs := repository.NewRequirementRepository(tx)
		txBids := repository.NewBidRepository(tx)

		// Re-read the requirement inside the tx to observe the latest status;
		// a FOR UPDATE row lock also serializes repeated submissions.
		r, err := txReqs.FindByIDForUpdate(req.RequirementID)
		if err != nil {
			return err
		}
		if r.PublisherID == userID {
			return constants.NewAppError(constants.CodeForbidden, "不能对自己发布的需求报价")
		}
		if r.Status != constants.RequirementOpen && r.Status != constants.RequirementBidding {
			return constants.NewAppError(constants.CodeConflict, "该需求当前不可报价")
		}
		bid := &model.Bid{
			RequirementID: req.RequirementID,
			BidderID:      userID,
			Amount:        req.Amount,
			DurationDays:  req.DurationDays,
			Proposal:      req.Proposal,
			Attachments:   attachments,
			Status:        constants.BidPending,
		}
		if err := txBids.Create(bid); err != nil {
			return fmt.Errorf("create bid: %w", err)
		}
		// Notify the requirement publisher; self-bidding is rejected above so
		// the recipient is always the other party. Failure rolls the bid back.
		if err := s.notifications.NotifyTx(tx, NotifyCommand{
			RecipientID: r.PublisherID,
			BizType:     constants.NotificationBidSubmitted,
			BizID:       bid.ID,
			BizNo:       fmt.Sprintf("报价 #%d", bid.ID),
			RefID:       r.ID,
			Title:       "收到新报价",
			Content:     fmt.Sprintf("你发布的需求「%s」收到一条新报价：金额 %.2f 元，工期 %d 天。", r.Title, bid.Amount, bid.DurationDays),
		}); err != nil {
			return err
		}
		bidOut = bid
		return nil
	})
	if errTx != nil {
		return nil, errTx
	}
	s.logs.Record(userID, userName, "bid.create", "bid", bidOut.ID, fmt.Sprintf("提交报价 %.2f", bidOut.Amount))
	return bidOut, nil
}

// Withdraw withdraws a pending bid owned by the caller.
func (s *BidService) Withdraw(id uint, userID uint, userName string) (*model.Bid, error) {
	bid, err := s.bids.FindByID(id)
	if err != nil {
		return nil, err
	}
	if bid.BidderID != userID {
		return nil, constants.ErrForbidden
	}
	if bid.Status != constants.BidPending {
		return nil, constants.NewAppError(constants.CodeConflict, "仅待审报价可撤回")
	}
	bid.Status = constants.BidWithdrawn
	if err := s.bids.Update(bid); err != nil {
		return nil, fmt.Errorf("withdraw bid: %w", err)
	}
	s.logs.Record(userID, userName, "bid.withdraw", "bid", bid.ID, "撤回报价")
	return bid, nil
}
