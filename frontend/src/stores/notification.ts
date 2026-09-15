import { defineStore } from 'pinia';
import type { Notification, PageResult } from '../types';
import { notificationApi } from '../api/notification';

interface NotificationState {
  items: Notification[];
  total: number;
  page: number;
  pageSize: number;
  unreadOnly: boolean;
  recent: Notification[];
  unreadCount: number;
}

export const useNotificationStore = defineStore('notification', {
  state: (): NotificationState => ({
    items: [],
    total: 0,
    page: 1,
    pageSize: 20,
    unreadOnly: false,
    recent: [],
    unreadCount: 0
  }),
  getters: {
    hasUnread: (state) => state.unreadCount > 0
  },
  actions: {
    async fetchUnreadCount() {
      const data = await notificationApi.unreadCount();
      this.unreadCount = data.unread;
      return this.unreadCount;
    },
    async fetchRecent(limit = 5) {
      const data = await notificationApi.list({ page: 1, pageSize: limit });
      this.recent = data.items;
      return this.recent;
    },
    async fetchList() {
      const data: PageResult<Notification> = await notificationApi.list({
        unreadOnly: this.unreadOnly,
        page: this.page,
        pageSize: this.pageSize
      });
      this.items = data.items;
      this.total = data.total;
      return data;
    },
    setFilter(unreadOnly: boolean) {
      this.unreadOnly = unreadOnly;
      this.page = 1;
    },
    setPage(page: number) {
      this.page = page;
    },
    // refresh keeps the bell badge and dropdown in sync; it never throws so
    // it is safe to call from navigation guards and timers.
    async refresh() {
      try {
        await Promise.all([this.fetchUnreadCount(), this.fetchRecent()]);
      } catch {
        // unauthenticated or transient network error; ignore.
      }
    },
    async markRead(id: number) {
      const wasUnread =
        this.items.find((n) => n.id === id)?.isRead === false ||
        this.recent.find((n) => n.id === id)?.isRead === false;
      await notificationApi.markRead(id);
      this.patchRead(id);
      if (wasUnread && this.unreadCount > 0) this.unreadCount -= 1;
    },
    async markAllRead() {
      await notificationApi.markAllRead();
      this.items.forEach((n) => (n.isRead = true));
      this.recent.forEach((n) => (n.isRead = true));
      this.unreadCount = 0;
    },
    patchRead(id: number) {
      const item = this.items.find((n) => n.id === id);
      if (item) item.isRead = true;
      const recent = this.recent.find((n) => n.id === id);
      if (recent) recent.isRead = true;
    },
    reset() {
      this.items = [];
      this.total = 0;
      this.page = 1;
      this.unreadOnly = false;
      this.recent = [];
      this.unreadCount = 0;
    }
  }
});
