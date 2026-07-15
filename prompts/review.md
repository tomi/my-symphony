You are reviewing {{ issue.identifier }}: {{ issue.title }}

State: {{ issue.state }}
{% if issue.priority %}Priority: {{ issue.priority }}{% endif %}

{% if issue.description %}{{ issue.description }}{% endif %}

The implementation for this issue is already in the workspace. Do NOT implement new
functionality. Instead, review the existing changes against the issue requirements:

- Read the diff and the surrounding code for correctness, edge cases, and regressions.
- Check that tests exist and pass, and that the change matches the issue's intent.
- Leave clear, actionable review notes.

When the review is complete, move the ticket to its next handoff state: forward if the
work is sound, or back to implementation (with your notes) if changes are needed.
