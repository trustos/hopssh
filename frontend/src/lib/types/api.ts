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
	role: string;
	createdAt: number;
}

export interface NetworkDetailResponse extends NetworkResponse {
	nodes: NodeResponse[];
}

export interface NetworkMemberResponse {
	id: string;
	userId: string;
	email: string;
	name: string;
	role: string;
	createdAt: number;
}

export interface InviteResponse {
	id: string;
	code: string;
	role: string;
	maxUses: number | null;
	useCount: number;
	expiresAt: number | null;
	createdAt: number;
}

export interface InviteDetailResponse {
	code: string;
	networkId: string;
	networkName: string;
	role: string;
	expiresAt: number | null;
}

export type Connectivity = 'direct' | 'mixed' | 'relayed' | 'idle';

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
	capabilities: string[];
	status: string;
	lastSeenAt: number | null;
	createdAt: number;
	peersDirect?: number;
	peersRelayed?: number;
	peersReportedAt?: number;
	connectivity?: Connectivity;
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

export interface AuditEntryResponse {
	id: string;
	userId: string;
	userEmail: string | null;
	userName: string | null;
	nodeId: string | null;
	nodeHostname: string | null;
	networkId: string | null;
	action: string;
	details: string | null;
	createdAt: number;
}

export interface ErrorResponse {
	error: string;
}
