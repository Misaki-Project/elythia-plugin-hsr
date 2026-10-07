<!--
SPDX-FileCopyrightText: mk-go project
SPDX-License-Identifier: AGPL-3.0-only
-->
<template>
<component :is="embedded ? 'div' : PageWithHeader">
	<div class="_spacer" :class="$style.root">
	<div class="_panel _gaps_m" :class="$style.panel">
		<h2 :class="$style.heading"><i class="ti ti-trophy"></i> スターレイル実績ランキング</h2>
		<div>本人確認済みで参加を有効にしたローカルUIDを、実績数の多い順に表示します。同数は同順位です。取得時点のキャッシュであり、リアルタイムのゲーム情報ではありません。</div>
		<div v-if="message" role="status">{{ message }}</div>
		<ol :class="$style.records">
			<li v-for="entry in entries" :key="entry.accountId" :class="$style.record">
				<b :aria-label="`${entry.rank}位`">{{ ['🥇', '🥈', '🥉'][entry.rank - 1] ?? entry.rank }}</b>
				<div><a :href="`/users/${encodeURIComponent(entry.userId)}`">{{ entry.nickname }}</a><div v-if="entry.uid && showUid" :class="$style.detail">UID {{ entry.uid }}</div></div>
				<div :class="$style.score"><b>実績 {{ entry.value.toLocaleString() }}</b><div :class="$style.detail">取得 <MkTime :time="entry.fetchedAt"/></div></div>
			</li>
		</ol>
		<p v-if="!busy && !message && !entries.length">集計対象の記録はありません。</p>
		<MkSwitch v-model="showUid"><template #label>公開UIDを表示する</template></MkSwitch>
		<div class="_buttons">
			<MkButton :disabled="busy || offset === 0" @click="load(Math.max(0, offset - 50))">前へ</MkButton>
			<MkButton :disabled="busy || !hasMore" @click="load(offset + 50)">次へ</MkButton>
			<MkButton :disabled="busy" @click="load(offset)">再読込み</MkButton>
		</div>
	</div>
	<div :class="$style.credit"><a href="https://enka.network/" target="_blank" rel="noopener noreferrer">Powered by Enka.Network</a></div>
	</div>
</component>
</template>
<script lang="ts" setup>
import { ref, onMounted } from 'vue';
import { MkButton, MkSwitch, MkTime, PageWithHeader, definePage } from '@/plugin-api.js';
import { api } from './api.js';
import type { RankingEntry, RankingResponse } from './api.js';

const props = withDefaults(defineProps<{ embedded?: boolean }>(), { embedded: false });
if (!props.embedded) definePage(() => ({ title: 'スターレイル実績ランキング', icon: 'ti ti-trophy' }));
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
<style lang="scss" module>
.root { --MI_SPACER-w: 800px; }
.panel { padding: 16px; }
.heading { margin: 0; font-size: 1em; }
.records { margin: 0; padding: 0; list-style: none; }
.record { display: grid; grid-template-columns: 28px minmax(0, 1fr) auto; gap: 8px; align-items: center; padding: 12px 0; border-bottom: 1px solid var(--MI_THEME-divider); overflow-wrap: anywhere; }
.score { text-align: right; font-variant-numeric: tabular-nums; }
.detail { margin-top: 4px; font-size: 85%; color: var(--MI_THEME-fgTransparentWeak); }
.credit { margin-top: 12px; text-align: right; font-size: 75%; color: var(--MI_THEME-fgTransparentWeak); }
@media (max-width: 480px) { .record { grid-template-columns: 24px minmax(0, 1fr); } .score { grid-column: 2; text-align: left; } }
</style>
