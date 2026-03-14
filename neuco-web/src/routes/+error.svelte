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
		if (currentStatus === 404) return 'Page not found';
		if (currentStatus >= 500) return 'Server error';
		return 'Unexpected error';
	});

	const subtitle = $derived.by(() => {
		if (currentStatus === 404) {
			return "The page you're looking for doesn't exist or may have moved.";
		}
		if (currentStatus >= 500) {
			return 'We hit an issue on our side. Please try again in a moment.';
		}
		return 'An unexpected error occurred while loading this page.';
	});
</script>

<div class="flex min-h-screen items-center justify-center bg-background px-6 py-16 text-foreground">
	<div class="w-full max-w-xl text-center">
		<p class="text-sm font-semibold uppercase tracking-wide text-muted-foreground">Error {currentStatus}</p>
		<h1 class="mt-3 text-3xl font-bold tracking-tight text-foreground sm:text-4xl">{title}</h1>
		<p class="mt-3 text-sm text-muted-foreground sm:text-base">{subtitle}</p>

		<div class="mt-6 rounded-lg border border-border bg-card px-5 py-4 text-left shadow-sm dark:bg-card/80">
			<p class="text-sm font-medium text-foreground">{errorMessage}</p>
		</div>

		<div class="mt-8">
			<a
				href="/"
				class="inline-flex items-center justify-center rounded-md bg-primary px-4 py-2 text-sm font-medium text-primary-foreground transition-colors hover:bg-primary/90 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2"
			>
				Go Home
			</a>
		</div>
	</div>
</div>
