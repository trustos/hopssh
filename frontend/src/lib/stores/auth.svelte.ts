import { auth as authApi } from '$lib/api/client';
import type { UserResponse } from '$lib/types/api';

let user = $state<UserResponse | null>(null);
let loading = $state(true);
let initialized = $state(false);

export function getAuth() {
	return {
		get user() {
			return user;
		},
		get loading() {
			return loading;
		},
		get isAuthenticated() {
			return user !== null;
		},

		async init() {
			if (initialized) return;
			loading = true;
			try {
				user = await authApi.me();
			} catch {
				user = null;
			} finally {
				loading = false;
				initialized = true;
			}
		},

		async login(email: string, password: string) {
			const res = await authApi.login(email, password);
			localStorage.setItem('hop_token', res.token);
			user = { id: res.id, email: res.email, name: res.name };
		},

		async register(email: string, name: string, password: string) {
			const res = await authApi.register(email, name, password);
			localStorage.setItem('hop_token', res.token);
			user = { id: res.id, email: res.email, name: res.name };
		},

		async logout() {
			try {
				await authApi.logout();
			} catch {
				/* ignore */
			}
			localStorage.removeItem('hop_token');
			user = null;
		}
	};
}
