// Tests of the Firewall tab's logic (src/lib/components/firewall-tab.svelte):
// unsaved changes, versions and labels. Run: npm test (from frontend/).
//
// The project has no component test runner, so this compiles the tab's real
// <script> with Svelte's compiler, against a fake API, and calls its
// functions; the markup is not rendered. If the script changes shape (its
// imports, its props, a name used below), this fails loudly: fix it here.
import assert from 'node:assert/strict';
import { mkdirSync, readFileSync, writeFileSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';
import ts from 'typescript';
import { compileModule } from 'svelte/compiler';

const root = join(dirname(fileURLToPath(import.meta.url)), '..');
const src = readFileSync(join(root, 'src/lib/components/firewall-tab.svelte'), 'utf8');
let script = src.match(/<script lang="ts">([\s\S]*?)<\/script>/)?.[1];
assert.ok(script, 'no <script lang="ts"> in the tab');
script = script.replace(/^\s*import\s[\s\S]*?from\s+'[^']*';/gm, '');
const props = /let \{ networkId, nodes \}[^=]*= \$props\(\);/;
assert.ok(props.test(script), 'the tab no longer reads { networkId, nodes } from $props()');
script = script.replace(props, 'let networkId = "net1"; let nodes = globalThis.__t.nodes;');

const code = `
const fwApi = globalThis.__t.api;
const ApiError = globalThis.__t.ApiError;
const toast = { error: (m) => globalThis.__t.toasts.push(m), success: () => {} };
const onMount = () => {};
${script}
export const tab = {
	load, save, attach, detach, editSet, saveSet, deleteSet, nodeLabel, unknownInSet,
	get: () => ({ preset, rules: $state.snapshot(rules), dirty, editing: $state.snapshot(editing), policy: $state.snapshot(policy) }),
	setPreset: (v) => { preset = v; },
	addRule: (r) => { rules.push(r); },
	pick: (id) => { attachId = id; },
	rename: (name) => { editing.name = name; }
};`;
const js = ts.transpileModule(code, {
	compilerOptions: { target: ts.ScriptTarget.ES2022, module: ts.ModuleKind.ESNext }
}).outputText;
// Inside node_modules, so that the compiled module finds svelte.
const out = join(root, 'node_modules/.cache/firewall-tab-test/tab.mjs');
mkdirSync(dirname(out), { recursive: true });
writeFileSync(out, compileModule(js, { filename: 'firewall-tab.svelte.js', generate: 'client' }).js.code);

class ApiError extends Error {
	constructor(status, message) {
		super(message);
		this.status = status;
	}
}
const clone = (x) => structuredClone(x);

// A fake server: one network at version 1 with web (s1) attached; db (s2) is
// attached to another network and names a device of it.
function fakeServer() {
	const net = { version: 1, preset: 'all', rules: [], canEdit: true, canAttach: true, sets: [] };
	const sets = new Map([
		['s1', { id: 's1', name: 'web', version: 1, rules: [{ proto: 'tcp', port: '80' }], networkIds: ['net1'] }],
		['s2', { id: 's2', name: 'db', version: 1, rules: [{ proto: 'tcp', port: '5432', targetNodeId: 'other' }], networkIds: ['netA'] }]
	]);
	const view = () => ({ ...clone(net), sets: [...sets.values()].filter((s) => s.networkIds.includes('net1')).map((s) => ({ ...clone(s), networkIds: undefined })) });
	const api = {
		get: async () => view(),
		listSets: async () => clone([...sets.values()]),
		update: async (_id, version, preset, rules) => {
			if (version !== net.version) throw new ApiError(412, 'the rules were changed in the meantime: reload and try again');
			Object.assign(net, { version: net.version + 1, preset, rules: clone(rules) });
			return view();
		},
		attach: async (_id, setId) => void sets.get(setId).networkIds.push('net1'),
		detach: async (_id, setId) => {
			const s = sets.get(setId);
			s.networkIds = s.networkIds.filter((n) => n !== 'net1');
		},
		createSet: async () => assert.fail('not used'),
		updateSet: async (setId, version, name, rules) => {
			const s = sets.get(setId);
			if (version !== s.version) throw new ApiError(412, 'the rules were changed in the meantime: reload and try again');
			if ([...sets.values()].some((o) => o.id !== setId && o.name === name.trim()))
				throw new ApiError(409, 'you already have a rule set with that name');
			Object.assign(s, { version: s.version + 1, name: name.trim(), rules: clone(rules) });
		},
		deleteSet: async (setId) => void sets.delete(setId)
	};
	return { net, sets, api };
}

let run = 0;
async function open() {
	const server = fakeServer();
	globalThis.__t = { api: server.api, ApiError, toasts: [], nodes: [{ id: 'box', hostname: 'box' }] };
	globalThis.confirm = () => true;
	const { tab } = await import(`${pathToFileURL(out).href}?run=${++run}`);
	await tab.load();
	return { tab, server, toasts: globalThis.__t.toasts };
}

// A change of the network's own rules, not saved.
function draft(tab) {
	tab.setPreset('tcp');
	tab.addRule({ proto: 'tcp', port: '22' });
}
function assertDraft(tab, what) {
	const s = tab.get();
	assert.equal(s.dirty, true, `${what}: the unsaved change is gone`);
	assert.equal(s.preset, 'tcp', what);
	assert.deepEqual(s.rules, [{ proto: 'tcp', port: '22' }], what);
}

const tests = {
	async 'attach keeps an unsaved change'() {
		const { tab } = await open();
		draft(tab);
		tab.pick('s2');
		await tab.attach();
		assertDraft(tab, 'after attach');
		assert.deepEqual(tab.get().policy.sets.map((s) => s.id), ['s1', 's2']);
	},
	async 'detach keeps an unsaved change'() {
		const { tab } = await open();
		draft(tab);
		await tab.detach({ id: 's1', name: 'web' });
		assertDraft(tab, 'after detach');
		assert.deepEqual(tab.get().policy.sets, []);
	},
	async 'saving and deleting a rule set keep an unsaved change'() {
		const { tab, server } = await open();
		draft(tab);
		tab.editSet({ id: 's1' });
		tab.rename('web servers');
		await tab.saveSet();
		assertDraft(tab, 'after saving a set');
		assert.equal(server.sets.get('s1').name, 'web servers');
		tab.editSet({ id: 's2' });
		await tab.deleteSet();
		assertDraft(tab, 'after deleting a set');
	},
	async 'a kept change is saved against the version it was made on'() {
		const { tab, server, toasts } = await open();
		draft(tab);
		server.net.version = 2; // someone else saved meanwhile
		tab.pick('s2');
		await tab.attach();
		assert.equal(tab.get().policy.version, 1);
		await tab.save();
		assert.equal(server.net.preset, 'all', 'a change made on version 1 overwrote version 2');
		assert.match(toasts.at(-1), /changed these rules first/);
		assert.equal(tab.get().dirty, false, 'after a refused save the tab shows the current rules');
		assert.equal(tab.get().policy.version, 2);
	},
	async 'a refused network save leaves an open rule-set editor alone'() {
		const { tab, server } = await open();
		tab.editSet({ id: 's1' });
		tab.rename('web (unsaved)');
		draft(tab);
		server.net.version = 2;
		await tab.save();
		assert.equal(tab.get().editing?.name, 'web (unsaved)');
	},
	async 'a rule set saved on a stale version reopens as it is now'() {
		const { tab, server, toasts } = await open();
		draft(tab);
		tab.editSet({ id: 's1' });
		Object.assign(server.sets.get('s1'), { version: 2, name: 'web2' });
		tab.rename('mine');
		await tab.saveSet();
		assert.match(toasts.at(-1), /changed this rule set first/);
		assert.deepEqual([tab.get().editing?.version, tab.get().editing?.name], [2, 'web2']);
		assertDraft(tab, 'after a refused set save');
	},
	async 'a name already taken keeps the editor and what was typed'() {
		const { tab, toasts } = await open();
		tab.editSet({ id: 's1' });
		tab.rename('db');
		await tab.saveSet();
		assert.equal(tab.get().editing?.name, 'db');
		assert.match(toasts.at(-1), /already have a rule set with that name/);
	},
	async 'a device of another network is not called deleted'() {
		const { tab } = await open();
		assert.equal(tab.nodeLabel('box', 'Any device', tab.unknownInSet('s2')), 'box');
		assert.equal(tab.nodeLabel('other', 'Any device', tab.unknownInSet('s2')), 'not a device of this network');
		assert.equal(tab.nodeLabel('gone', 'Any device', tab.unknownInSet('s1')), 'deleted device');
		assert.equal(tab.nodeLabel(undefined, 'Any device', tab.unknownInSet('s1')), 'Any device');
	},
	async 'a successful save shows what was saved'() {
		const { tab, server } = await open();
		draft(tab);
		await tab.save();
		assert.equal(tab.get().dirty, false);
		assert.deepEqual([server.net.version, server.net.preset], [2, 'tcp']);
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
