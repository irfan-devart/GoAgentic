# Tracker Sweep

Reference for `agents/CONVENTIONS.md` (the master). Loaded on demand, not at startup.

#### Tracker Sweep Procedure

Run it when {{PRINCIPAL}} types `sweep` or asks you to review or clean up your action tracker, and once per `sweep-cadence` (default weekly) when your startup proposes it (master § Tracker Hygiene rule 2). It is your own tracker, so you fix what you can yourself and bring {{PRINCIPAL}} only the decisions. Nothing is deleted, only moved, and nothing is committed until your session close, so {{PRINCIPAL}} can reverse any change.

**1. Tidy the format**

1. Remove any struck-through or completed items from Open — move to Completed, delete the row from Open
2. Archive completed items older than 30 days to `actions-archive.md`
3. Refresh stale due dates — anything marked "this week" that's >7 days old gets a new date or TBD
4. Apply P1/P2/P3 sections if not already present
5. Normalise statuses: every Open status starts with `Not started`, `In progress`, `Waiting on <who>`, `Blocked` or `Parked` (master § actions.md), with any detail after ` — `. Blocked on someone / gated / awaiting → `Waiting on <who>`; `Blocked` only when nobody named can move it, with what is missing after ` — `; deferred / on hold / dormant / backlog / watch → `Parked`; Open / To do → `Not started`. Move long status narrative into the Action cell or drop it
6. Label every Open item: the Action cell starts with a bold label of 3–8 words, under 60 characters, naming the work with no dates or status words (master § actions.md). Where a cell already opens with a long bold sentence, cut it down to a label and keep the rest as detail
7. Trim long cells (master § actions.md): an Open cell over about 300 characters keeps its label, current state, next step and one pointer; the history moves to the outcome file or work order, the plan doc, or `items/<#>-<slug>.md` (dated lines, newest last). Exact IDs and recipes go to `reference/` or `tools.md`. Cut Completed rows to one line (label, result, release or commit), first copying any detail found nowhere else into the item note or `actions-archive.md`
8. Tidy `peer/` (`reference/peer.md` § Statuses, Archiving): correct statuses on files that already hold a reply, turn stale `open` requests into answers or tracker items, and move `done` files and `answered` files older than 14 days to `peer/archive/`

**2. Read every Open row against what is true now**

Check each row against its sources: other agents' trackers for shared items, external state (tickets, email, the repo, the CRM) for its status, your session memories for later decisions. Look for these patterns:

1. **Two rows for one piece of work.** Two rows for the same deal or outcome (the contract and the kick-off, the pilot and the proposal). Merge them: one row per deal or outcome.
2. **Superseded and never closed.** A later decision replaced it (an old pricing model, a meeting prep deprioritised weeks ago).
3. **A standing rule filed as an action.** A rule to follow from now on is not work; it belongs in standing memory.
4. **A wrong `Waiting on <who>`.** The item really waits on another person or another item. `Waiting on {{PRINCIPAL}}` gets the closest look.
5. **A pipeline entry, not work.** (Workspaces with a CRM.) A lead with nothing to do until something else lands belongs in the CRM at a parked stage, not in the tracker.
6. **A residual too small to track.** The item stays open only for one small leftover (one person's first login). Close it, and make the leftover a startup check or a line in the parent item.
7. **Steps that belong to a parent item.** Several rows that all happen as part of one larger piece of work: fold them into that row.
8. **An answer only {{PRINCIPAL}} has, unasked.** An item idle for weeks for want of a one-word decision. It becomes a question in step 4.
9. **A stale description.** A row that names a roster, a plan or a count that has since changed (a team-design row listing 2 of the 4 agents built).

Sort every row you would change into one of: **close**, **merge**, **re-status**, **re-prioritise**, or **question** (only {{PRINCIPAL}} can decide it).

**3. Apply your own changes**

Apply every close, merge, re-status and re-prioritise yourself. A closed row moves to Completed with its reason (`superseded by #N`, `merged into #N`). A merge keeps the older number, folds the other row's detail into it, and closes the other. A standing rule moves to standing memory before its row closes.

**4. Bring {{PRINCIPAL}} one report**

1. One table of the changes you made: `#`, change (close / merge / re-status / re-prioritise), and why, in a few words.
2. Then **all** the questions in one numbered batch, each with your recommendation, so {{PRINCIPAL}} can answer `1 yes, 2 no, 3 as you say`.

Never drip the questions one at a time across the session.

**5. Apply the answers and finish**

1. Apply {{PRINCIPAL}}'s answers.
2. Check P1 count — if >8, propose what moves down.
3. Set `Last swept: YYYY-MM-DD` (today) under the `Next session:` line in `actions.md`, adding the line if it is missing.
