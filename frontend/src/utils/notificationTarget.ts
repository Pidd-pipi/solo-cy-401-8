import type { Notification } from '../types';

// notificationTarget maps a notification to the page it relates to:
// bid events open the requirement detail, contract events the contract detail.
export function notificationTarget(n: Notification): { path: string; label: string } | null {
  switch (n.bizType) {
    case 'bid_submitted':
    case 'bid_accepted':
      return n.refId
        ? { path: `/requirements/${n.refId}`, label: '查看需求' }
        : null;
    case 'contract_signed':
    case 'contract_completed':
      return n.bizId ? { path: `/contracts/${n.bizId}`, label: '查看合同' } : null;
    default:
      return null;
  }
}
