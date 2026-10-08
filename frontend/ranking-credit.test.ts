/*
 * SPDX-FileCopyrightText: mk-go project
 * SPDX-License-Identifier: AGPL-3.0-only
 */
import { afterEach, expect, it, vi } from 'vitest';
import { cleanup, fireEvent, render, screen } from '@testing-library/vue';
import Rankings from './Rankings.vue';
import ProfileCard from './ProfileCard.vue';

const mocks = vi.hoisted(() => ({ api: vi.fn(), definePage: vi.fn() }));
vi.mock('./api.js', async (importOriginal) => ({ ...await importOriginal<object>(), api: mocks.api }));
vi.mock('@/plugin-api.js', async () => {
	const { defineComponent, h } = await import('vue');
	const wrapper = defineComponent({ setup(_props, { slots }) { return () => h('div', slots.default?.()); } });
	return {
		PageWithHeader: wrapper, MkSelect: wrapper, definePage: mocks.definePage,
		MkButton: defineComponent({ props: { disabled: Boolean }, setup(props, { slots }) { return () => h('button', { disabled: props.disabled }, slots.default?.()); } }),
		MkTime: defineComponent({ props: ['time'], setup(props) { return () => h('time', props.time); } }),
		MkSwitch: defineComponent({ props: { modelValue: Boolean }, emits: ['update:modelValue'], setup(props, { slots, emit }) { return () => h('button', { role: 'switch', 'aria-checked': props.modelValue, onClick: () => emit('update:modelValue', !props.modelValue) }, slots.label?.()); } }),
	};
});
afterEach(() => { cleanup(); mocks.api.mockReset(); mocks.definePage.mockClear(); });

it('ランキングはFolderなしで埋め込み、UIDは既定非表示で出典を表示する', async () => {
	mocks.api.mockResolvedValue({ entries: [{ rank: 1, userId: 'u1', accountId: 'a1', nickname: 'Trailblazer', uid: '800000001', value: 123, fetchedAt: '2026-10-07T00:00:00Z' }], hasMore: false });
	render(Rankings, { props: { embedded: true } });
	await screen.findByText('Trailblazer');
	expect(screen.getByRole('heading', { name: /スターレイル実績ランキング/ })).toBeTruthy();
	expect(screen.queryByText(/UID 800000001/)).toBeNull();
	expect(screen.getByRole('link', { name: 'Powered by Enka.Network' }).getAttribute('href')).toBe('https://enka.network/');
	expect(mocks.definePage).not.toHaveBeenCalled();
	await fireEvent.click(screen.getByRole('switch'));
	expect(screen.getByText('UID 800000001')).toBeTruthy();
});
it('プロフィールでは公開UIDの表示を変えず、右側の出典を常に残す', async () => {
	mocks.api.mockResolvedValue({ profiles: [{ linked: true, nickname: 'Trailblazer', uid: '800000001', level: 70, worldLevel: 6, characters: [] }] });
	render(ProfileCard, { props: { ctx: { user: { id: 'u1' } } } });
	await fireEvent.click(await screen.findByRole('button', { name: /崩壊:スターレイル/ }));
	const credit = screen.getByRole('link', { name: 'Powered by Enka.Network' });
	expect(screen.queryByText('UID 800000001')).toBeNull();
	await fireEvent.click(screen.getByRole('switch'));
	expect(credit.previousElementSibling?.textContent).toBe('UID 800000001');
	expect(credit.className).toContain('credit');
	expect(credit.getAttribute('rel')).toBe('noopener noreferrer');
});
