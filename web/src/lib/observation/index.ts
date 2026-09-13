import { SessionProjection, type JSONObject, type JSONValue, type ProjectionState, type Snapshot } from '../sessionstate/index.js'
import type { Usage } from '../skgo/observation/types.js'

export type { Usage }

export type RunStatus = 'running' | 'completed' | 'failed' | 'cancelled'
export type RunSession = { name: string; adapter: string; model: string; scope: string; parent?: string }
export type InvocationSnapshot = { scope: string; session: string; turn: string; snapshot: Snapshot; provenance: Record<string, unknown> }
/** One scope instance's workflow state. An ended scope with no error is 'ended', never 'succeeded'. */
export type ScopeInfo = { name: string; status: 'running' | 'ended'; error?: string; task?: JSONValue; values?: Record<string, JSONValue>; decisions?: JSONValue[] }
export type RunSnapshot = {
	run: { id: string; name: string; status: RunStatus; error?: string; sessions: Record<string, RunSession>; usage?: Record<string, Usage> }
	scopes: Record<string, ScopeInfo>
	invocations: Record<string, InvocationSnapshot>
}
export type LifecycleRecord = { seq: number; time: string; scope: string; session?: string; turn?: string; event: { kind: string; [key: string]: unknown } }
export type ObservationFrame =
	| { type: 'snapshot'; data: RunSnapshot }
	| { type: 'event'; data: { scope: string; session: string; turn: string; event: JSONObject; nativeRef?: JSONValue } }
	| { type: 'lifecycle'; data: LifecycleRecord }
export type Invocation = Omit<InvocationSnapshot, 'snapshot'> & { projection: SessionProjection }

/** The native event that carries a session's running total. */
const usageUpdated = 'session.usage.updated'

const emptySnapshot = (): Snapshot => ({ state: { info: {}, family: {}, active: {}, message: {}, pending: {}, permission: {}, form: {} } })
const clone = <T>(value: T): T => structuredClone(value)
const scopeAt = (scopes: Record<string, ScopeInfo>, key: string): ScopeInfo => (scopes[key] ??= { name: key, status: 'running' })
/** A lifecycle record carries a stored value as its JSON source text. */
const parseValue = (text: unknown): JSONValue => { try { return JSON.parse(String(text)) as JSONValue } catch { return String(text) } }

/** Live run state. Revision invalidates renderers without cloning transcript data per frame. */
export class RunObservation {
	run: RunSnapshot['run']
	scopes: Record<string, ScopeInfo> = {}
	readonly invocations = new Map<string, Invocation>()
	revision = 0
	private messageRevisions = new Map<string, number>()
	private snapshotRevision = 0
	private connectionGeneration = 0

	constructor(snapshot: RunSnapshot) { this.run = clone(snapshot.run); this.replace(snapshot) }
	beginConnection(): number { return ++this.connectionGeneration }
	endConnection(generation: number) { if (generation === this.connectionGeneration) this.connectionGeneration++ }
	isCurrentConnection(generation: number) { return generation === this.connectionGeneration }

	replace(snapshot: RunSnapshot, generation?: number): boolean {
		if (generation !== undefined && !this.isCurrentConnection(generation)) return false
		this.run = clone(snapshot.run)
		this.scopes = clone(snapshot.scopes ?? {})
		this.messageRevisions.clear()
		this.snapshotRevision = this.revision + 1
		this.invocations.clear()
		for (const [turn, value] of Object.entries(snapshot.invocations)) this.invocations.set(turn, {
			scope: value.scope, session: value.session, turn: value.turn,
			projection: SessionProjection.restore(value.snapshot), provenance: clone(value.provenance)
		})
		this.revision++
		return true
	}

	apply(frame: ObservationFrame, generation: number): boolean {
		if (!this.isCurrentConnection(generation)) return false
		switch (frame.type) {
			case 'snapshot': return this.replace(frame.data, generation)
			case 'event': {
				const value = frame.data
				const messageID = canonicalMessageID(value.event)
				if (messageID) this.messageRevisions.set(`${value.turn}\0${messageID}`, this.revision + 1)
				let invocation = this.invocations.get(value.turn)
				if (!invocation) {
					invocation = { scope: value.scope, session: value.session, turn: value.turn, projection: SessionProjection.restore(emptySnapshot()), provenance: {} }
					this.invocations.set(value.turn, invocation)
				}
				invocation.projection.apply(value.event)
				// The session's running total lives on the run, keyed by placement
				// session, exactly as the Go store folds it: set semantics, the
				// latest event is the total so far.
				if (value.event.type === usageUpdated) (this.run.usage ??= {})[value.session] = usageOf(value.event.data)
				foldProvenance(invocation.provenance, value.event, value.nativeRef)
				break
			}
			case 'lifecycle': foldLifecycle(this.run, this.scopes, frame.data); break
		}
		this.revision++
		return true
	}

	state(turn: string): Readonly<ProjectionState> | undefined { return this.invocations.get(turn)?.projection.viewState() }
	messageRevision(turn: string, message: string): number { return this.messageRevisions.get(`${turn}\0${message}`) ?? this.snapshotRevision }
	snapshot(): RunSnapshot {
		return { run: clone(this.run), scopes: clone(this.scopes), invocations: Object.fromEntries([...this.invocations].map(([turn, value]) => [turn, {
			scope: value.scope, session: value.session, turn: value.turn,
			snapshot: value.projection.snapshot(), provenance: clone(value.provenance)
		}])) }
	}
}

export function foldLifecycle(run: RunSnapshot['run'], scopes: Record<string, ScopeInfo>, record: LifecycleRecord) {
	const event = record.event
	switch (event.kind) {
		case 'run_started': run.name = String(event.name); run.status = 'running'; delete run.error; break
		// A cancelled run stays cancelled, as the Go store decides it.
		case 'run_ended': if (run.status === 'running') { run.status = event.error ? 'failed' : 'completed'; if (event.error) run.error = String(event.error); else delete run.error }; break
		case 'run_cancelled': run.status = 'cancelled'; if (event.error) run.error = String(event.error); else delete run.error; break
		case 'session_created':
			if (record.session) run.sessions[record.session] = { name: String(event.name), adapter: String(event.adapter), model: String(event.model), scope: record.scope, ...(event.parent ? { parent: String(event.parent) } : {}) }
			break
		case 'scope_began': {
			const scope = scopeAt(scopes, record.scope)
			scope.name = String(event.name); scope.status = 'running'
			if (event.task !== undefined) scope.task = event.task as JSONValue
			break
		}
		case 'scope_ended': {
			const scope = scopeAt(scopes, record.scope)
			scope.status = 'ended'
			if (event.error) scope.error = String(event.error); else delete scope.error
			break
		}
		case 'value_set': {
			const scope = scopeAt(scopes, record.scope)
			;(scope.values ??= {})[String(event.key)] = parseValue(event.value)
			break
		}
		case 'planner_decision': {
			const scope = scopeAt(scopes, record.scope)
			;(scope.decisions ??= []).push((event.task !== undefined ? { task: event.task } : {}) as JSONValue)
			break
		}
	}
}

export function canonicalMessageID(event: JSONObject): string | undefined {
	const data = event.data ?? {}
	if (typeof data.assistantMessageID === 'string') return data.assistantMessageID
	if (typeof data.messageID === 'string') return data.messageID
	if (typeof data.message?.id === 'string') return data.message.id
	if (typeof data.inboxID === 'string') return data.inboxID
	return undefined
}

export function foldProvenance(target: Record<string, unknown>, event: JSONObject, nativeRef?: JSONValue) {
	if (nativeRef === undefined) return
	const normalized = typeof nativeRef === 'object' && nativeRef !== null && !Array.isArray(nativeRef) ? nativeRef.normalizedMessageID : undefined
	const messageID = typeof normalized === 'string' ? normalized : canonicalMessageID(event)
	if (messageID) target[messageID] = clone(nativeRef)
}

/** The five token counts and the cost of one message or session, zero-filled.
 * A count a harness did not report is 0: zero is a number, and nothing here
 * says whether it was measured. */
export function usageOf(value: JSONObject | undefined): Usage {
	const tokens = (value?.tokens ?? {}) as JSONObject
	const cache = (tokens.cache ?? {}) as JSONObject
	return {
		cost: numberOf(value?.cost),
		tokens: {
			input: numberOf(tokens.input),
			output: numberOf(tokens.output),
			reasoning: numberOf(tokens.reasoning),
			cache: { read: numberOf(cache.read), write: numberOf(cache.write) }
		}
	}
}

/** One line of usage, for a message, a session total or a scope. */
export const usageText = (usage: Usage): string =>
	`${usage.tokens.input} in · ${usage.tokens.output} out · ${usage.tokens.reasoning} reasoning · ` +
	`${usage.tokens.cache.read} cache read · ${usage.tokens.cache.write} cache write · $${usage.cost}`

const numberOf = (value: unknown): number => (typeof value === 'number' && Number.isFinite(value) ? value : 0)
