/*
 * SPDX-FileCopyrightText: mk-go project
 * SPDX-License-Identifier: AGPL-3.0-only
 */
import { afterEach, expect, it, vi } from 'vitest';
import { cleanup, fireEvent, render, screen } from '@testing-library/vue';
import SettingsSection from './SettingsSection.vue';

const mocks = vi.hoisted(() => ({ api: vi.fn() }));
vi.mock('./api.js', () => ({ api: mocks.api }));
vi.mock('@/plugin-api.js', async () => {
	const { defineComponent, h } = await import('vue');
	const wrapper = defineComponent({ setup(_props, { slots }) { return () => h('div', slots.default?.()); } });
	return {
		MkFolder: wrapper,
		MkInput: wrapper,
		MkButton: defineComponent({ setup(_props, { slots }) { return () => h('button', slots.default?.()); } }),
		MkSwitch: defineComponent({
			props: { modelValue: Boolean, disabled: Boolean },
			emits: ['update:modelValue'],
			setup(props, { slots, emit }) {
				return () => h('button', { role: 'switch', 'aria-checked': props.modelValue, disabled: props.disabled, onClick: () => emit('update:modelValue', !props.modelValue) }, slots.label?.());
			},
		}),
	};
});
afterEach(() => { cleanup(); vi.restoreAllMocks(); mocks.api.mockReset(); });

it('UID公開は既定オフで、公開・参加設定をまとめて保存する', async () => {
	mocks.api.mockImplementation(async (path: string, params: unknown) => {
		if (path === 'me/preferences') return { publishUid: false, publishSignature: true, rankingEnabled: true };
		if (path === 'me') return { uids: [], limit: 1, pending: null, unverifiedUid: '800000000' };
		if (path === 'me/preferences/update') return params;
		throw new Error(path);
	});
	render(SettingsSection);
	await vi.waitFor(() => expect(screen.getByRole('switch', { name: 'UIDを公開する' }).getAttribute('aria-checked')).toBe('false'));
	expect(screen.getAllByRole('switch')).toHaveLength(3);
	expect(await screen.findByText(/以前のUID 800000000 は本人確認待ち/)).toBeTruthy();
	await fireEvent.click(screen.getByRole('switch', { name: 'UIDを公開する' }));
	await fireEvent.click(screen.getByRole('button', { name: '公開・参加設定を保存' }));
	expect(mocks.api).toHaveBeenCalledWith('me/preferences/update', { publishUid: true, publishSignature: true, rankingEnabled: true });
});

it('確認送信の直後から60秒間は再送信できない', async () => {
	const now = Date.now();
	vi.spyOn(Date, 'now').mockReturnValue(now);
	mocks.api.mockImplementation(async (path: string) => {
		if (path === 'me/preferences') return { publishUid: false, publishSignature: true, rankingEnabled: true };
		if (path === 'me') return { uids: [], limit: 1, pending: { uid: '800000000', code: '12+345', expiresAt: new Date(now + 600_000).toISOString(), nextCheckAt: new Date(now).toISOString(), attempts: 0 }, unverifiedUid: '' };
		if (path === 'me/verify') return { verified: false };
		throw new Error(path);
	});
	render(SettingsSection);
	const button = await screen.findByRole('button', { name: '認証する' });
	await vi.waitFor(() => expect((button as HTMLButtonElement).disabled).toBe(false));
	await fireEvent.click(button);
	await screen.findByText(/60 秒後に再確認できます/);
	expect((button as HTMLButtonElement).disabled).toBe(true);
	await fireEvent.click(button);
	expect(mocks.api.mock.calls.filter(([path]) => path === 'me/verify')).toHaveLength(1);
});
