import type { components } from './v1';

type Schema = components['schemas'];

type SnakeToCamel<S extends string> = S extends `${infer Head}_${infer Tail}`
	? `${Head}${Capitalize<SnakeToCamel<Tail>>}`
	: S;

type Primitive = string | number | boolean | bigint | symbol | null | undefined;

// Recursively convert snake_case object keys to camelCase.
export type CamelCaseKeys<T> = T extends Primitive
	? T
	: T extends (...args: never[]) => unknown
		? T
		: T extends readonly (infer U)[]
			? CamelCaseKeys<U>[]
			: T extends object
				? {
						[K in keyof T as K extends string ? SnakeToCamel<K> : K]: CamelCaseKeys<T[K]>;
				  }
				: T;

// ─── FE-only utility types ───────────────────────────────────────────────────

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

// ─── OpenAPI schema compat exports (camelCase) ──────────────────────────────

export type User = CamelCaseKeys<Schema['User']>;
export type Organization = CamelCaseKeys<Schema['Organization']>;
export type OrgRole = Schema['OrgRole'];
export type OrgMember = CamelCaseKeys<Schema['OrgMember']>;

export type Framework = Schema['ProjectFramework'];
export type Styling = Schema['ProjectStyling'];
export type Project = CamelCaseKeys<Schema['Project']>;

export type SignalSource = Schema['SignalSource'];
export type SignalType = Schema['SignalType'];
export type Signal = CamelCaseKeys<Schema['Signal']>;

export type CandidateStatus = Schema['CandidateStatus'];
export type FeatureCandidate = CamelCaseKeys<Schema['FeatureCandidate']>;

export type UserStory = CamelCaseKeys<Schema['UserStory']>;
export type Spec = CamelCaseKeys<Schema['Spec']>;

export type GeneratedFile = CamelCaseKeys<Schema['GeneratedFile']>;
export type Generation = CamelCaseKeys<Schema['Generation']>;

export type PipelineType = Schema['PipelineType'];
export type PipelineTask = CamelCaseKeys<Schema['PipelineTask']>;
export type PipelineRun = CamelCaseKeys<Schema['PipelineRun']>;

export type CopilotNote = CamelCaseKeys<Schema['CopilotNote']>;
export type CopilotNoteType = NonNullable<CopilotNote['noteType']>;

export type AuditEntry = CamelCaseKeys<Schema['AuditEntry']>;

export type Integration = CamelCaseKeys<Schema['Integration']>;

export type Subscription = CamelCaseKeys<Schema['Subscription']>;
export type PlanLimits = CamelCaseKeys<Schema['PlanLimits']>;
export type UsageSummary = CamelCaseKeys<Schema['UsageSummary']>;

export type LLMUsageAgg = CamelCaseKeys<Schema['LLMUsageAgg']>;
export type LLMCall = CamelCaseKeys<Schema['LLMCall']>;

export type OnboardingStep = Schema['OnboardingStep'];
export type OnboardingStatus = CamelCaseKeys<Schema['OnboardingStatus']>;

export type AgentConfig = CamelCaseKeys<Schema['AgentConfig']>;
export type AgentProviderInfo = CamelCaseKeys<Schema['AgentProviderInfo']>;
export type UpsertAgentConfigPayload = CamelCaseKeys<Schema['UpsertAgentConfigRequest']>;
export type ValidateAgentConfigPayload = CamelCaseKeys<Schema['ValidateAgentConfigRequest']>;
export type ValidateAgentConfigResponse = CamelCaseKeys<Schema['ValidateAgentConfigResponse']>;

export type SandboxSession = CamelCaseKeys<Schema['SandboxSession']>;
export type SandboxSessionStatus = NonNullable<SandboxSession['status']>;

export type AuthResponse = CamelCaseKeys<Schema['AuthTokenResponse']>;

// ─── FE-only domain/UI types ────────────────────────────────────────────────

export interface SignalFilterParams extends PageParams {
	source?: SignalSource;
	type?: SignalType;
	projectId?: string;
	search?: string;
	excludeDuplicates?: boolean;
}

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

export interface ProjectStats {
	signalsIngested: number;
	candidatesFound: number;
	prsCreated: number;
	pipelineSuccessRate: number;
	totalPipelines: number;
	failedPipelines: number;
}

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

export type NotificationType =
	| 'pipeline_completed'
	| 'pipeline_failed'
	| 'new_candidate'
	| 'copilot_insight'
	| 'new_signal_batch'
	| 'pr_created';

export interface SubscriptionResponse {
	subscription: Subscription | null;
	limits: PlanLimits;
}

export type PlanTier = NonNullable<Subscription['planTier']>;
export type SubscriptionStatus = NonNullable<Subscription['status']>;

export interface LLMCallsPage {
	calls: LLMCall[];
	total: number;
}

export interface SandboxSessionPage {
	sessions: SandboxSession[];
	total: number;
}

export type AgentProviderName =
	| 'claude-code'
	| 'codex'
	| 'gemini'
	| 'opencode'
	| 'slate'
	| 'aider'
	| 'generic';

export interface ProjectContext {
	id: string;
	projectId: string;
	category: ContextCategory;
	title: string;
	content: string;
	sourceRunId?: string;
	metadata?: Record<string, unknown>;
	createdAt: string;
	updatedAt: string;
}

export type ContextCategory = 'insight' | 'theme' | 'decision' | 'risk' | 'opportunity';

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

export type IntegrationProvider = 'github' | 'slack' | 'linear' | 'jira' | 'notion';

export type PipelineStatus = 'pending' | 'running' | 'completed' | 'failed' | 'cancelled';

export type CopilotNoteTargetType = CamelCaseKeys<Schema['CopilotNoteTargetType']>;

export interface PipelineTaskCompat extends Omit<PipelineTask, 'status'> {
	status: PipelineStatus;
}

export interface PipelineRunCompat extends Omit<PipelineRun, 'status' | 'tasks'> {
	status: PipelineStatus;
	tasks?: PipelineTaskCompat[];
}

export type PipelineTaskLike = PipelineTask | PipelineTaskCompat;
export type PipelineRunLike = PipelineRun | PipelineRunCompat;

export type FeatureCandidateLike = FeatureCandidate;
export type GeneratedFileLike = GeneratedFile;

export type UserLike = User;
export type OrganizationLike = Organization;
export type ProjectLike = Project;

export type SignalLike = Signal;
export type SpecLike = Spec;
export type GenerationLike = Generation;
export type SandboxSessionLike = SandboxSession;

// Keep 32-query-hook imports available with exact names.
export type {
	AuditEntry as _AuditEntryCompat,
	CopilotNote as _CopilotNoteCompat,
	CreateOrgPayload as _CreateOrgPayloadCompat,
	CreateProjectPayload as _CreateProjectPayloadCompat,
	Framework as _FrameworkCompat,
	Generation as _GenerationCompat,
	InviteMemberPayload as _InviteMemberPayloadCompat,
	LLMUsageAgg as _LLMUsageAggCompat,
	Notification as _NotificationCompat,
	OnboardingStatus as _OnboardingStatusCompat,
	OnboardingStep as _OnboardingStepCompat,
	OrgAnalytics as _OrgAnalyticsCompat,
	Organization as _OrganizationCompat,
	OrgMember as _OrgMemberCompat,
	PageParams as _PageParamsCompat,
	PaginatedResponse as _PaginatedResponseCompat,
	PipelineRun as _PipelineRunCompat,
	Project as _ProjectCompat,
	ProjectStats as _ProjectStatsCompat,
	SandboxSession as _SandboxSessionCompat,
	SandboxSessionPage as _SandboxSessionPageCompat,
	Signal as _SignalCompat,
	SignalFilterParams as _SignalFilterParamsCompat,
	Spec as _SpecCompat,
	Styling as _StylingCompat,
	SubscriptionResponse as _SubscriptionResponseCompat,
	UpdateMemberRolePayload as _UpdateMemberRolePayloadCompat,
	UpdateOrgPayload as _UpdateOrgPayloadCompat,
	UpdateProjectPayload as _UpdateProjectPayloadCompat,
	UpdateSpecPayload as _UpdateSpecPayloadCompat,
	UsageSummary as _UsageSummaryCompat,
	User as _UserCompat
};
