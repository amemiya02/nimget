# Issue tracker: GitHub

Issues and specs live in https://github.com/amemiya02/nimget/issues.
Use the authenticated gh CLI with --repo amemiya02/nimget.
Verify the target with gh repo view before creating an issue.
Use --body-file for multiline issue bodies and comments.
Check for an existing matching issue before publishing a spec.
Read back the created issue to verify its body and labels.

PRs as a request surface: no.

## Wayfinding operations

- **Map.** Open one issue labeled `wayfinder:map`. Keep notes, decisions, and open questions in its body.
- **Tickets.** Link each ticket as a sub-issue and apply `wayfinder:research`, `wayfinder:prototype`, `wayfinder:grilling`, or `wayfinder:task`. If sub-issues are unavailable, use a task list in the map and add `Part of #<map>` to each ticket.
- **Blockers.** Record blockers with GitHub issue dependencies. If dependencies are unavailable, add `Blocked by: #<issue>` to the ticket. A ticket becomes eligible when all its blockers are closed.
- **Claim.** In map order, select the first open, unassigned ticket with no open blockers. Assign it to yourself before starting work.
- **Resolve.** Comment with the outcome, close the ticket, and add a short decision and ticket link to the map.
