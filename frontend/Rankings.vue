<!--
SPDX-FileCopyrightText: mk-go project
SPDX-License-Identifier: AGPL-3.0-only
-->
<template>
<MkFolder :defaultOpen="true">
	<template #label>スターレイル実績ランキング</template>
	<div class="_gaps_m">
		<div>本人確認済みで参加を有効にしたローカルUIDを、実績数の多い順に表示します。同数は同順位です。取得時点のキャッシュであり、リアルタイムのゲーム情報ではありません。</div>
		<div v-if="message" role="status">{{ message }}</div>
		<div v-for="entry in entries" :key="entry.accountId" class="_gaps_s">
			<a :href="`/users/${encodeURIComponent(entry.userId)}`">{{ entry.rank }}位 {{ entry.nickname }}</a>
			<span>実績 {{ entry.value }}</span>
			<span v-if="entry.uid && showUid">UID {{ entry.uid }}</span>
			<span>取得日時 {{ entry.fetchedAt }}</span>
		</div>
		<MkSwitch v-model="showUid"><template #label>公開UIDを表示する</template></MkSwitch>
		<div class="_buttons">
			<MkButton :disabled="busy || offset === 0" @click="load(Math.max(0, offset - 50))">前へ</MkButton>
			<MkButton :disabled="busy || !hasMore" @click="load(offset + 50)">次へ</MkButton>
			<MkButton :disabled="busy" @click="load(offset)">再読込み</MkButton>
		</div>
	</div>
</MkFolder>
</template>
<script lang="ts" setup>
import { ref, onMounted } from 'vue';
import { MkFolder, MkButton, MkSwitch } from '@/plugin-api.js';
import { api } from './api.js';
import type { RankingEntry, RankingResponse } from './api.js';

const entries = ref<RankingEntry[]>([]);
const offset = ref(0);
const hasMore = ref(false);
const busy = ref(false);
const message = ref('');
const showUid = ref(false);
async function load(next: number): Promise<void> {
	busy.value = true;
	message.value = '';
	try {
		const result = await api<RankingResponse>('rankings', { metric: 'achievements', limit: 50, offset: next });
		entries.value = result.entries;
		offset.value = next;
		hasMore.value = result.hasMore;
	} catch { entries.value = []; hasMore.value = false; message.value = 'ランキングを取得できませんでした。'; }
	finally { busy.value = false; }
}
onMounted(async () => { await load(0); });
</script>
