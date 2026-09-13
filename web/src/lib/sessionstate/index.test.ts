import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import { describe, test } from 'node:test'
import { SessionProjection, type ProjectionState } from './index.ts'

const initial = (fixture: any): ProjectionState => {
	const info = Object.fromEntries(fixture.seed.info.map((item: any) => [item.id, item]))
	const family = Object.fromEntries(fixture.seed.info.filter((item: any) => !item.parentID).map((item: any) => [item.id, [item.id]]))
	return { info, family, active: {}, message: structuredClone(fixture.seed.messages ?? {}), pending: structuredClone(fixture.seed.pending ?? {}), permission: {}, form: {} }
}

const load = async (name: string) => JSON.parse(await readFile(new URL(`./fixtures/${name}.json`, import.meta.url), 'utf8'))

describe('session event projection', () => {
	for (const name of ['stream', 'native-backend', 'overlapping-reasoning', 'usage-running-total']) {
		test(`${name} restores at every event cut`, async () => {
			const fixture = await load(name)
			const uninterrupted = new SessionProjection(initial(fixture))
			for (const event of fixture.events) uninterrupted.apply(event)
			for (let cut = 0; cut <= fixture.events.length; cut++) {
				const before = new SessionProjection(initial(fixture))
				for (const event of fixture.events.slice(0, cut)) before.apply(event)
				const restored = SessionProjection.restore(before.snapshot())
				for (const event of fixture.events.slice(cut)) restored.apply(event)
				assert.deepEqual(restored.snapshot().state, uninterrupted.snapshot().state, `cut ${cut}`)
			}
		})
	}

	// The same fixture the Go suite replays: the core publishes
	// session.usage.updated after every step.ended, after every step.failed
	// that carries usage, and again after the harness turn report, so the last
	// one is the session total and each assistant message keeps its own step.
	test('usage.updated carries the session running total', async () => {
		const fixture = await load('usage-running-total')
		const projection = new SessionProjection(initial(fixture))
		for (const event of fixture.events) projection.apply(event)
		const state = projection.snapshot().state

		const info = state.info['ses_usage']
		assert.equal(info.cost, 0.0301)
		assert.deepEqual(info.tokens, { input: 147, output: 33, reasoning: 5, cache: { read: 210, write: 30 } })

		const byID = Object.fromEntries(state.message['ses_usage'].map((item: any) => [item.id, item]))
		assert.equal(byID['msg_step_1'].cost, 0)
		assert.deepEqual(byID['msg_step_1'].tokens, { input: 100, output: 20, reasoning: 5, cache: { read: 10, write: 30 } })
		assert.equal(byID['msg_step_2'].cost, 0.0125)
		assert.deepEqual(byID['msg_step_2'].tokens, { input: 40, output: 10, reasoning: 0, cache: { read: 200, write: 0 } })
		assert.equal(byID['msg_step_3'].cost, 0.004)
		assert.deepEqual(byID['msg_step_3'].tokens, { input: 7, output: 3, reasoning: 0, cache: { read: 0, write: 0 } })
	})

	test('usage.updated for an unknown session changes nothing', () => {
		const projection = new SessionProjection({ info: {}, family: {}, active: {}, message: {}, pending: {}, permission: {}, form: {} })
		projection.apply({ id: 'evt_1', created: 1, type: 'session.usage.updated', data: { sessionID: 'ses_absent', cost: 1, tokens: { input: 1, output: 1, reasoning: 0, cache: { read: 0, write: 0 } } } })
		assert.deepEqual(projection.snapshot().state.info, {})
	})
})
