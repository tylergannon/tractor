<script lang="ts">
	import RunViewer from '#lib/observation/RunViewer.svelte';
	import { usageText, type RunSnapshot } from '#lib/observation/index.js';
	import { scopeUsage } from './usage.remote';

	let { data }: { data: { snapshot: RunSnapshot } } = $props();
	// The run's whole usage, streamed from Go: the root scope is every
	// session, and every session nested under it.
	const usage = $derived(scopeUsage({ runID: data.snapshot.run.id, scope: '' }));
</script>

<p class="run-usage">Run usage · {usage.ready ? usageText(usage.current) : 'connecting'}</p>
<RunViewer snapshot={data.snapshot} />

<style>
	.run-usage { margin: 0 0 1rem; color: #556070; font-size: .8rem; }
</style>
