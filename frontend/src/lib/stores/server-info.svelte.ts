import { browser } from '$app/environment';

// Version of the control plane currently serving this dashboard.
// Fetched once from GET /version on layout mount and cached for the
// session. Used in the sidebar footer (display) and in the network
// detail page's Nodes table (compares against each node's
// `agentVersion` to highlight drift).
let current = $state<string | null>(null);
let fetched = $state(false);

export function getServerInfo() {
	return {
		get current() {
			return current;
		},

		async init() {
			if (!browser || fetched) return;
			fetched = true;
			try {
				const resp = await fetch('/version');
				if (!resp.ok) return;
				const body = (await resp.json()) as { current?: string };
				if (body.current) current = body.current;
			} catch {
				// Network error → leave null; sidebar renders em-dash.
			}
		}
	};
}
