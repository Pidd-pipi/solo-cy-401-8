<template>
  <el-container class="layout">
    <el-header class="header">
      <div class="brand" @click="$router.push('/requirements')">
        <span class="brand-mark">CY</span>
        <span class="brand-name">自由职业者撮合平台</span>
      </div>
      <el-menu mode="horizontal" :default-active="active" router class="menu" :ellipsis="false">
        <el-menu-item index="/requirements">需求大厅</el-menu-item>
        <el-menu-item index="/dashboard">我的工作台</el-menu-item>
      </el-menu>
      <div class="user-box">
        <template v-if="store.isAuthenticated">
          <el-popover
            v-model:visible="popoverVisible"
            placement="bottom-end"
            :width="420"
            trigger="click"
            popper-class="notif-popover"
            @show="onPopoverShow"
          >
            <template #reference>
              <el-badge :value="notifStore.unreadCount" :hidden="!notifStore.hasUnread" :max="99" class="bell-badge">
                <el-button circle size="large" :icon="Bell" class="bell-btn" aria-label="通知中心" />
              </el-badge>
            </template>
            <div class="notif-panel">
              <div class="notif-head">
                <b>通知中心</b>
                <el-button link type="primary" size="small" :disabled="!notifStore.hasUnread" @click="markAll">
                  全部已读
                </el-button>
              </div>
              <div class="notif-list">
                <NotificationItem
                  v-for="n in notifStore.recent"
                  :key="n.id"
                  :notification="n"
                  @read="markOne"
                  @open="openNotification"
                />
                <el-empty v-if="notifStore.recent.length === 0" description="暂无通知" :image-size="60" />
              </div>
              <div class="notif-foot">
                <el-button link type="primary" size="small" @click="goCenter">查看全部通知</el-button>
              </div>
            </div>
          </el-popover>
          <el-dropdown @command="onCommand">
            <span class="user-name">
              <UserAvatar :name="store.user?.name" :avatar="store.user?.avatar" :size="28" />
              <span class="muted">{{ store.user?.name }}</span>
            </span>
            <template #dropdown>
              <el-dropdown-menu>
                <el-dropdown-item command="notifications">通知中心</el-dropdown-item>
                <el-dropdown-item command="profile">个人资料</el-dropdown-item>
                <el-dropdown-item command="logout">退出登录</el-dropdown-item>
              </el-dropdown-menu>
            </template>
          </el-dropdown>
        </template>
        <el-button v-else size="small" @click="$router.push('/login')">登录</el-button>
      </div>
    </el-header>
    <el-main class="main">
      <router-view />
    </el-main>
  </el-container>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue';
import { useRoute, useRouter } from 'vue-router';
import { Bell } from '@element-plus/icons-vue';
import { useUserStore } from '../stores/user';
import { useNotificationStore } from '../stores/notification';
import UserAvatar from '../components/common/UserAvatar.vue';
import NotificationItem from '../components/common/NotificationItem.vue';
import type { Notification } from '../types';
import { notificationTarget } from '../utils/notificationTarget';

const route = useRoute();
const router = useRouter();
const store = useUserStore();
const notifStore = useNotificationStore();

const active = computed(() => {
  const path = route.path;
  if (path.startsWith('/requirements')) return '/requirements';
  if (path.startsWith('/dashboard')) return '/dashboard';
  if (path.startsWith('/notifications')) return '/dashboard';
  if (path.startsWith('/contracts')) return '/dashboard';
  if (path.startsWith('/profile')) return '/dashboard';
  return '';
});

let timer: number | undefined;
const popoverVisible = ref(false);

function onPopoverShow() {
  void notifStore.fetchRecent();
}

async function markOne(id: number) {
  await notifStore.markRead(id);
}

async function markAll() {
  await notifStore.markAllRead();
}

function goCenter() {
  popoverVisible.value = false;
  void router.push('/notifications');
}

function openNotification(n: Notification) {
  if (!n.isRead) void notifStore.markRead(n.id);
  popoverVisible.value = false;
  const target = notificationTarget(n);
  if (target) void router.push(target.path);
}

function onCommand(cmd: string) {
  if (cmd === 'logout') {
    store.logout();
    notifStore.reset();
    router.push('/login');
  } else if (cmd === 'profile') {
    router.push(`/profile/${store.user?.id}`);
  } else if (cmd === 'notifications') {
    router.push('/notifications');
  }
}

onMounted(() => {
  if (store.isAuthenticated) void notifStore.refresh();
  timer = window.setInterval(() => {
    if (store.isAuthenticated) void notifStore.fetchUnreadCount();
  }, 60000);
});

// Refresh the badge after navigating between business pages (e.g. the user
// just submitted a bid or signed a contract).
watch(
  () => route.fullPath,
  () => {
    if (store.isAuthenticated) void notifStore.fetchUnreadCount();
  }
);

onBeforeUnmount(() => {
  if (timer) window.clearInterval(timer);
});
</script>

<style scoped>
.layout { min-height: 100vh; }
.header { display: flex; align-items: center; background: #fff; border-bottom: 1px solid #e4e7ed; padding: 0 24px; gap: 24px; }
.brand { display: flex; align-items: center; gap: 10px; cursor: pointer; }
.brand-mark { display: inline-grid; place-items: center; width: 34px; height: 34px; background: #243b53; color: #fff; font-weight: 800; border-radius: 8px; }
.brand-name { font-weight: 600; color: #243b53; }
.menu { flex: 1; border-bottom: none; }
.user-box { display: flex; align-items: center; gap: 16px; }
.user-name { display: flex; align-items: center; gap: 8px; cursor: pointer; }
.bell-btn { border: none; background: transparent; }
.main { background: #f5f7fa; padding: 0; }
</style>

<style>
.notif-popover { padding: 0 !important; }
.notif-panel { width: 100%; }
.notif-head { display: flex; justify-content: space-between; align-items: center; padding: 10px 14px; border-bottom: 1px solid #ebeef5; }
.notif-list { max-height: 380px; overflow-y: auto; }
.notif-list .notif-item:last-child { border-bottom: none; }
.notif-foot { text-align: center; padding: 8px 0; border-top: 1px solid #ebeef5; }
</style>
