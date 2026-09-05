# Notes

Durable knowledge: what was intended, what was found out, what actually
happened. It lives here, in the repository, because a session ends and the
record has to outlive it.

    notes/plans/       what is intended, written before the work
    notes/research/    what was found out, written while a question is open
    notes/work-logs/   what actually happened, written after something changed

Every file is markdown with front matter that `machinist notes check` reads
back. See [docs/durable-knowledge.md](../docs/durable-knowledge.md) for the
shape and when to write which.

    machinist notes new --kind work-log --title "..." --subject "..."
    machinist notes list --kind plan
    machinist notes check
