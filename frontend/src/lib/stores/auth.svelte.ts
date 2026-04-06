import { auth as authApi } from '$lib/api/client';
import type { UserResponse } from '$lib/types/api';

// Module-scoped singleton state. All calls to getAuth() share these.
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
				// The session cookie is sent automatically via credentials: 'include'.
				// If the cookie is valid, we get the user. If not, we get a 401.
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
			// The server sets an HttpOnly session cookie. No localStorage needed.
			user = { id: res.id, email: res.email, name: res.name };
		},

		async register(email: string, name: string, password: string) {
			const res = await authApi.register(email, name, password);
			user = { id: res.id, email: res.email, name: res.name };
		},

		async logout() {
			try {
				await authApi.logout();
			} catch {
				/* ignore */
			}
			user = null;
			initialized = false;
		}
	};
}
