import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import test from 'node:test'
import { RunObservation, foldProvenance, usageOf, usageText, type LifecycleRecord, type RunSnapshot } from './index.ts'
import type { Snapshot } from '../sessionstate/index.ts'

const projection = (): Snapshot => ({
	state: { info: { ses: { id: 'ses', title: 'start' } }, family: { ses: ['ses'] }, active: {}, message: { ses: [] }, pending: {}, permission: {}, form: {} }
})
const snapshot = (title = 'start'): RunSnapshot => {
	const value = projection(); value.state.info.ses.title = title
	return { run: { id: 'run', name: 'Run', status: 'running', sessions: { ses: { name: 'agent', adapter: 'codex', model: 'm', scope: 'lap' } } }, scopes: { 'loop.1': { name: 'loop.1', status: 'running' } }, invocations: { turn: { scope: 'lap', session: 'ses', turn: 'turn', snapshot: value, provenance: {} } } }
}

test('replacement snapshot resets machines and stale connection callbacks cannot mutate them', () => {
	const observation = new RunObservation(snapshot('first'))
	const oldConnection = observation.beginConnection()
	observation.apply({ type: 'snapshot', data: snapshot('replacement') }, oldConnection)
	const currentConnection = observation.beginConnection()
	assert.equal(observation.apply({ type: 'event', data: { scope: 'lap', session: 'ses', turn: 'turn', event: { id: 'old', created: 1, type: 'session.renamed', data: { sessionID: 'ses', title: 'stale' } } } }, oldConnection), false)
	observation.apply({ type: 'event', data: { scope: 'lap', session: 'ses', turn: 'turn', event: { id: 'new', created: 2, type: 'session.permissions.updated', data: { sessionID: 'ses', permissions: ['read'] } } } }, currentConnection)
	assert.equal(observation.state('turn')?.info.ses.title, 'replacement')
	assert.deepEqual(observation.state('turn')?.info.ses.permissions, ['read'])
})

test('lifecycle only controls workflow terminal status and relationships', () => {
	const observation = new RunObservation(snapshot())
	const connection = observation.beginConnection()
	observation.apply({ type: 'lifecycle', data: { seq: 1, time: '2026-01-01T00:00:00Z', scope: 'lap.2', session: 'reviewer', event: { kind: 'session_created', name: 'reviewer', adapter: 'claude', model: 'opus', workdir: '/tmp', parent: 'ses' } } }, connection)
	observation.apply({ type: 'event', data: { scope: 'lap', session: 'ses', turn: 'turn', event: { id: 'ended', created: 3, type: 'session.step.ended', data: { sessionID: 'ses', assistantMessageID: 'none', finish: 'stop' } } } }, connection)
	assert.equal(observation.run.status, 'running')
	assert.equal(observation.run.sessions.reviewer.parent, 'ses')
	observation.apply({ type: 'lifecycle', data: { seq: 2, time: '2026-01-01T00:00:01Z', scope: '', event: { kind: 'run_ended', name: 'Run', error: '' } } }, connection)
	assert.equal(observation.run.status, 'completed')
})

test('message revision survives a following lifecycle frame', () => {
	const observation = new RunObservation(snapshot())
	const connection = observation.beginConnection()
	observation.apply({ type: 'event', data: { scope: 'lap', session: 'ses', turn: 'turn', event: { id: 'delta', created: 3, type: 'session.text.delta', data: { sessionID: 'ses', assistantMessageID: 'message', delta: 'done' } } } }, connection)
	const changed = observation.messageRevision('turn', 'message')
	observation.apply({ type: 'lifecycle', data: { seq: 2, time: '2026-01-01T00:00:01Z', scope: '', event: { kind: 'run_ended', name: 'Run', error: '' } } }, connection)
	assert.equal(observation.messageRevision('turn', 'message'), changed)
})

test('usage is five zero-filled token counts and a cost, whatever the harness reported', () => {
	assert.deepEqual(usageOf(undefined), { cost: 0, tokens: { input: 0, output: 0, reasoning: 0, cache: { read: 0, write: 0 } } })
	assert.deepEqual(usageOf({ cost: 0.25, tokens: { input: 10, output: 2, reasoning: 3, cache: { read: 4, write: 5 } } }),
		{ cost: 0.25, tokens: { input: 10, output: 2, reasoning: 3, cache: { read: 4, write: 5 } } })
	// A field the harness never reported reads as the number zero, with no
	// wording about availability anywhere in the line.
	assert.equal(usageText(usageOf({ tokens: { input: 7 } })), '7 in · 0 out · 0 reasoning · 0 cache read · 0 cache write · $0')
})

test('a session total is the running total the reducer folded into session info', () => {
	const observation = new RunObservation(snapshot())
	const connection = observation.beginConnection()
	const usage = { cost: 0.5, tokens: { input: 12, output: 3, reasoning: 1, cache: { read: 6, write: 7 } } }
	observation.apply({ type: 'event', data: { scope: 'lap', session: 'ses', turn: 'turn', event: { id: 'usage', created: 4, type: 'session.usage.updated', data: { sessionID: 'ses', ...usage } } } }, connection)
	assert.deepEqual(usageOf(observation.state('turn')?.info.ses), usage)
})

test('provenance keys the latest native sidecar by normalized message ID', () => {
	const provenance: Record<string, unknown> = {}
	foldProvenance(provenance, { data: { assistantMessageID: 'source-id' } }, { normalizedMessageID: 'msg_canonical', messageID: 'provider-id' })
	assert.deepEqual(Object.keys(provenance), ['msg_canonical'])
	assert.equal((provenance.msg_canonical as { messageID: string }).messageID, 'provider-id')
})

test('a usage.updated frame is the session running total on the run, replaced by the next one', () => {
	const observation = new RunObservation(snapshot())
	const connection = observation.beginConnection()
	const first = { cost: 0.5, tokens: { input: 12, output: 3, reasoning: 1, cache: { read: 6, write: 7 } } }
	const second = { cost: 1.25, tokens: { input: 30, output: 9, reasoning: 2, cache: { read: 8, write: 0 } } }
	observation.apply({ type: 'event', data: { scope: 'lap', session: 'ses', turn: 'turn', event: { id: 'usage-1', created: 4, type: 'session.usage.updated', data: { sessionID: 'ses_native', ...first } } } }, connection)
	assert.deepEqual(observation.run.usage?.ses, first)
	observation.apply({ type: 'event', data: { scope: 'lap', session: 'ses', turn: 'turn', event: { id: 'usage-2', created: 5, type: 'session.usage.updated', data: { sessionID: 'ses_native', ...second } } } }, connection)
	assert.deepEqual(observation.run.usage?.ses, second)
})

test('a cancelled run stays cancelled, as the Go store decides it', () => {
	const observation = new RunObservation(snapshot())
	const connection = observation.beginConnection()
	const fixture = new URL('../../../../internal/observation/testdata/cancelled-run.jsonl', import.meta.url)
	for (const line of readFileSync(fixture, 'utf8').trim().split('\n')) {
		observation.apply({ type: 'lifecycle', data: JSON.parse(line) as LifecycleRecord }, connection)
	}
	assert.equal(observation.run.status, 'cancelled')
	assert.equal(observation.run.error, 'context canceled')
})

test('the snapshot carries the scope tree and workflow lifecycle folds into it', () => {
	const observation = new RunObservation(snapshot())
	const connection = observation.beginConnection()
	const frame = (seq: number, scope: string, event: Record<string, unknown>) =>
		observation.apply({ type: 'lifecycle', data: { seq, time: '2026-01-01T00:00:00Z', scope, event } as LifecycleRecord }, connection)
	frame(1, 'loop.1', { kind: 'planner_decision', task: { name: 'write a.txt' } })
	frame(2, 'loop.1/task.2', { kind: 'scope_began', name: 'task.2', task: { name: 'write a.txt' } })
	frame(3, 'loop.1/task.2', { kind: 'value_set', key: 'worker result', value: '"done"' })
	frame(4, 'loop.1/task.2', { kind: 'scope_ended', error: '' })
	frame(5, 'loop.1', { kind: 'planner_decision' })
	// An ended scope with no error is ended, never succeeded.
	assert.deepEqual(observation.scopes['loop.1/task.2'], { name: 'task.2', status: 'ended', task: { name: 'write a.txt' }, values: { 'worker result': 'done' } })
	assert.deepEqual(observation.scopes['loop.1'].decisions, [{ task: { name: 'write a.txt' } }, {}])
	assert.deepEqual(observation.snapshot().scopes, observation.scopes)
})
