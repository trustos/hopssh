import { browser } from '$app/environment';

export interface TerminalSession {
	id: string;
	networkId: string;
	nodeId: string;
	hostname: string;
	// Reactive UI state — updated by terminal-pane + the network
	// detail page as events arrive. Drives the banner/tab-dot that
	// tells the user their input isn't getting through.
	nodeOffline: boolean;
	wsState: 'connecting' | 'open' | 'reconnecting' | 'failed';
}

// Module-scoped singleton state — survives across route navigation.
let sessions = $state<TerminalSession[]>([]);
let activeId = $state<string | null>(null);
let paneHeight = $state(300);
let maximized = $state(false);
let collapsed = $state(true); // start collapsed, show when first terminal opens

// Restore pane height from localStorage.
if (browser) {
	const saved = localStorage.getItem('hop_terminal_height');
	if (saved) paneHeight = Math.max(150, Math.min(parseInt(saved), 800));
}

export function getTerminals() {
	return {
		get sessions() { return sessions; },
		get activeId() { return activeId; },
		get paneHeight() { return paneHeight; },
		get maximized() { return maximized; },
		get collapsed() { return collapsed; },
		get hasTerminals() { return sessions.length > 0; },

		open(networkId: string, nodeId: string, hostname: string) {
			const id = `${networkId}:${nodeId}`;
			// Don't duplicate — just focus.
			if (sessions.find(s => s.id === id)) {
				activeId = id;
				collapsed = false;
				return;
			}
			sessions = [
				...sessions,
				{ id, networkId, nodeId, hostname, nodeOffline: false, wsState: 'connecting' }
			];
			activeId = id;
			collapsed = false;
		},

		setNodeOffline(id: string, offline: boolean) {
			const idx = sessions.findIndex(s => s.id === id);
			if (idx === -1) return;
			if (sessions[idx].nodeOffline === offline) return; // no-op
			const copy = [...sessions];
			copy[idx] = { ...copy[idx], nodeOffline: offline };
			sessions = copy;
		},

		setWsState(id: string, state: TerminalSession['wsState']) {
			const idx = sessions.findIndex(s => s.id === id);
			if (idx === -1) return;
			if (sessions[idx].wsState === state) return; // no-op
			const copy = [...sessions];
			copy[idx] = { ...copy[idx], wsState: state };
			sessions = copy;
		},

		close(id: string) {
			sessions = sessions.filter(s => s.id !== id);
			if (activeId === id) {
				activeId = sessions.length > 0 ? sessions[sessions.length - 1].id : null;
			}
			if (sessions.length === 0) {
				collapsed = true;
			}
		},

		focus(id: string) {
			activeId = id;
		},

		setHeight(h: number) {
			paneHeight = Math.max(150, Math.min(h, 800));
			if (browser) {
				localStorage.setItem('hop_terminal_height', String(paneHeight));
			}
		},

		toggleMaximize() {
			maximized = !maximized;
		},

		toggleCollapse() {
			collapsed = !collapsed;
			if (browser) {
				localStorage.setItem('hop_terminal_collapsed', String(collapsed));
			}
		}
	};
}
