import { getData, postData } from './request';
import type { Notification, PageResult } from '../types';

export interface ListNotificationsParams {
  unreadOnly?: boolean;
  page?: number;
  pageSize?: number;
}

export const notificationApi = {
  list: (params: ListNotificationsParams = {}) =>
    getData<PageResult<Notification>>('/notifications', {
      unreadOnly: params.unreadOnly ? 1 : undefined,
      page: params.page ?? 1,
      page_size: params.pageSize ?? 20
    }),
  unreadCount: () => getData<{ unread: number }>('/notifications/unread-count'),
  markRead: (id: number) => postData<{ id: number; isRead: boolean }>(`/notifications/${id}/read`),
  markAllRead: () => postData<{ updated: number }>('/notifications/read-all')
};
