package email

import "fmt"

// All templates are plain HTML — no React Email or templating engine.
// Keep it simple for MVP; upgrade to a template engine later.

func wrapHTML(title, body string) string {
	return fmt.Sprintf(`<!DOCTYPE html>
<html>
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<title>%s</title>
<style>
body { font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, sans-serif; background: #f5f5f5; margin: 0; padding: 0; }
.container { max-width: 580px; margin: 0 auto; padding: 40px 20px; }
.card { background: #fff; border-radius: 8px; padding: 32px; box-shadow: 0 1px 3px rgba(0,0,0,0.08); }
h1 { color: #111; font-size: 22px; margin: 0 0 16px; }
p { color: #444; font-size: 15px; line-height: 1.6; margin: 0 0 16px; }
.btn { display: inline-block; background: #111; color: #fff; padding: 12px 24px; border-radius: 6px; text-decoration: none; font-size: 14px; font-weight: 600; }
.footer { text-align: center; padding: 24px 0; color: #888; font-size: 12px; }
.stat { display: inline-block; text-align: center; padding: 12px 20px; }
.stat-value { font-size: 28px; font-weight: 700; color: #111; }
.stat-label { font-size: 12px; color: #666; text-transform: uppercase; letter-spacing: 0.5px; }
.divider { border: none; border-top: 1px solid #eee; margin: 24px 0; }
</style>
</head>
<body>
<div class="container">
<div class="card">
%s
</div>
<div class="footer">
<p>Neuco — AI-native product intelligence</p>
</div>
</div>
</body>
</html>`, title, body)
}

func renderWelcome(userName, frontendURL string) string {
	displayName := userName
	if displayName == "" {
		displayName = "there"
	}
	body := fmt.Sprintf(`
<h1>Welcome to Neuco! 👋</h1>
<p>Hey %s, thanks for signing up. Neuco helps you turn customer signals into shipped features — automatically.</p>
<p>Here's how to get started:</p>
<p><strong>1.</strong> Create a project and connect your GitHub repo<br>
<strong>2.</strong> Upload customer signals (CSV, webhook, or integrations)<br>
<strong>3.</strong> Let Neuco synthesize themes, generate specs, and open PRs</p>
<p style="margin-top: 24px;">
<a href="%s" class="btn">Open Neuco</a>
</p>
`, displayName, frontendURL)
	return wrapHTML("Welcome to Neuco", body)
}

func renderInvite(inviterName, orgName, frontendURL string) string {
	body := fmt.Sprintf(`
<h1>You're invited!</h1>
<p><strong>%s</strong> has invited you to join <strong>%s</strong> on Neuco.</p>
<p>Neuco helps product teams turn customer feedback into shipped features with AI-powered synthesis, spec generation, and automated PRs.</p>
<p style="margin-top: 24px;">
<a href="%s" class="btn">Accept Invite</a>
</p>
`, inviterName, orgName, frontendURL)
	return wrapHTML("Team Invite — Neuco", body)
}

func renderPRCreated(n PRNotification, frontendURL string) string {
	body := fmt.Sprintf(`
<h1>New PR created</h1>
<p>Neuco generated a pull request for <strong>%s</strong>.</p>
<hr class="divider">
<p>
<strong>PR #%d</strong><br>
Files generated: %d
</p>
<p style="margin-top: 24px;">
<a href="%s" class="btn">View Pull Request</a>
</p>
<p style="margin-top: 16px; font-size: 13px; color: #666;">
<a href="%s" style="color: #666;">Open in Neuco</a>
</p>
`, n.ProjectName, n.PRNumber, n.FilesCount, n.PRURL, frontendURL)
	return wrapHTML("PR Created — Neuco", body)
}

func renderWeeklyDigest(d DigestData, frontendURL string) string {
	statsHTML := fmt.Sprintf(`
<div style="text-align: center; padding: 16px 0;">
<div class="stat"><div class="stat-value">%d</div><div class="stat-label">Signals</div></div>
<div class="stat"><div class="stat-value">%d</div><div class="stat-label">Candidates</div></div>
<div class="stat"><div class="stat-value">%d</div><div class="stat-label">Specs</div></div>
<div class="stat"><div class="stat-value">%d</div><div class="stat-label">PRs</div></div>
</div>
`, d.SignalsCount, d.CandidateCount, d.SpecsCount, d.PRsCount)

	projectRows := ""
	for _, p := range d.Projects {
		projectRows += fmt.Sprintf(`<tr><td style="padding:8px 0;border-bottom:1px solid #eee;">%s</td><td style="padding:8px 12px;border-bottom:1px solid #eee;text-align:center;">%d</td><td style="padding:8px 0;border-bottom:1px solid #eee;text-align:center;">%d</td></tr>`, p.Name, p.SignalCount, p.PRCount)
	}

	projectsTable := ""
	if len(d.Projects) > 0 {
		projectsTable = fmt.Sprintf(`
<hr class="divider">
<p style="font-weight: 600; margin-bottom: 8px;">Project breakdown</p>
<table style="width:100%%;border-collapse:collapse;font-size:14px;">
<tr><th style="text-align:left;padding:8px 0;border-bottom:2px solid #eee;">Project</th><th style="padding:8px 12px;border-bottom:2px solid #eee;">Signals</th><th style="padding:8px 0;border-bottom:2px solid #eee;">PRs</th></tr>
%s
</table>
`, projectRows)
	}

	insightsHTML := ""
	if len(d.Insights) > 0 {
		insightItems := ""
		for _, ins := range d.Insights {
			badge := ins.NoteType
			insightItems += fmt.Sprintf(`<li style="margin-bottom:8px;"><span style="display:inline-block;background:#f0f0f0;color:#444;font-size:11px;padding:2px 8px;border-radius:4px;margin-right:8px;text-transform:uppercase;">%s</span><strong>%s:</strong> %s</li>`, badge, ins.ProjectName, ins.Content)
		}
		insightsHTML = fmt.Sprintf(`
<hr class="divider">
<p style="font-weight: 600; margin-bottom: 8px;">Copilot Highlights</p>
<ul style="padding-left:16px;font-size:14px;color:#444;">
%s
</ul>
`, insightItems)
	}

	unsubscribeURL := fmt.Sprintf("%s/%s/settings", frontendURL, d.OrgSlug)

	body := fmt.Sprintf(`
<h1>Weekly Summary — %s</h1>
<p>Here's what happened this week across your organization.</p>
%s
%s
%s
<p style="margin-top: 24px;">
<a href="%s" class="btn">Open Neuco</a>
</p>
<hr class="divider">
<p style="font-size:12px;color:#999;text-align:center;">
You're receiving this because you're an admin of %s on Neuco.<br>
<a href="%s" style="color:#999;">Unsubscribe from weekly digests</a>
</p>
`, d.OrgName, statsHTML, projectsTable, insightsHTML, frontendURL, d.OrgName, unsubscribeURL)
	return wrapHTML("Weekly Digest — Neuco", body)
}
