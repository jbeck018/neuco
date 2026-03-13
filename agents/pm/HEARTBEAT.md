# HEARTBEAT.md -- PM Heartbeat Checklist

Run this checklist on every heartbeat.

## 1. Identity and Context

- `GET /api/agents/me` -- confirm your id, role, budget, chainOfCommand.
- Check wake context: `PAPERCLIP_TASK_ID`, `PAPERCLIP_WAKE_REASON`, `PAPERCLIP_WAKE_COMMENT_ID`.

## 2. Get Assignments

- `GET /api/companies/{companyId}/issues?assigneeAgentId={your-id}&status=todo,in_progress,blocked`
- Prioritize: `in_progress` first, then `todo`. Skip `blocked` unless you can unblock it.
- If `PAPERCLIP_TASK_ID` is set and assigned to you, prioritize that task.

## 3. Checkout and Work

- Always checkout before working: `POST /api/issues/{id}/checkout`.
- Never retry a 409 -- that task belongs to someone else.
- Do the QA work: read code, verify routes, check UI behavior, validate data flow.
- Comment with findings. File subtasks for bugs found.

## 4. Testing Approach

1. **Read the code** -- check that routes exist, components render correct data, types match
2. **Verify API contracts** -- ensure frontend types match backend response shapes
3. **Check responsive design** -- read Tailwind classes, verify mobile breakpoints
4. **Validate error states** -- check loading, error, and empty states
5. **Test integration points** -- OAuth flows, webhook handlers, third-party API calls

## 5. Bug Reporting

When you find issues:
- Create subtasks with clear titles and reproduction steps
- Set appropriate priority (critical/high/medium/low)
- Assign to the right agent (engineer for code bugs, designer for UI issues)
- Always set `parentId` to the QA task

## 6. Exit

- Comment on any in_progress work before exiting.
- If all tests pass, mark the task done with a summary.
- If issues found, mark done and reference the filed bugs.
