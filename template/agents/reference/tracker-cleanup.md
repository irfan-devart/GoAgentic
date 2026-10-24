# Tracker Cleanup Procedure

Reference for `agents/CONVENTIONS.md` (the master). Loaded on demand, not at startup.

#### Tracker Cleanup Procedure

When {{PRINCIPAL}} asks you to review or clean up your action tracker, follow these steps:

1. Remove any struck-through or completed items from Open — move to Completed, delete the row from Open
2. Archive completed items older than 30 days to `actions-archive.md`
3. Refresh stale due dates — anything marked "this week" that's >7 days old gets a new date or TBD
4. Cross-check shared items against other agents' trackers for status changes
5. Apply P1/P2/P3 sections if not already present
6. Normalise statuses: every Open status starts with `Not started`, `In progress`, `Waiting on <who>`, `Blocked` or `Parked` (master § actions.md), with any detail after ` — `. Blocked on someone / gated / awaiting → `Waiting on <who>`; `Blocked` only when nobody named can move it, with what is missing after ` — `; deferred / on hold / dormant / backlog / watch → `Parked`; Open / To do → `Not started`. Move long status narrative into the Action cell or drop it
7. Label every Open item: the Action cell starts with a bold label of 3–8 words, under 60 characters, naming the work with no dates or status words (master § actions.md). Where a cell already opens with a long bold sentence, cut it down to a label and keep the rest as detail
8. Trim long cells (master § actions.md): an Open cell over about 300 characters keeps its label, current state, next step and one pointer; the history moves to the outcome file or work order, the plan doc, or `items/<#>-<slug>.md` (dated lines, newest last). Exact IDs and recipes go to `reference/` or `tools.md`. Cut Completed rows to one line: label, result, release or commit
9. Check P1 count — if >8, something needs deprioritising
10. Scan for items that may be moot or complete but not marked — present candidates to {{PRINCIPAL}}
11. Tidy `peer/` (`reference/peer.md` § Statuses, Archiving): correct statuses on files that already hold a reply, turn stale `open` requests into answers or tracker items, and move `done` files and `answered` files older than 14 days to `peer/archive/`
12. Present a summary of changes for {{PRINCIPAL}}'s confirmation before saving
