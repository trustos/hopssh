// Tests of "Who can reach this device" (src/lib/firewall-reach.ts), the
// list in a node's row of the network page. Run: npm test (from frontend/).
import assert from 'node:assert/strict';
import { mkdirSync, readFileSync, writeFileSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';
import ts from 'typescript';

const root = join(dirname(fileURLToPath(import.meta.url)), '..');
const js = ts.transpileModule(readFileSync(join(root, 'src/lib/firewall-reach.ts'), 'utf8'), {
	compilerOptions: { target: ts.ScriptTarget.ES2022, module: ts.ModuleKind.ESNext }
}).outputText;
const out = join(root, 'node_modules/.cache/firewall-reach-test/reach.mjs');
mkdirSync(dirname(out), { recursive: true });
writeFileSync(out, js);
const { reachRows, reachNote } = await import(pathToFileURL(out).href);

const always = [
	{ origin: 'always', proto: 'tcp', port: '41820', from: 'control' },
	{ origin: 'always', proto: 'tcp', port: '41820', from: 'network' },
	{ origin: 'always', proto: 'icmp', port: 'any', from: 'any' }
];
const names = { tv: 'tv.home' };
const nodeName = (id) => names[id] ?? `device ${id}`;
const rows = (r) => reachRows(r, nodeName).map((x) => [x.proto, x.port, x.from, x.why, x.applies]);
const hopssh = [
	['TCP', '41820', 'The control plane (web terminal, health)', 'hopssh needs it', true],
	['TCP', '41820', 'Every device of the network', 'hopssh needs it', true],
	['ICMP (ping)', '', 'Any device', 'hopssh needs it', true]
];

const tests = {
	'a network nobody chose rules for'() {
		const r = {
			managed: false,
			preset: 'all',
			lines: [...always, { origin: 'preset', proto: 'any', port: 'any', from: 'network' }],
			device: { nodeId: 'nas' }
		};
		assert.deepEqual(rows(r), [...hopssh, ['Any', 'any', 'Every device of the network', 'Preset "Everything"', true]]);
		assert.match(reachNote(r), /Not sent to the devices yet.*what it got when it joined/);
	},
	'rules of the network and of a set, with their sources'() {
		const r = {
			managed: true,
			preset: 'ping',
			lines: [
				...always,
				{ origin: 'network', proto: 'tcp', port: '8096', from: 'node', sourceNodeId: 'tv', description: 'Jellyfin' },
				{ origin: 'network', proto: 'tcp', port: '445', from: 'network' },
				{ origin: 'set', setId: 's1', setName: 'discovery', proto: 'udp', port: '7359', from: 'node', sourceNodeId: 'gone' },
				{ origin: 'set', setId: 's1', setName: 'discovery', proto: 'udp', port: '1900-1901', from: 'network', description: 'SSDP' }
			],
			device: { nodeId: 'nas', expected: 'f', fingerprint: 'f', reportedAt: 1 }
		};
		assert.deepEqual(rows(r), [
			...hopssh,
			['TCP', '8096', 'tv.home', 'This network: Jellyfin', true],
			['TCP', '445', 'Every device of the network', 'A rule of this network', true],
			['UDP', '7359', 'device gone', 'Rule set discovery', true],
			['UDP', '1900-1901', 'Every device of the network', 'Rule set discovery: SSDP', true]
		]);
		assert.equal(reachNote(r), '');
	},
	'a device that blocks incoming keeps only what hopssh needs'() {
		const r = {
			managed: true,
			preset: 'tcp',
			lines: [...always, { origin: 'preset', proto: 'tcp', port: 'any', from: 'network' }, { origin: 'network', proto: 'udp', port: '9', from: 'network' }],
			device: { nodeId: 'nas', blocked: true, reportedAt: 1 }
		};
		assert.deepEqual(
			reachRows(r, nodeName).map((x) => x.applies),
			[true, true, true, false, false]
		);
		assert.equal(reachRows(r, nodeName)[3].why, 'Preset "TCP only"');
		assert.match(reachNote(r), /blocks incoming.*only what hopssh needs.*switched off/);
		// Not managed and blocked: the switch is what counts.
		assert.match(reachNote({ ...r, managed: false }), /blocks incoming/);
	}
};

let failed = 0;
for (const [name, test] of Object.entries(tests)) {
	try {
		await test();
		console.log(`ok    ${name}`);
	} catch (err) {
		failed++;
		console.log(`FAIL  ${name}\n      ${err.message.split('\n').join('\n      ')}`);
	}
}
console.log(failed ? `${failed} of ${Object.keys(tests).length} failed` : `all ${Object.keys(tests).length} passed`);
process.exit(failed ? 1 : 0);
