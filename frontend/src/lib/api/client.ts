import type {
	AuthResponse,
	UserResponse,
	StatusResponse,
	NetworkResponse,
	NetworkDetailResponse,
	NodeResponse,
	CreateNodeResponse,
	HealthResponse,
	PortForwardResponse,
	ErrorResponse
} from '$lib/types/api';

export class ApiError extends Error {
	constructor(
		public status: number,
		message: string
	) {
		super(message);
		this.name = 'ApiError';
	}
}

async function request<T>(method: string, path: string, body?: unknown): Promise<T> {
	const headers: Record<string, string> = {};

	const token = typeof localStorage !== 'undefined' ? localStorage.getItem('hop_token') : null;
	if (token) {
		headers['Authorization'] = `Bearer ${token}`;
	}

	if (body) {
		headers['Content-Type'] = 'application/json';
	}

	const res = await fetch(path, {
		method,
		credentials: 'include',
		headers,
		body: body ? JSON.stringify(body) : undefined
	});

	if (!res.ok) {
		let msg = res.statusText;
		try {
			const err: ErrorResponse = await res.json();
			msg = err.error || msg;
		} catch {
			msg = await res.text().catch(() => msg);
		}
		throw new ApiError(res.status, msg);
	}

	if (res.status === 204) return undefined as T;
	return res.json();
}

// --- Auth ---
export const auth = {
	status: () => request<StatusResponse>('GET', '/api/auth/status'),
	register: (email: string, name: string, password: string) =>
		request<AuthResponse>('POST', '/api/auth/register', { email, name, password }),
	login: (email: string, password: string) =>
		request<AuthResponse>('POST', '/api/auth/login', { email, password }),
	logout: () => request<void>('POST', '/api/auth/logout'),
	me: () => request<UserResponse>('GET', '/api/auth/me')
};

// --- Networks ---
export const networks = {
	list: () => request<NetworkResponse[]>('GET', '/api/networks'),
	get: (id: string) => request<NetworkDetailResponse>('GET', `/api/networks/${id}`),
	create: (name: string) => request<NetworkResponse>('POST', '/api/networks', { name }),
	delete: (id: string) => request<void>('DELETE', `/api/networks/${id}`)
};

// --- Nodes ---
export const nodes = {
	list: (networkId: string) => request<NodeResponse[]>('GET', `/api/networks/${networkId}/nodes`),
	create: (networkId: string) =>
		request<CreateNodeResponse>('POST', `/api/networks/${networkId}/nodes`, {}),
	delete: (networkId: string, nodeId: string) =>
		request<void>('DELETE', `/api/networks/${networkId}/nodes/${nodeId}`),
	health: (networkId: string, nodeId: string) =>
		request<HealthResponse>('GET', `/api/networks/${networkId}/nodes/${nodeId}/health`)
};

// --- Port Forwards ---
export const portForwards = {
	list: (networkId: string) =>
		request<PortForwardResponse[]>('GET', `/api/networks/${networkId}/port-forwards`),
	start: (networkId: string, nodeId: string, remotePort: number, localPort?: number) =>
		request<PortForwardResponse>(
			'POST',
			`/api/networks/${networkId}/nodes/${nodeId}/port-forwards`,
			{ remotePort, localPort: localPort ?? 0 }
		),
	stop: (networkId: string, fwdId: string) =>
		request<void>('DELETE', `/api/networks/${networkId}/port-forwards/${fwdId}`)
};

// --- Device Flow ---
export const device = {
	verify: (code: string) =>
		request<{ userCode: string; status: string; expiresIn: number }>(
			'GET',
			`/api/device/verify/${code}`
		),
	authorize: (userCode: string, networkId: string) =>
		request<{ status: string }>('POST', '/api/device/authorize', { userCode, networkId })
};
