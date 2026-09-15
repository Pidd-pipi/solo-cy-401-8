<template>
  <div class="page notif-page">
    <div class="page-title">
      <h2>通知中心</h2>
      <div class="actions">
        <el-radio-group v-model="filter" size="small" @change="onFilterChange">
          <el-radio-button :value="false">全部</el-radio-button>
          <el-radio-button :value="true">未读</el-radio-button>
        </el-radio-group>
        <el-button type="primary" plain size="small" :disabled="!notifStore.hasUnread" @click="markAll">
          全部标记已读
        </el-button>
      </div>
    </div>

    <el-card shadow="never" class="notif-card">
      <NotificationItem
        v-for="n in notifStore.items"
        :key="n.id"
        :notification="n"
        @read="markOne"
        @open="openNotification"
      />
      <el-empty v-if="notifStore.items.length === 0" :description="filter ? '暂无未读通知' : '暂无通知'" />
      <div v-if="notifStore.total > notifStore.pageSize" class="pager">
        <el-pagination
          layout="prev, pager, next, total"
          :total="notifStore.total"
          :page-size="notifStore.pageSize"
          :current-page="notifStore.page"
          @current-change="onPageChange"
        />
      </div>
    </el-card>
  </div>
</template>

<script setup lang="ts">
import { onMounted, ref } from 'vue';
import { useRouter } from 'vue-router';
import { useNotificationStore } from '../stores/notification';
import NotificationItem from '../components/common/NotificationItem.vue';
import type { Notification } from '../types';
import { notificationTarget } from '../utils/notificationTarget';

const router = useRouter();
const notifStore = useNotificationStore();
const filter = ref(false);

async function load() {
  await notifStore.fetchList();
}

function onFilterChange(value: boolean) {
  notifStore.setFilter(value);
  void load();
}

function onPageChange(page: number) {
  notifStore.setPage(page);
  void load();
}

async function markOne(id: number) {
  await notifStore.markRead(id);
  if (filter.value) {
    void load();
  }
}

async function markAll() {
  await notifStore.markAllRead();
  if (filter.value) {
    void load();
  }
}

function openNotification(n: Notification) {
  if (!n.isRead) {
    void notifStore.markRead(n.id).then(() => {
      if (filter.value) void load();
    });
  }
  const target = notificationTarget(n);
  if (target) void router.push(target.path);
}

onMounted(() => void load());
</script>

<style scoped>
.notif-page { max-width: 860px; margin: 0 auto; }
.actions { display: flex; gap: 12px; align-items: center; }
.notif-card { padding: 0; }
.notif-card :deep(.el-card__body) { padding: 0; }
.pager { display: flex; justify-content: center; padding: 16px 0; }
</style>
