<template>
  <div class="notif-item" :class="{ unread: !notification.isRead }" @click="onOpen">
    <span class="dot" :class="{ on: !notification.isRead }" />
    <div class="body">
      <div class="line">
        <el-tag size="small" :type="tagType" effect="light">{{ typeLabel }}</el-tag>
        <b class="title">{{ notification.title }}</b>
        <el-button
          v-if="!notification.isRead"
          link
          type="primary"
          size="small"
          class="read-btn"
          @click.stop="onRead"
        >标为已读</el-button>
      </div>
      <p class="content">{{ notification.content }}</p>
      <div class="meta muted">
        <span>{{ notification.bizNo }}</span>
        <span>{{ formatDateTime(notification.occurredAt) }}</span>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue';
import type { Notification } from '../../types';
import { NotificationTypeLabel, NotificationTagType } from '../../types/enums';
import { formatDateTime } from '../../utils/formatCurrency';

const props = defineProps<{ notification: Notification }>();
const emit = defineEmits<{
  (e: 'read', id: number): void;
  (e: 'open', notification: Notification): void;
}>();

const typeLabel = computed(() => NotificationTypeLabel[props.notification.bizType] || '系统通知');
const tagType = computed(() => NotificationTagType[props.notification.bizType] || 'info');

function onRead() {
  emit('read', props.notification.id);
}

function onOpen() {
  emit('open', props.notification);
}
</script>

<style scoped>
.notif-item { display: flex; gap: 10px; padding: 12px 14px; border-bottom: 1px solid #ebeef5; cursor: pointer; transition: background 0.15s; }
.notif-item:hover { background: #f5f7fa; }
.notif-item.unread { background: #f0f7ff; }
.notif-item.unread:hover { background: #e6f1fe; }
.dot { width: 8px; display: flex; align-items: flex-start; padding-top: 8px; }
.dot::before { content: ''; width: 8px; height: 8px; border-radius: 50%; }
.dot.on::before { background: #f56c6c; }
.body { flex: 1; min-width: 0; }
.line { display: flex; align-items: center; gap: 8px; }
.title { font-size: 14px; }
.read-btn { margin-left: auto; }
.content { margin: 6px 0 4px; font-size: 13px; color: #606266; line-height: 1.5; }
.meta { display: flex; justify-content: space-between; gap: 12px; }
</style>
