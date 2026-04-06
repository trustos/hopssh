import type {
	AuthResponse,
	UserResponse,
	StatusResponse,
	NetworkResponse,
	NetworkDetailResponse,
	CreateNodeResponse,
	HealthResponse,
	PortForwardResponse
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

	if (body) {
		headers['Content-Type'] = 'application/json';
	}

	const res = await fetch(path, {
		method,
		credentials: 'include', // send HttpOnly session cookie
		headers,
		body: body ? JSON.stringify(body) : undefined
	});

	if (!res.ok) {
		let msg = res.statusText;
		try {
			const err = await res.json();
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

// Sanitize path segments to prevent path traversal.
const e = encodeURIComponent;

// --- Networks ---
export const networks = {
	list: () => request<NetworkResponse[]>('GET', '/api/networks'),
	get: (id: string) => request<NetworkDetailResponse>('GET', `/api/networks/${e(id)}`),
	create: (name: string, dnsDomain?: string) =>
		request<{ id: string; name: string; slug: string; subnet: string; dnsDomain: string }>(
			'POST',
			'/api/networks',
			{ name, dnsDomain: dnsDomain || 'hop' }
		),
	delete: (id: string) => request<void>('DELETE', `/api/networks/${e(id)}`)
};

// --- Nodes ---
export const nodes = {
	list: (networkId: string) =>
		request<import('$lib/types/api').NodeResponse[]>(
			'GET',
			`/api/networks/${e(networkId)}/nodes`
		),
	create: (networkId: string) =>
		request<CreateNodeResponse>('POST', `/api/networks/${e(networkId)}/nodes`, {}),
	delete: (networkId: string, nodeId: string) =>
		request<void>('DELETE', `/api/networks/${e(networkId)}/nodes/${e(nodeId)}`),
	health: (networkId: string, nodeId: string) =>
		request<HealthResponse>('GET', `/api/networks/${e(networkId)}/nodes/${e(nodeId)}/health`)
};

// --- Port Forwards ---
export const portForwards = {
	list: (networkId: string) =>
		request<PortForwardResponse[]>('GET', `/api/networks/${e(networkId)}/port-forwards`),
	start: (networkId: string, nodeId: string, remotePort: number, localPort?: number) =>
		request<PortForwardResponse>(
			'POST',
			`/api/networks/${e(networkId)}/nodes/${e(nodeId)}/port-forwards`,
			{ remotePort, localPort: localPort ?? 0 }
		),
	stop: (networkId: string, fwdId: string) =>
		request<void>('DELETE', `/api/networks/${e(networkId)}/port-forwards/${e(fwdId)}`)
};

// --- DNS Records ---
export const dns = {
	list: (networkId: string) =>
		request<import('$lib/types/api').DNSRecordResponse[]>(
			'GET',
			`/api/networks/${e(networkId)}/dns`
		),
	create: (networkId: string, name: string, nebulaIP: string) =>
		request<{ id: string; name: string; nebulaIP: string }>(
			'POST',
			`/api/networks/${e(networkId)}/dns`,
			{ name, nebulaIP }
		),
	delete: (networkId: string, recordId: string) =>
		request<void>('DELETE', `/api/networks/${e(networkId)}/dns/${e(recordId)}`)
};

// --- Device Flow ---
export const device = {
	verify: (code: string) =>
		request<{ userCode: string; status: string; expiresIn: number }>(
			'GET',
			`/api/device/verify/${e(code)}`
		),
	authorize: (userCode: string, networkId: string) =>
		request<{ status: string }>('POST', '/api/device/authorize', { userCode, networkId })
};
