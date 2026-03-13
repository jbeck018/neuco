---
name: Always assign blocked tasks to the decision-maker
description: Board feedback — blocked tasks must have an assignee, not just a mention in comments
type: feedback
---

When creating blocked tasks that need board decisions, always assign them to the board user (`assigneeUserId: "local-board"`) so they appear in the board's inbox.

**Why:** Board noticed blocked items were mentioned in comments but never assigned to them, so the tasks didn't appear in their queue.
**How to apply:** Any time a task is blocked on a human decision, PATCH it with the appropriate `assigneeUserId` so the decision-maker sees it in their assignments.
