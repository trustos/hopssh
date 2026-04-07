// Types mirroring Go API response DTOs (internal/api/types.go)

export interface AuthResponse {
	id: string;
	email: string;
	name: string;
	token: string;
}

export interface UserResponse {
	id: string;
	email: string;
	name: string;
}

export interface StatusResponse {
	hasUsers: boolean;
}

export interface NetworkResponse {
	id: string;
	name: string;
	slug: string;
	subnet: string;
	nodeCount: number;
	lighthousePort: number | null;
	dnsDomain: string;
	createdAt: number;
}

export interface NetworkDetailResponse extends NetworkResponse {
	nodes: NodeResponse[];
}

export interface NodeResponse {
	id: string;
	networkId: string;
	hostname: string;
	os: string;
	arch: string;
	nebulaIP: string;
	agentRealIP: string | null;
	nodeType: string;
	exposedPorts: string | null;
	dnsName: string | null;
	status: string;
	lastSeenAt: number | null;
	createdAt: number;
}

export interface CreateNodeResponse {
	nodeId: string;
	enrollmentToken: string;
	endpoint: string;
	nebulaIP: string;
}

export interface HealthResponse {
	status: string;
	hostname: string;
	os: string;
	arch: string;
	uptime: string;
}

export interface PortForwardResponse {
	id: string;
	networkId: string;
	nodeId: string;
	remotePort: number;
	localPort: number;
	localAddr: string;
	createdAt: number;
}

export interface DNSRecordResponse {
	id: string;
	networkId: string;
	name: string;
	nebulaIP: string;
	createdAt: number;
}

export interface DeviceCodeResponse {
	deviceCode: string;
	userCode: string;
	verificationURI: string;
	expiresIn: number;
	interval: number;
}

export interface ErrorResponse {
	error: string;
}
