/**
 * Frontend-maintained API types.
 *
 * This file extends `types.gen.ts` with frontend-specific concerns that are
 * intentionally hand-written, including:
 * - UI/state-oriented shapes (e.g. filters, payloads, paginated wrappers)
 * - camelCase request/response models used by the web client
 * - composed/enriched view models that flatten or join backend data
 *
 * Keep domain-struct mirrors in sync with backend types, and prefer moving
 * exact domain copies into `types.gen.ts` over time.
 */

// ─── Pagination ───────────────────────────────────────────────────────────────
export interface PageParams {
	page?: number;
	pageSize?: number;
}

export interface PaginatedResponse<T> {
	data: T[];
	total: number;
	page: number;
	pageSize: number;
	totalPages: number;
}

// ─── User & Auth ──────────────────────────────────────────────────────────────

export interface User {
	id: string;
	email: string;
	name: string;
	avatarUrl: string;
	githubLogin: string;
	createdAt: string;
	updatedAt: string;
}

export type OrgRole = 'owner' | 'admin' | 'member' | 'viewer';

export interface Organization {
	id: string;
	name: string;
	slug: string;
	plan: string;
	avatarUrl: string;
	createdAt: string;
	updatedAt: string;
}

export interface OrgMember {
	orgId: string;
	userId: string;
	role: OrgRole;
	invitedAt: string;
	joinedAt?: string;
	// Flat user fields (joined from users table by backend)
	githubLogin: string;
	email: string;
	avatarUrl: string;
}

// ─── Project ──────────────────────────────────────────────────────────────────

export type Framework =
	| 'react'
	| 'nextjs'
	| 'vue'
	| 'nuxt'
	| 'svelte'
	| 'sveltekit'
	| 'angular'
	| 'other';

export type Styling =
	| 'tailwind'
	| 'css_modules'
	| 'styled_components'
	| 'sass'
	| 'plain_css'
	| 'other';

export interface Project {
	id: string;
	orgId: string;
	name: string;
	slug: string;
	githubRepo: string;
	framework: Framework;
	styling: Styling;
	signalCount: number;
	lastActivityAt: string;
	createdAt: string;
	updatedAt: string;
}

export interface ProjectStats {
	signalsIngested: number;
	candidatesFound: number;
	prsCreated: number;
	pipelineSuccessRate: number;
	totalPipelines: number;
	failedPipelines: number;
}

// ─── Signals ──────────────────────────────────────────────────────────────────

export type SignalSource = 'github_issue' | 'github_pr' | 'slack' | 'csv' | 'api' | 'manual';

export type SignalType = 'bug' | 'feature_request' | 'improvement' | 'question' | 'other';

export interface Signal {
	id: string;
	projectId: string;
	source: SignalSource;
	type: SignalType;
	title: string;
	body: string;
	externalId: string;
	externalUrl: string;
	metadata: Record<string, unknown>;
	createdAt: string;
	updatedAt: string;
	contentHash?: string;
	duplicateOfId?: string | null;
}

export interface SignalFilterParams extends PageParams {
	source?: SignalSource;
	type?: SignalType;
	projectId?: string;
	search?: string;
	excludeDuplicates?: boolean;
}

// ─── Feature Candidates ───────────────────────────────────────────────────────

export type CandidateStatus = 'pending' | 'approved' | 'rejected' | 'deferred';

export interface FeatureCandidate {
	id: string;
	projectId: string;
	title: string;
	description: string;
	rationale: string;
	priority: number;
	status: CandidateStatus;
	signalIds: string[];
	createdAt: string;
	updatedAt: string;
}

// ─── Specs ────────────────────────────────────────────────────────────────────

export interface UserStory {
	id: string;
	asA: string;
	iWantTo: string;
	soThat: string;
	acceptanceCriteria: string[];
}

export interface Spec {
	id: string;
	projectId: string;
	candidateId: string;
	title: string;
	summary: string;
	userStories: UserStory[];
	technicalNotes: string;
	version: number;
	createdAt: string;
	updatedAt: string;
}

// ─── Generations ──────────────────────────────────────────────────────────────

export interface GeneratedFile {
	id: string;
	generationId: string;
	path: string;
	content: string;
	language: string;
	isNew: boolean;
}

export interface Generation {
	id: string;
	projectId: string;
	specId: string;
	status: 'pending' | 'running' | 'completed' | 'failed';
	files: GeneratedFile[];
	errorMessage: string;
	prUrl: string;
	prNumber: number;
	createdAt: string;
	updatedAt: string;
}

// ─── Pipelines ────────────────────────────────────────────────────────────────

export type PipelineType =
	| 'signal_ingestion'
	| 'candidate_extraction'
	| 'spec_generation'
	| 'code_generation'
	| 'pr_creation';

export type PipelineStatus = 'pending' | 'running' | 'completed' | 'failed' | 'cancelled';

export interface PipelineTask {
	id: string;
	pipelineId: string;
	name: string;
	status: PipelineStatus;
	startedAt: string;
	completedAt: string;
	errorMessage: string;
	metadata: Record<string, unknown>;
}

export interface PipelineRun {
	id: string;
	projectId: string;
	type: PipelineType;
	status: PipelineStatus;
	tasks: PipelineTask[];
	startedAt: string;
	completedAt: string;
	errorMessage: string;
	metadata: Record<string, unknown>;
	createdAt: string;
	updatedAt: string;
}

// ─── Copilot Notes ────────────────────────────────────────────────────────────

export type CopilotNoteType =
	| 'suggestion'
	| 'warning'
	| 'error'
	| 'info'
	| 'performance'
	| 'security';

export interface CopilotNote {
	id: string;
	projectId: string;
	type: CopilotNoteType;
	title: string;
	body: string;
	dismissed: boolean;
	entityType: string;
	entityId: string;
	createdAt: string;
	updatedAt: string;
}

// ─── Audit ────────────────────────────────────────────────────────────────────

export interface AuditEntry {
	id: string;
	orgId: string;
	userId: string;
	user: User;
	action: string;
	entityType: string;
	entityId: string;
	metadata: Record<string, unknown>;
	createdAt: string;
}

// ─── Integrations ─────────────────────────────────────────────────────────────

export type IntegrationProvider = 'github' | 'slack' | 'linear' | 'jira' | 'notion';

export interface Integration {
	id: string;
	orgId: string;
	provider: IntegrationProvider;
	name: string;
	accessToken: string;
	refreshToken: string;
	expiresAt: string;
	scopes: string[];
	metadata: Record<string, unknown>;
	isActive: boolean;
	createdAt: string;
	updatedAt: string;
}

// ─── Billing & Usage ─────────────────────────────────────────────────────────

export type PlanTier = 'starter' | 'builder';

export type SubscriptionStatus = 'active' | 'past_due' | 'canceled' | 'incomplete' | 'trialing';

export interface Subscription {
	id: string;
	orgId: string;
	stripeCustomerId: string;
	stripeSubscriptionId?: string;
	planTier: PlanTier;
	status: SubscriptionStatus;
	currentPeriodEnd?: string;
	createdAt: string;
	updatedAt: string;
}

export interface PlanLimits {
	maxProjects: number;
	maxSignalsPerMonth: number;
	maxPrsPerMonth: number;
}

export interface UsageSummary {
	limits: PlanLimits;
	projectCount: number;
	signalsUsed: number;
	prsUsed: number;
	planTier?: PlanTier;
}

export interface SubscriptionResponse {
	subscription: Subscription | null;
	limits: PlanLimits;
}

// ─── LLM Usage ───────────────────────────────────────────────────────────────

// TODO: auto-generate from domain.LLMUsageAgg
export interface LLMUsageAgg {
	total_calls: number;
	total_tokens_in: number;
	total_tokens_out: number;
	total_cost_usd: number;
	avg_latency_ms: number;
	p95_latency_ms: number;
}

// TODO: auto-generate from domain.LLMCall
export interface LLMCall {
	id: string;
	project_id: string;
	pipeline_run_id?: string;
	pipeline_task_id?: string;
	provider: string;
	model: string;
	call_type: string;
	tokens_in: number;
	tokens_out: number;
	latency_ms: number;
	cost_usd: number;
	error?: string;
	created_at: string;
}

export interface LLMCallsPage {
	calls: LLMCall[];
	total: number;
}

// ─── Onboarding ──────────────────────────────────────────────────────────────
export type OnboardingStep = 'welcome' | 'org' | 'project' | 'signal' | 'synthesis' | 'done';

export interface OnboardingStatus {
	completedSteps: OnboardingStep[];
	isComplete: boolean;
	totalSteps: number;
}

// ─── Auth Responses ───────────────────────────────────────────────────────────

export interface AuthResponse {
	accessToken: string;
	expiresIn: number;
}

// ─── Create/Update Payloads ───────────────────────────────────────────────────

export interface CreateOrgPayload {
	name: string;
	slug: string;
}

export interface UpdateOrgPayload {
	name?: string;
	avatarUrl?: string;
}

export interface CreateProjectPayload {
	name: string;
	githubRepo?: string;
	framework: Framework;
	styling: Styling;
}

export interface UpdateProjectPayload {
	name?: string;
	githubRepo?: string;
	framework?: Framework;
	styling?: Styling;
}

export interface InviteMemberPayload {
	email: string;
	role: OrgRole;
}

export interface UpdateMemberRolePayload {
	role: OrgRole;
}

export interface UpdateCandidateStatusPayload {
	status: CandidateStatus;
}

export interface UpdateSpecPayload {
	title?: string;
	summary?: string;
	userStories?: UserStory[];
	technicalNotes?: string;
}

// ─── Analytics ────────────────────────────────────────────────────────────────

export interface DailyCount {
	date: string;
	count: number;
}

export interface StatusCount {
	status: string;
	count: number;
}

export interface SourceCount {
	source: string;
	count: number;
}

export interface ProjectAnalytics {
	id: string;
	name: string;
	signalCount: number;
	candidateCount: number;
	prCount: number;
	pipelineCount: number;
}

export interface MemberActivity {
	userId: string;
	displayName: string;
	signalsUploaded: number;
	specsGenerated: number;
	prsCreated: number;
}

export interface OrgAnalytics {
	totalSignals: number;
	totalCandidates: number;
	totalPrs: number;
	pipelineSuccessRate: number;
	signalTrend: DailyCount[];
	pipelineTrend: DailyCount[];
	pipelineBreakdown: StatusCount[];
	candidateBreakdown: StatusCount[];
	signalsBySource: SourceCount[];
	projects: ProjectAnalytics[];
	teamActivity: MemberActivity[];
}

// ─── Project Context (Memory) ─────────────────────────────────────────────────

export type ContextCategory = 'insight' | 'theme' | 'decision' | 'risk' | 'opportunity';

// TODO: auto-generate from domain.ProjectContext
export interface ProjectContext {
	id: string;
	project_id: string;
	category: ContextCategory;
	title: string;
	content: string;
	source_run_id?: string;
	metadata?: Record<string, unknown>;
	created_at: string;
	updated_at: string;
}

export interface CreateProjectContextPayload {
	category: ContextCategory;
	title: string;
	content: string;
}

export interface UpdateProjectContextPayload {
	category: ContextCategory;
	title: string;
	content: string;
}

// ─── Notifications ────────────────────────────────────────────────────────────
export type NotificationType =
	| 'pipeline_completed'
	| 'pipeline_failed'
	| 'new_candidate'
	| 'copilot_insight'
	| 'new_signal_batch'
	| 'pr_created';

export interface Notification {
	id: string;
	orgId: string;
	userId?: string;
	type: NotificationType;
	title: string;
	body: string;
	link: string;
	readAt?: string;
	createdAt: string;
}
