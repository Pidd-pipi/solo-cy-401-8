package service

import (
	"fmt"
	"log/slog"

	"gorm.io/gorm"

	"github.com/gigmatch/gigmatch/internal/constants"
	"github.com/gigmatch/gigmatch/internal/model"
	"github.com/gigmatch/gigmatch/internal/repository"
)

// ContractService manages contracts.
type ContractService struct {
	db            *gorm.DB
	contracts     *repository.ContractRepository
	notifications *NotificationService
	logs          *OperationLogService
	logger        *slog.Logger
}

// NewContractService builds a ContractService.
func NewContractService(db *gorm.DB, contracts *repository.ContractRepository, notifications *NotificationService, logs *OperationLogService, logger *slog.Logger) *ContractService {
	return &ContractService{db: db, contracts: contracts, notifications: notifications, logs: logs, logger: logger}
}

// ListByParty returns contracts involving the caller.
func (s *ContractService) ListByParty(userID uint) ([]model.Contract, error) {
	list, err := s.contracts.ListByParty(userID)
	if err != nil {
		return nil, fmt.Errorf("list contracts: %w", err)
	}
	return list, nil
}

// Get loads a contract.
func (s *ContractService) Get(id uint) (*model.Contract, error) {
	return s.contracts.FindByID(id)
}

// CreateFromBidTx builds a contract from an accepted bid inside the caller's
// transaction (shared with the bid-accept state transition).
func (s *ContractService) CreateFromBidTx(tx *gorm.DB, r *model.Requirement, bid *model.Bid, requesterID uint, paymentType string) (*model.Contract, error) {
	if paymentType == "" {
		paymentType = "one_time"
	}
	stages := []model.ContractStage{
		{Name: "项目启动", Amount: bid.Amount * 0.3, Status: "done", DueAt: "签约后3日内"},
		{Name: "中期交付", Amount: bid.Amount * 0.4, Status: "in_progress", DueAt: "工期过半"},
		{Name: "验收结项", Amount: bid.Amount * 0.3, Status: "pending", DueAt: "验收通过后"},
	}
	contract := &model.Contract{
		ContractNo:    fmt.Sprintf("CY-%d-%d", r.ID, bid.ID),
		TotalAmount:   bid.Amount,
		PaymentType:   paymentType,
		Stages:        stages,
		Status:        constants.ContractPendingSignature,
		RequirementID: r.ID,
		PartyAID:      requesterID,
		PartyBID:      bid.BidderID,
	}
	if err := repository.NewContractRepository(tx).Create(contract); err != nil {
		return nil, fmt.Errorf("create contract: %w", err)
	}
	return contract, nil
}

// Sign confirms a contract by either party. The status transition and the
// notification share one transaction with a FOR UPDATE row lock: concurrent
// or retried sign calls serialize, and only the one that performs the
// transition can insert the notification.
func (s *ContractService) Sign(id uint, userID uint, userName string) (contractOut *model.Contract, errOut error) {
	errTx := s.db.Transaction(func(tx *gorm.DB) error {
		txContracts := repository.NewContractRepository(tx)

		c, err := txContracts.FindByIDForUpdate(id)
		if err != nil {
			return err
		}
		if c.PartyAID != userID && c.PartyBID != userID {
			return constants.ErrForbidden
		}
		if c.Status != constants.ContractPendingSignature {
			return constants.NewAppError(constants.CodeConflict, "合同当前不可签署")
		}
		c.Status = constants.ContractInProgress
		if err := txContracts.Update(c); err != nil {
			return fmt.Errorf("sign contract: %w", err)
		}
		// Notify the other party. Only the tx that flipped the status reaches
		// here, so the event can be inserted at most once.
		otherPartyID := c.PartyBID
		if userID == c.PartyBID {
			otherPartyID = c.PartyAID
		}
		if err := s.notifications.NotifyTx(tx, NotifyCommand{
			RecipientID: otherPartyID,
			BizType:     constants.NotificationContractSigned,
			BizID:       c.ID,
			BizNo:       c.ContractNo,
			RefID:       c.RequirementID,
			Title:       "合同已签署",
			Content:     fmt.Sprintf("合同 %s 已由对方签署确认，项目进入执行阶段。", c.ContractNo),
		}); err != nil {
			return err
		}
		contractOut = c
		return nil
	})
	if errTx != nil {
		return nil, errTx
	}
	s.logs.Record(userID, userName, "contract.sign", "contract", contractOut.ID, "签署确认合同")
	return contractOut, nil
}

// Complete confirms completion (requester side). The transition and the
// notification share one transaction with a FOR UPDATE row lock.
func (s *ContractService) Complete(id uint, userID uint, userName string) (contractOut *model.Contract, errOut error) {
	errTx := s.db.Transaction(func(tx *gorm.DB) error {
		txContracts := repository.NewContractRepository(tx)

		c, err := txContracts.FindByIDForUpdate(id)
		if err != nil {
			return err
		}
		if c.PartyAID != userID {
			return constants.ErrForbidden
		}
		if c.Status != constants.ContractInProgress && c.Status != constants.ContractPendingReview {
			return constants.NewAppError(constants.CodeConflict, "合同当前不可完成确认")
		}
		c.Status = constants.ContractCompleted
		for i := range c.Stages {
			c.Stages[i].Status = "done"
		}
		if err := txContracts.Update(c); err != nil {
			return fmt.Errorf("complete contract: %w", err)
		}
		// Only party A (requester) can reach this point; notify the freelancer.
		if err := s.notifications.NotifyTx(tx, NotifyCommand{
			RecipientID: c.PartyBID,
			BizType:     constants.NotificationContractCompleted,
			BizID:       c.ID,
			BizNo:       c.ContractNo,
			RefID:       c.RequirementID,
			Title:       "合同已完成",
			Content:     fmt.Sprintf("合同 %s 已被确认完成，项目已结项。", c.ContractNo),
		}); err != nil {
			return err
		}
		contractOut = c
		return nil
	})
	if errTx != nil {
		return nil, errTx
	}
	s.logs.Record(userID, userName, "contract.complete", "contract", contractOut.ID, "确认合同完成")
	return contractOut, nil
}
