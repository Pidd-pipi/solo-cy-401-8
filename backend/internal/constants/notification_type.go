package constants

// In-app notification business types. The value is stored in
// notifications.biz_type and consumed by the frontend.
const (
	NotificationBidSubmitted      = "bid_submitted"      // 报价提交
	NotificationBidAccepted       = "bid_accepted"       // 报价被采纳
	NotificationContractSigned    = "contract_signed"    // 合同签署
	NotificationContractCompleted = "contract_completed" // 合同完成
)
