<script lang="ts">
	import { page } from '$app/stores';

	let { error, status } = $props<{
		error?: App.Error;
		status?: number;
	}>();

	const currentStatus = $derived(status ?? $page.status ?? 500);
	const errorMessage = $derived(
		error?.message ?? $page.error?.message ?? 'Something went wrong. Please try again.'
	);

	const title = $derived.by(() => {
		if (currentStatus === 404) return 'Not found';
		if (currentStatus >= 500) return 'Something broke';
		return 'Unable to load this page';
	});

	const subtitle = $derived.by(() => {
		if (currentStatus === 404) return "We couldn't find what you were looking for.";
		if (currentStatus >= 500) return 'The app ran into a server error.';
		return 'Please refresh and try again.';
	});

	const dashboardHref = $derived(
		$page.params.orgSlug ? `/${$page.params.orgSlug}/dashboard` : '/'
	);

	function tryAgain() {
		window.location.reload();
	}
</script>

<div class="flex min-h-screen items-center justify-center bg-background px-4 py-10">
	<div class="w-full max-w-lg rounded-xl border border-border bg-card p-6 shadow-sm dark:bg-card/80 sm:p-8">
		<p class="text-xs font-semibold uppercase tracking-wide text-muted-foreground">Error {currentStatus}</p>
		<h1 class="mt-2 text-2xl font-semibold tracking-tight text-foreground">{title}</h1>
		<p class="mt-2 text-sm text-muted-foreground">{subtitle}</p>

		<div class="mt-5 rounded-md border border-border/80 bg-muted/40 px-4 py-3 dark:bg-muted/20">
			<p class="text-sm text-foreground">{errorMessage}</p>
		</div>

		<div class="mt-6 flex flex-col gap-3 sm:flex-row">
			<a
				href={dashboardHref}
				class="inline-flex items-center justify-center rounded-md border border-border bg-background px-4 py-2 text-sm font-medium text-foreground transition-colors hover:bg-accent hover:text-accent-foreground"
			>
				Go to Dashboard
			</a>
			<button
				type="button"
				onclick={tryAgain}
				class="inline-flex items-center justify-center rounded-md bg-primary px-4 py-2 text-sm font-medium text-primary-foreground transition-colors hover:bg-primary/90"
			>
				Try Again
			</button>
		</div>
	</div>
</div>
