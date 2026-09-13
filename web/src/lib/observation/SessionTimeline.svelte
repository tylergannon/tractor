<script lang="ts">
	import type { JSONObject, ProjectionState } from '../sessionstate/index.js';
	import type { RunObservation } from './index.js';
	import MessageRow from './MessageRow.svelte';
	import { usageOf, usageText } from './index.js';

	let { state, revision, observation, turn }: { state: Readonly<ProjectionState>; revision: number; observation: RunObservation; turn: string } = $props();
	const text = (value: unknown) => typeof value === 'string' ? value : JSON.stringify(value, null, 2);
	const sessions = $derived.by(() => {
		revision;
		return [...new Set([...Object.keys(state.message), ...Object.keys(state.pending), ...Object.keys(state.active)])].map((sessionID) => {
			const messages = state.message[sessionID] ?? [];
			const pending = new Map((state.pending[sessionID] ?? []).map((input) => [input.id, input]));
			return { sessionID, messages, pending, unmatchedPending: [...pending.values()].filter((input) => !messages.some((message) => message.id === input.id)) };
		});
	});
	// The session's running total is the run's, keyed by the turn's placement
	// session -- the same map the Go store folds `session.usage.updated` into.
	// An absent entry zero-fills through usageOf.
	const total = $derived.by(() => {
		revision;
		return usageOf(observation.run.usage?.[observation.invocations.get(turn)?.session ?? '']);
	});
</script>

<div class="timeline">
	{#each sessions as session (session.sessionID)}
		<section class="native-session" data-session-id={session.sessionID}>
			<header class="session-header"><code>{session.sessionID}</code><span>{state.active[session.sessionID] ?? 'idle'}</span></header>
			<p class="session-total">{usageText(total)}</p>
			{#each session.messages as message (message.id)}
				<MessageRow {message} pending={session.pending.get(message.id)} revision={observation.messageRevision(turn, message.id)} />
			{/each}
		{#each session.unmatchedPending as input (input.id)}
			<article data-message-id={input.id}>
				<header><strong>{input.type}</strong><span>{input.delivery ?? 'pending'}</span></header>
				{#if input.payload?.text !== undefined}<p class="prose">{input.payload.text}</p>{:else}<pre>{text(input.payload ?? input)}</pre>{/if}
			</article>
		{/each}
		{#if session.messages.length === 0 && session.unmatchedPending.length === 0}<p class="empty">Waiting for transcript events…</p>{/if}
		</section>
	{/each}
	{#if sessions.length === 0}<p class="empty">Waiting for transcript events…</p>{/if}
</div>

<style>
	.timeline { display: grid; gap: .8rem; }
	.native-session { display: grid; gap: .8rem; }
	.session-header code { text-transform: none; }
	article { border: 1px solid #dfe3ea; border-radius: .65rem; padding: .85rem 1rem; background: #fff; }
	header { display: flex; justify-content: space-between; gap: 1rem; color: #556070; font-size: .82rem; text-transform: capitalize; }
	.prose { white-space: pre-wrap; margin: .65rem 0; }
	pre { overflow-x: auto; white-space: pre-wrap; font: .82rem/1.45 ui-monospace, monospace; }
	.empty { color: #697386; }
	.session-total { color: #556070; font-size: .78rem; }
</style>
