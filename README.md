# plug-koreader

**Your reading, measured — pages, sessions and highlights, straight from your e-reader.**

## What this plug is

TODO: two or three sentences. This text is the first thing a reader — or an agent —
sees, and the manifest Overview should say the same thing.

## Where the product lives

`entity.go`. One `resource.Spec` declaration generates the capabilities, the MCP
tools, the chat verbs, the add wizard, the window-manager entity and the whole web CRUD
surface, so adding a field means editing one struct and one list.

## What the rails already guarantee

You do not need to remember any of these — they are enforced, not documented:

* the add form lives on its own `/book/new` page; there is no inline option
* the page header comes from ONE `resource.Identity`, so it cannot differ per page
* the navbar is htmx-boosted like every other plug
* one admission gate serves every generated route
* there is no HTML or CSS in this repository; the shared components own the markup, and
  dynlint refuses hand-rolled tables, buttons and inline styles
* `verify_contract_test.go` asserts locally what `mesh.verify` asserts over the
  wire, so a conformance failure is a failing test rather than a failed release

## Demo

This plug **offers an anonymous demo**. A visitor with no account reaches a seeded,
ephemeral workspace and runs the real code against it.

The seeds live in `demo.go` and should demonstrate the product's RANGE, not merely be
non-empty — someone should be able to learn what this plug does without clicking.
Verify anonymously (`curl -sL -c jar -b jar https://koreader.zm.is/`); a logged-in
browser never takes the demo path.

## Deployment

```sh
mesh.deploy plug-koreader
```

Staging builds and proves the binary on itself; production asks for it, fetches those
exact bytes across the bridge, verifies the hash and installs through the privileged
executor, which probes the result and rolls back on its own if it does not stay up.
Nothing is compiled on production and nothing is pushed to it.
