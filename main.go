// Command plug-koreader — Your reading, measured — pages, sessions and highlights, straight from your e-reader.
package main

import (
	"context"
	"log"
	"net/http"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"xf/controller/pkg/dblib"
	"xf/controller/pkg/demo"
	"xf/controller/pkg/flowweb"
	"xf/controller/pkg/plugauth"
	"xf/controller/pkg/plugcfg"
	"xf/controller/pkg/plugrun"
	"xf/controller/pkg/resource"
	"xf/controller/pkg/xats"
)

type app struct {
	items     dblib.DocStore[Book]
	item      *resource.Resource[Book]
	rt        *plugrun.Runtime
	publicURL string
	webGroup  string

	// The reading rail: paired devices, the codes that create them, and the RAW
	// events a device sends. Raw on purpose — see ingest.go.
	devices   dblib.DocStore[Device]
	pairCodes dblib.DocStore[PairCode]
	events    dblib.DocStore[ReadEvent]
	tokens    dblib.DocStore[TokenRef]
	cards     dblib.DocStore[Card]
	pages     dblib.DocStore[PageCapture]

	// Derived state. index caches the aggregate over events (summary.go); tz caches the
	// caller→timezone resolve. Both are rebuilt rather than persisted — they are a view
	// of the raw rows, and a view that can go stale silently is worse than one that is
	// recomputed when the only writer runs.
	index   *indexCache

	// generated is the scaffolded Book CRUD surface, which used to be mounted at "/".
	// serveReadingRoot falls through to it for any path the hand-written pages do not
	// claim. nil before the Web hook runs, which is why serveReadingRoot checks.
	generated http.Handler
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	base := plugcfg.LoadBase("PLUG_KOREADER_")
	// Declared HERE, never derived in a library: xfctl finds a plug's JID by
	// regex-scanning THIS repository for exactly this idiom. A JID that lives only in a
	// shared package is invisible to it — the plug then gets an EMPTY systemd env file,
	// no NATS configuration and no XMPP component, and nothing says so.
	base.ComponentJID = plugcfg.Env("PLUG_KOREADER_COMPONENT_JID", plugcfg.Svc("koreader"))
	base.ComponentName = plugcfg.Env("PLUG_KOREADER_COMPONENT_NAME", "plug-koreader")
	base.WebGroup = plugcfg.Env("PLUG_KOREADER_WEB_GROUP", "plug_koreader")
	base.SocketPath = plugcfg.Env("PLUG_KOREADER_SOCKET_PATH",
		plugcfg.RunDir()+"/koreader.sock")

	pg, err := dblib.OpenPostgres(ctx, dblib.PostgresOptions{
		DSN: plugcfg.Env("PLUG_KOREADER_POSTGRES_DSN", "postgresql:///koreader?host=/run/postgresql&sslmode=disable"),
	})
	if err != nil {
		log.Fatalf("plug-koreader: postgres: %v", err)
	}
	defer pg.Close()

	items, err := dblib.NewPostgresDocStore[Book](ctx, pg, collBooks)
	if err != nil {
		log.Fatalf("plug-koreader: store: %v", err)
	}

	devices, err := dblib.NewPostgresDocStore[Device](ctx, pg, collDevices)
	if err != nil {
		log.Fatalf("plug-koreader: device store: %v", err)
	}
	pairCodes, err := dblib.NewPostgresDocStore[PairCode](ctx, pg, collPairCodes)
	if err != nil {
		log.Fatalf("plug-koreader: pair-code store: %v", err)
	}
	events, err := dblib.NewPostgresDocStore[ReadEvent](ctx, pg, collEvents)
	if err != nil {
		log.Fatalf("plug-koreader: event store: %v", err)
	}
	tokens, err := dblib.NewPostgresDocStore[TokenRef](ctx, pg, collTokens)
	if err != nil {
		log.Fatalf("plug-koreader: token store: %v", err)
	}
	cards, err := dblib.NewPostgresDocStore[Card](ctx, pg, collCards)
	if err != nil {
		log.Fatalf("plug-koreader: card store: %v", err)
	}
	pages, err := dblib.NewPostgresDocStore[PageCapture](ctx, pg, collPages)
	if err != nil {
		log.Fatalf("plug-koreader: page store: %v", err)
	}

	a := &app{items: items, webGroup: base.WebGroup,
		devices: devices, pairCodes: pairCodes, events: events, tokens: tokens, cards: cards, pages: pages,
		index: newIndexCache(),
		publicURL: plugcfg.Env("PLUG_KOREADER_PUBLIC_URL", plugcfg.SvcURL("koreader"))}
	a.item = a.itemEntity()

	rt, err := plugrun.Run(ctx, plugrun.Spec{
		// The component is registered with prosody, and plugrun opens the link from
		// exactly these two. Leave them out and the component NEVER CONNECTS while
		// nothing on this side says so — only prosody complains, bouncing a delivery
		// error back to whoever messaged the plug. Four plugs lost XMPP for two days
		// this way and the symptom was a red (!) on a phone.
		XMPPAddress:    plugcfg.Env("PLUG_KOREADER_XMPP_ADDRESS", ""),
		XMPPSecretFile: plugcfg.Env("PLUG_KOREADER_XMPP_SECRET_FILE", ""),

		Base:     base,
		Manifest: a.manifest(base),
		Dispatch: a.dispatch,
		Web: func(rt *plugrun.Runtime) http.Handler {
			mux := http.NewServeMux()
			mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) })

			// THE DEVICE ENDPOINTS COME FIRST and are deliberately outside the web
			// gate: a reader holds a bearer token, not an SSO session, and the edge
			// routes a Bearer request straight to this socket with identity headers
			// stripped. Both handlers authenticate on the token alone and read no
			// Remote-* header, so a forged one is inert.
			mux.HandleFunc("/api/pair", a.serveAPIPair)
			mux.HandleFunc("/api/ingest", a.serveIngest)
			mux.HandleFunc("/api/page", a.serveAPIPage)

			// The human half, behind the ordinary gate.
			mux.HandleFunc("/", a.serveReadingRoot)
			mux.HandleFunc("/day", a.serveDay)
			mux.HandleFunc("/quotes", a.serveQuotes)
			mux.HandleFunc("/quote", a.serveQuote)
			mux.HandleFunc("/books", a.serveBooks)
			mux.HandleFunc("/page/request", a.servePageRequest)
			mux.HandleFunc("/review", a.serveReview)
			mux.HandleFunc("/pair", a.servePair)
			mux.HandleFunc("/pair/delete", a.servePairDelete)
			mux.HandleFunc("/addon", a.serveAddon)
			mux.HandleFunc("/addon/"+addonDirName+".zip", a.serveAddonZip)
			mux.HandleFunc("/addon/file/", a.serveAddonFile)

			// The generated CRUD surface: the list, the separate /book/new create
			// page, add, delete, the step wizard and the window-manager entity. There
			// is no HTML in this repository, which is why there is nothing here to
			// style, boost or gate a second time.
			//
			// It can no longer own "/" — the calendar does, because the reading is what
			// this product is for and it was reachable from nowhere. ServeMux panics on
			// a second registration of the same pattern, so the generated surface is
			// held here and serveReadingRoot delegates every path except "/" to it,
			// which keeps /book/new and the rest exactly where they were.
			a.generated = a.item.Handler(rt.WebAuthz(), nav("/"))
			return mux
		},
		BillingJID: plugcfg.Env("PLUG_KOREADER_BILLING_JID", plugcfg.Svc("billing")),
		LocalTrust: plugcfg.EnvBool("PLUG_KOREADER_LOCAL_TRUST", true),
		// A demo subject resolves to its own ephemeral workspace on the web path AND
		// on cross-plug fan-out, so the demo runs the REAL code against isolated
		// storage rather than a mock of it.
		WorkspaceResolverWrap: demo.WorkspaceResolver,
	})
	if err != nil {
		log.Fatalf("plug-koreader: %v", err)
	}
	a.rt = rt
	defer rt.Close()

	go demo.RunCleanup(ctx, pg.DB(), time.Hour, 7*24*time.Hour, log.Printf)
	<-ctx.Done()
}

// nav is the plug's header. Keep it short — the brand already links home, so an entry
// pointing at "/" would be a second route to the same page.
func nav(active string) []flowweb.NavLink {
	links := []flowweb.NavLink{
		// NO "Books" ENTRY. It was the scaffolded entity's link and it pointed at "/",
		// which the reading calendar now owns — so the nav offered the same destination
		// twice under two names, and the Active check (URL == active) lit both.
		{Label: "Reading", URL: "/", Icon: "fa-solid fa-calendar-days"},
		{Label: "Books", URL: "/books", Icon: "fa-solid fa-book"},
		{Label: "Quotes", URL: "/quotes", Icon: "fa-solid fa-quote-left"},
		{Label: "Review", URL: "/review", Icon: "fa-solid fa-repeat"},
		{Label: "Devices", URL: "/pair", Icon: "fa-solid fa-tablet-screen-button"},
		{Label: "Plugin", URL: "/addon", Icon: "fa-solid fa-puzzle-piece"},
	}
	for i := range links {
		links[i].Active = links[i].URL == active
	}
	return links
}

// webGate is this plug's SINGLE web admission, used by every surface. A demo visitor
// holds no plug_koreader group by definition, so they must be recognised BEFORE the
// group gate — and doing that in ONE place is the only shape that survives someone
// adding a route later.
func (a *app) webGate(w http.ResponseWriter, r *http.Request) (string, bool) {
	if flowweb.IsDemoRequest(r) {
		return plugauth.FullIdentity(r), true
	}
	return flowweb.AuthGate(w, r, a.webGroup)
}

func (a *app) manifest(base plugcfg.Base) xats.Manifest {
	return xats.Manifest{
		Name: base.ComponentName, Title: koreaderApp.Name, Subtitle: "Your reading, measured — pages, sessions and highlights, straight from your e-reader.",
		Emoji: koreaderApp.Emoji, Icon: koreaderApp.Icon, IconColor: koreaderApp.IconColor,
		URL: a.publicURL, JID: base.ComponentJID, Version: base.ComponentVersion,
		Demo:     true,
		Overview: "Your reading, measured — pages, sessions and highlights, straight from your e-reader.",
		// Every capability comes from the one resource.Spec, which stamps a Command
		// and the required scopes on each. That is what mesh.verify checks, and
		// verify_contract_test.go asserts it here so a drift is a failing test rather
		// than a failed release.
		Capabilities: a.item.Capabilities(),
		Entities:     []xats.EntityDecl{a.item.EntityDecl()},
	}
}

func (a *app) dispatch(ctx context.Context, node string, meta map[string]any) map[string]any {
	tk := tenant(strings.TrimSpace(plugrun.Str(meta, "caller_user_id")))
	if a.item.Handles(node) {
		return a.item.Dispatch(ctx, node, tk, meta)
	}
	// A TYPED rejection, not silence. mesh.verify probes every capability with an
	// empty payload and treats a typed refusal as proof the path is wired; an
	// "unknown capability" reply is how it detects one that is not.
	msg := "unknown capability: " + node
	return map[string]any{"error": msg, "body": msg}
}
