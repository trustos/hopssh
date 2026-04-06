import { Terminal } from '@xterm/xterm';
import { FitAddon } from '@xterm/addon-fit';
import { WebLinksAddon } from '@xterm/addon-web-links';

const RESIZE_PREFIX = 1;

export interface ShellConnection {
	terminal: Terminal;
	dispose: () => void;
}

export function connectShell(
	container: HTMLElement,
	networkId: string,
	nodeId: string,
	onConnect?: () => void
): ShellConnection {
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

	// WebSocket URL — works with both dev proxy and production
	const proto = location.protocol === 'https:' ? 'wss:' : 'ws:';
	const wsUrl = `${proto}//${location.host}/api/networks/${networkId}/nodes/${nodeId}/shell`;

	const ws = new WebSocket(wsUrl);
	ws.binaryType = 'arraybuffer';

	ws.onopen = () => {
		sendResize(ws, terminal.rows, terminal.cols);
		onConnect?.();
	};

	ws.onmessage = (event) => {
		if (event.data instanceof ArrayBuffer) {
			terminal.write(new Uint8Array(event.data));
		} else {
			terminal.write(event.data);
		}
	};

	ws.onclose = () => {
		terminal.write('\r\n\x1b[90m[Connection closed]\x1b[0m\r\n');
	};

	ws.onerror = () => {
		terminal.write('\r\n\x1b[31m[Connection error]\x1b[0m\r\n');
	};

	// Terminal input → WebSocket as text frames.
	// PROTOCOL INVARIANT: The Go agent (cmd/agent/main.go) only checks the
	// resize prefix byte (0x01) on BinaryMessage frames. Terminal input is sent
	// as text frames via ws.send(string), which the agent writes directly to
	// the PTY. Resize commands use ws.send(ArrayBuffer) which becomes a
	// BinaryMessage. This text/binary distinction MUST be preserved end-to-end
	// through the control plane's WebSocket proxy (internal/api/proxy.go).
	const inputDisposable = terminal.onData((data) => {
		if (ws.readyState === WebSocket.OPEN) {
			ws.send(data); // string → text frame
		}
	});

	// Resize handling
	const resizeDisposable = terminal.onResize(({ rows, cols }) => {
		sendResize(ws, rows, cols);
	});

	const resizeObserver = new ResizeObserver(() => {
		fitAddon.fit();
	});
	resizeObserver.observe(container);

	return {
		terminal,
		dispose() {
			resizeObserver.disconnect();
			inputDisposable.dispose();
			resizeDisposable.dispose();
			ws.close();
			terminal.dispose();
		}
	};
}

function sendResize(ws: WebSocket, rows: number, cols: number) {
	if (ws.readyState !== WebSocket.OPEN) return;
	// Protocol: prefix byte 0x01 + rows (2 bytes BE) + cols (2 bytes BE)
	const buf = new Uint8Array(5);
	buf[0] = RESIZE_PREFIX;
	buf[1] = (rows >> 8) & 0xff;
	buf[2] = rows & 0xff;
	buf[3] = (cols >> 8) & 0xff;
	buf[4] = cols & 0xff;
	ws.send(buf.buffer);
}
