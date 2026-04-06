import { Terminal } from '@xterm/xterm';
import { FitAddon } from '@xterm/addon-fit';
import { WebLinksAddon } from '@xterm/addon-web-links';

const RESIZE_PREFIX = 1;
const MAX_RECONNECT_ATTEMPTS = 5;
const RECONNECT_DELAYS = [1000, 2000, 4000, 8000, 16000]; // ms, exponential

export interface ShellConnection {
	terminal: Terminal;
	dispose: () => void;
	reconnect: () => void;
}

export interface ShellCallbacks {
	onConnect?: () => void;
	onDisconnect?: () => void;
	onReconnecting?: (attempt: number, maxAttempts: number) => void;
	onReconnected?: () => void;
	onFailed?: () => void;
}

export function connectShell(
	container: HTMLElement,
	networkId: string,
	nodeId: string,
	cbs?: Partial<ShellCallbacks>
): ShellConnection {
	const callbacks: ShellCallbacks = { ...cbs };

	const terminal = new Terminal({
		cursorBlink: true,
		fontSize: 14,
		fontFamily: "'JetBrains Mono', monospace",
		theme: {
			background: '#0a0e14',
			foreground: '#e0e0e0',
			cursor: '#22d3a0',
			selectionBackground: '#22d3a033'
		}
	});

	const fitAddon = new FitAddon();
	terminal.loadAddon(fitAddon);
	terminal.loadAddon(new WebLinksAddon());

	terminal.open(container);
	fitAddon.fit();

	const resizeObserver = new ResizeObserver(() => {
		fitAddon.fit();
	});
	resizeObserver.observe(container);

	let ws: WebSocket | null = null;
	let inputDisposable: { dispose: () => void } | null = null;
	let resizeDisposable: { dispose: () => void } | null = null;
	let disposed = false;
	let reconnecting = false;
	let reconnectTimer: ReturnType<typeof setTimeout> | null = null;

	function buildWsUrl(): string {
		const proto = location.protocol === 'https:' ? 'wss:' : 'ws:';
		return `${proto}//${location.host}/api/networks/${networkId}/nodes/${nodeId}/shell`;
	}

	function attachWebSocket(socket: WebSocket) {
		ws = socket;
		ws.binaryType = 'arraybuffer';

		ws.onopen = () => {
			sendResize(ws!, terminal.rows, terminal.cols);
			if (reconnecting) {
				terminal.write('\r\n\x1b[32m[Reconnected]\x1b[0m\r\n');
				reconnecting = false;
				callbacks.onReconnected?.();
			}
			callbacks.onConnect?.();
		};

		ws.onmessage = (event) => {
			if (event.data instanceof ArrayBuffer) {
				terminal.write(new Uint8Array(event.data));
			} else {
				terminal.write(event.data);
			}
		};

		ws.onclose = (event) => {
			detachInput();
			if (disposed) return;

			if (event.code === 1000) {
				// Clean close (user typed exit).
				terminal.write('\r\n\x1b[90m[Session ended]\x1b[0m\r\n');
				callbacks.onDisconnect?.();
				return;
			}

			// Unexpected close — attempt reconnection (don't fire onDisconnect,
			// fire onReconnecting instead to avoid brief "ended" state flash).
			terminal.write('\r\n\x1b[33m[Connection lost. Reconnecting...]\x1b[0m\r\n');
			attemptReconnect(0);
		};

		ws.onerror = () => {
			// onclose will fire after onerror, handling reconnection there.
		};

		// Terminal input → WebSocket as text frames.
		// PROTOCOL INVARIANT: The Go agent only checks the resize prefix byte
		// (0x01) on BinaryMessage frames. Terminal input is sent as text frames
		// via ws.send(string). This distinction MUST be preserved end-to-end.
		inputDisposable = terminal.onData((data) => {
			if (ws && ws.readyState === WebSocket.OPEN) {
				ws.send(data); // string → text frame
			}
		});

		resizeDisposable = terminal.onResize(({ rows, cols }) => {
			sendResize(ws!, rows, cols);
		});
	}

	function detachInput() {
		inputDisposable?.dispose();
		inputDisposable = null;
		resizeDisposable?.dispose();
		resizeDisposable = null;
	}

	function attemptReconnect(attempt: number) {
		if (disposed || attempt >= MAX_RECONNECT_ATTEMPTS) {
			terminal.write('\r\n\x1b[31m[Connection failed after ' + MAX_RECONNECT_ATTEMPTS + ' attempts]\x1b[0m\r\n');
			callbacks.onFailed?.();
			return;
		}

		reconnecting = true;
		callbacks.onReconnecting?.(attempt + 1, MAX_RECONNECT_ATTEMPTS);

		const delay = RECONNECT_DELAYS[Math.min(attempt, RECONNECT_DELAYS.length - 1)];
		reconnectTimer = setTimeout(() => {
			reconnectTimer = null;
			if (disposed) return;
			try {
				const newWs = new WebSocket(buildWsUrl());
				newWs.onerror = () => {
					attemptReconnect(attempt + 1);
				};
				attachWebSocket(newWs);
			} catch {
				attemptReconnect(attempt + 1);
			}
		}, delay);
	}

	// Initial connection.
	attachWebSocket(new WebSocket(buildWsUrl()));

	return {
		terminal,
		reconnect() {
			if (reconnectTimer) {
				clearTimeout(reconnectTimer);
				reconnectTimer = null;
			}
			if (ws) {
				ws.onclose = null;
				ws.onerror = null;
				ws.close();
			}
			detachInput();
			reconnecting = true;
			attachWebSocket(new WebSocket(buildWsUrl()));
		},
		dispose() {
			disposed = true;
			if (reconnectTimer) {
				clearTimeout(reconnectTimer);
				reconnectTimer = null;
			}
			resizeObserver.disconnect();
			detachInput();
			if (ws) {
				ws.onclose = null;
				ws.close();
			}
			terminal.dispose();
		}
	};
}

function sendResize(ws: WebSocket, rows: number, cols: number) {
	if (ws.readyState !== WebSocket.OPEN) return;
	const buf = new Uint8Array(5);
	buf[0] = RESIZE_PREFIX;
	buf[1] = (rows >> 8) & 0xff;
	buf[2] = rows & 0xff;
	buf[3] = (cols >> 8) & 0xff;
	buf[4] = cols & 0xff;
	ws.send(buf.buffer);
}
