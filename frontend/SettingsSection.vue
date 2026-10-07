<!--
SPDX-FileCopyrightText: mk-go project
SPDX-License-Identifier: AGPL-3.0-only
-->
<template>
<MkFolder>
	<template #label>崩壊:スターレイル</template>
	<template #suffix>{{ uids.length }} / {{ limit }} 件</template>
	<div class="_gaps_m">
		<MkSwitch v-model="preferences.publishUid" :disabled="busy"><template #label>UIDを公開する</template></MkSwitch>
		<MkSwitch v-model="preferences.publishSignature" :disabled="busy"><template #label>ステータスメッセージを公開する</template></MkSwitch>
		<MkSwitch v-model="preferences.rankingEnabled" :disabled="busy"><template #label>サーバー内の実績ランキングに参加する</template></MkSwitch>
		<div>公開設定はすべての連携UIDに適用します。ゲーム内・Enkaの公開設定は変更しません。相手サーバーのキャッシュは更新まで残る場合があります。ランキング参加中は実績数とニックネームが公開されます。</div>
		<MkButton :disabled="busy" @click="savePreferences">公開・参加設定を保存</MkButton>
		<a href="/plugin/hsr/rankings">スターレイルの実績ランキング</a>
		<div v-for="uid in uids" :key="uid" class="_buttons">
			<span>UID {{ uid }}</span><MkButton :disabled="busy" @click="unlink(uid)">連携を解除</MkButton>
		</div>
		<div v-if="unverifiedUid">以前のUID {{ unverifiedUid }} は本人確認待ちです。再認証するまで公開・ランキングに使用しません。</div>
		<MkInput v-model="draft" :disabled="busy" placeholder="800000000"><template #label>連携するスターレイルUID</template><template #caption>本人確認が完了したUIDだけを公開します。リモート利用者は連携上限に含みません。</template></MkInput>
		<MkButton primary :disabled="busy || uids.length >= limit" @click="begin">紐づけコードを発行</MkButton>
		<div v-if="pending" class="_gaps_s">
			<div>確認対象: {{ pending.uid }}</div>
			<MkInput :modelValue="pending.code" readonly><template #label>紐づけコード</template></MkInput>
			<div>ゲーム内のステータスメッセージにコード全体を追加して保存し、一度ログアウトしてから「認証する」を押してください。コードは10分間有効です。</div>
			<div role="timer">残り {{ remaining }} 秒</div>
			<div v-if="waitSeconds > 0">反映待ちです。{{ waitSeconds }} 秒後に再確認できます。</div>
			<MkButton primary :disabled="busy || remaining === 0 || waitSeconds > 0 || pending.attempts >= 10" @click="verify">認証する</MkButton>
		</div>
		<div v-if="message" role="status">{{ message }}</div>
	</div>
</MkFolder>
</template>

<script lang="ts" setup>
import { ref, computed, onMounted, onUnmounted } from 'vue';
import { MkInput, MkButton, MkFolder, MkSwitch } from '@/plugin-api.js';
import { api } from './api.js';
import type { LinkChallenge, MeResponse, VerifyResponse, Preferences } from './api.js';

const uids = ref<string[]>([]);
const preferences = ref<Preferences>({ publishUid: false, publishSignature: true, rankingEnabled: true });
const limit = ref(1);
const draft = ref('');
const unverifiedUid = ref('');
const pending = ref<LinkChallenge | null>(null);
const busy = ref(false);
const message = ref('');
const now = ref(Date.now());
const verifyNotBefore = ref(0);
const remaining = computed(() => Math.max(0, Math.ceil(((pending.value ? Date.parse(pending.value.expiresAt) : 0) - now.value) / 1000)));
const waitSeconds = computed(() => Math.max(0, Math.ceil((Math.max(verifyNotBefore.value, pending.value ? Date.parse(pending.value.nextCheckAt) : 0) - now.value) / 1000)));
let timer: number | undefined;

async function reload(): Promise<void> {
	preferences.value = await api<Preferences>('me/preferences');
	const me = await api<MeResponse>('me');
	uids.value = me.uids;
	limit.value = me.limit;
	pending.value = me.pending;
	unverifiedUid.value = me.unverifiedUid;
}

async function run(action: () => Promise<void>): Promise<void> {
	busy.value = true;
	message.value = '';
	try { await action(); } catch (err) {
		message.value = (err as { message?: string } | null)?.message ?? '処理に失敗しました。しばらく待って再確認してください。';
		try { await reload(); } catch { /* Preserve the current UI on temporary network failure. */ }
	} finally { busy.value = false; }
}

async function savePreferences(): Promise<void> {
	await run(async () => { preferences.value = await api<Preferences>('me/preferences/update', { ...preferences.value }); message.value = '公開・参加設定を保存しました。'; });
}

async function begin(): Promise<void> {
	await run(async () => { pending.value = await api<LinkChallenge>('me/begin', { uid: draft.value.trim() }); now.value = Date.now(); });
}

async function verify(): Promise<void> {
	if (pending.value == null || busy.value || waitSeconds.value > 0 || remaining.value === 0 || pending.value.attempts >= 10) return;
	const code = pending.value.code;
	now.value = Date.now();
	verifyNotBefore.value = now.value + 60_000;
	await run(async () => {
		const result = await api<VerifyResponse>('me/verify', { code });
		await reload();
		message.value = result.verified ? '連携が完了しました。ゲーム内のコードは削除できます。' : 'コードをまだ確認できません。保存・ログアウトを確認し、反映を待ってください。';
	});
}

async function unlink(uid: string): Promise<void> {
	await run(async () => { await api('me/unlink', { uid }); await reload(); message.value = '連携を解除しました。'; });
}

onMounted(async () => { timer = window.setInterval(() => { now.value = Date.now(); }, 1000); await run(reload); });
onUnmounted(() => { if (timer != null) window.clearInterval(timer); });
</script>
