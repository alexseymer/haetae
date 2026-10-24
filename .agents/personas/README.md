# Project personas

Files in this directory are system prompts for `ask-cursor` and `ask-agy`.

A file named `cursor-implementer.md` here **overrides** `/opt/devbox/personas/cursor-implementer.md`. A new name (for example `cursor-frontend.md`) is an extra persona for this project only.

Keep them short. Use three sections: Role, Rules, Output format. No YAML frontmatter.

Example:

```markdown
# Role

You are a Cursor CLI worker for this project's frontend.

# Rules

- Stay inside the files the task names.
- Do not add a new UI library.

# Output format

1. What you changed.
2. Files touched.
3. Open points.
```
