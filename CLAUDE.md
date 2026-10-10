# Claude Code Configuration

@AGENTS.md

The repository rules (architecture, language, Definition of Done, Git and Pull Request flow) live in `AGENTS.md`. They are not duplicated here so the two files cannot drift apart.

## Claude Code specifics

- Meet the Definition of Done in `AGENTS.md` (section 5) before finishing a task.
- **Technical or architectural decisions:** ask the user before deciding; do not generate decision files (ADRs) on your own initiative.
- Do not commit, push or open a PR unless the user asks. Follow sections 7 and 7.1 of `AGENTS.md`.
- Read a service's `README.md` before modifying that service.
