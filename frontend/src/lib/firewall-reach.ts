import type { NodeFirewallResponse, ReachLine } from './types/api';

// "Who can reach this device", from GET /api/networks/{id}/nodes/{nodeId}/firewall
// (docs/wiki/concepts/firewall-rules.md, phase 3). Tested in
// tests/firewall-reach.test.mjs.

export interface ReachRow {
	proto: string;
	port: string;
	from: string;
	why: string;
	/** False for what the device's own switch "Block incoming" keeps out. */
	applies: boolean;
}

const presetNames: Record<string, string> = {
	all: 'Everything',
	tcp: 'TCP only',
	ping: 'Only what hopssh needs'
};

const protoNames: Record<string, string> = { tcp: 'TCP', udp: 'UDP', icmp: 'ICMP (ping)', any: 'Any' };

function from(l: ReachLine, nodeName: (id: string) => string): string {
	switch (l.from) {
		case 'control':
			return 'The control plane (web terminal, health)';
		case 'network':
			return 'Every device of the network';
		case 'node':
			return nodeName(l.sourceNodeId ?? '');
		default:
			return 'Any device';
	}
}

function why(l: ReachLine, preset: string): string {
	const desc = l.description ? `: ${l.description}` : '';
	switch (l.origin) {
		case 'always':
			return 'hopssh needs it';
		case 'preset':
			return `Preset "${presetNames[preset] ?? preset}"`;
		case 'set':
			return `Rule set ${l.setName ?? ''}${desc}`;
		default:
			return l.description ? `This network${desc}` : 'A rule of this network';
	}
}

/** The rows of the list, in the server's order. nodeName names a source node. */
export function reachRows(r: NodeFirewallResponse, nodeName: (id: string) => string): ReachRow[] {
	return r.lines.map((l) => ({
		proto: protoNames[l.proto] ?? l.proto,
		port: l.proto === 'icmp' ? '' : l.port,
		from: from(l, nodeName),
		why: why(l, r.preset),
		applies: !r.device.blocked || l.origin === 'always'
	}));
}

/** What the list says about itself, or '' when it is what the device runs. */
export function reachNote(r: NodeFirewallResponse): string {
	if (r.device.blocked)
		return 'This device blocks incoming (its own switch): only what hopssh needs gets in. The rest applies again once it is switched off.';
	if (!r.managed)
		return 'Not sent to the devices yet: until the first save or rule set in the Firewall tab, this device runs what it got when it joined. For a network created now that is this list.';
	return '';
}
