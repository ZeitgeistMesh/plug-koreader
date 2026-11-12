package main

// readweb.go — /books/read: one chapter of an EPUB at a time, in the browser.
//
// The file comes from the same cache as a download, so a page never waits on the book server
// when the book is already there. A person's place is saved as they scroll and the chapter
// opens where they left it, wherever that was made (this page or a KOReader device). A
// passage selected in the page is saved through the highlight capability and reaches vision
// as a quote.

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"

	"xf/controller/pkg/dblib"
	"xf/controller/pkg/webui"
)

const readerReadCSS = `
.rd-shell{display:flex;flex-direction:column;height:calc(100dvh - var(--rd-chrome, 19rem));min-height:16rem}
.rd-shell>.rd-text{flex:1 1 auto;min-height:0;overflow-y:auto;width:100%;box-sizing:border-box;padding-inline:var(--sp-3)}
.rd-bar{display:flex;flex-wrap:wrap;gap:var(--sp-3);align-items:center;justify-content:space-between;margin-block:var(--sp-3)}
.rd-bar .rd-pos{min-width:calc(var(--sp-6) * 3)}
.rd-toc{margin-block:var(--sp-3)}
.rd-toc ol{margin:0;padding-inline-start:var(--sp-5)}
.rd-toc a[aria-current]{font-weight:700}
.rd-text{max-width:42rem;margin-inline:auto;line-height:1.7;font-size:1.125rem;overflow-wrap:anywhere}
.rd-text img{max-width:100%;height:auto}
.rd-text :is(h1,h2,h3){line-height:1.3}
.rd-quote{position:fixed;z-index:20;display:none}
.rd-quote.on{display:block}
`

// readerReadJS saves the place as the page scrolls, restores it on load and offers the selected
// passage to the highlight route. Every fetch goes to a route that calls the capability.
const readerReadJS = `<script>
(function(){
var t=document.getElementById('rd-text');if(!t)return;
var d=t.dataset,pos=document.getElementById('rd-pos'),st=document.getElementById('rd-status'),qb=document.getElementById('rd-quote');
function fit(){var s=document.getElementById('rd-shell');if(!s)return;s.style.setProperty('--rd-chrome','0px');
var top=s.getBoundingClientRect().top+window.scrollY,below=document.documentElement.scrollHeight-top-s.offsetHeight;
s.style.setProperty('--rd-chrome',Math.max(top+below,0)+'px');}
if(window.__rdFit)window.removeEventListener('resize',window.__rdFit);window.__rdFit=fit;window.addEventListener('resize',fit);fit();
if(document.readyState!=='complete')window.addEventListener('load',fit);setTimeout(fit,0);
var p0=parseFloat(d.p0),p1=parseFloat(d.p1),frac=parseFloat(d.frac)||0,timer=null,lastSent=-1;
function room(){return Math.max(t.scrollHeight-t.clientHeight,1);}
function now(){return Math.min(Math.max(t.scrollTop/room(),0),1);}
function show(f){var pct=p0+f*(p1-p0);if(pos)pos.textContent=d.pctlabel.replace('{percent}',pct.toFixed(0));return pct;}
function post(url,obj){return fetch(url,{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify(obj),credentials:'same-origin'});}
function send(){var f=now();if(Math.abs(f-lastSent)<0.005)return;lastSent=f;var pct=show(f);
post(d.progressUrl,{book:d.book,chapter:parseInt(d.chapter,10),frac:f,percent:pct,title:d.title}).catch(function(){});}
function later(){show(now());clearTimeout(timer);timer=setTimeout(send,1500);}
t.addEventListener('scroll',later,{passive:true});
if(window.__rdPageHide)window.removeEventListener('pagehide',window.__rdPageHide);
window.__rdPageHide=function(){send();};window.addEventListener('pagehide',window.__rdPageHide);
var shell=document.getElementById('rd-shell');
if(shell)shell.addEventListener('htmx:beforeRequest',function(){send();});
if(location.hash){var el=document.getElementById(decodeURIComponent(location.hash.slice(1)));if(el)el.scrollIntoView();}
else if(frac>0){t.scrollTop=frac*room();}
show(now());
var sel='';
if(window.__rdSelect)document.removeEventListener('selectionchange',window.__rdSelect);
window.__rdSelect=function(){
var s=window.getSelection();if(!s||s.isCollapsed||!t.contains(s.anchorNode)){qb.classList.remove('on');return;}
sel=s.toString().trim();if(!sel){qb.classList.remove('on');return;}
var r=s.getRangeAt(0).getBoundingClientRect();qb.style.top=Math.max(r.top-48,8)+'px';qb.style.left=Math.max(Math.min(r.left,window.innerWidth-160),8)+'px';qb.classList.add('on');};
document.addEventListener('selectionchange',window.__rdSelect);
qb.addEventListener('mousedown',function(e){e.preventDefault();});
qb.addEventListener('click',function(){var text=sel;if(!text)return;
post(d.highlightUrl,{book:d.book,text:text,chapter:d.chaptertitle,title:d.title,authors:d.authors}).then(function(r){
st.textContent=r.ok?d.saved:d.failed;if(r.ok){window.getSelection().removeAllRanges();qb.classList.remove('on');}}).catch(function(){st.textContent=d.failed;});});
})();
</script>`

// swapAttrs makes a chapter link swap the reading area in place (htmx), keeping the address bar
// and the back button honest, the way the review pagers elsewhere in the mesh do.
func swapAttrs(href string) string {
	h := html.EscapeString(href)
	return ` hx-get="` + h + `" hx-target="#rd-shell" hx-select="#rd-shell" hx-swap="outerHTML" hx-push-url="true"`
}

// epubFile picks the format the browser can read, with the index the file cache names it by.
func epubFile(e *CatalogEntry) (int, bool) {
	for i, f := range e.Files {
		if f.Type == "application/epub+zip" {
			return i, true
		}
	}
	return 0, false
}

// openBook makes sure the EPUB is cached and opens it. The caller closes the book.
func (a *app) openBook(ctx context.Context, tk dblib.TenantKey, s *Source, e *CatalogEntry) (*epubBook, string, int, error) {
	idx, ok := epubFile(e)
	if !ok {
		return nil, "", 0, fmt.Errorf("%s", ui.ErrNotEpub)
	}
	p, err := a.fetchToCache(ctx, tk, s, entryKey(e.ID)+"-"+fmt.Sprint(idx)+".epub", e.Files[idx].Href)
	if err != nil {
		return nil, "", 0, err
	}
	b, err := openEpub(p)
	if err != nil {
		return nil, "", 0, err
	}
	md5, err := koPartialMD5(p)
	if err != nil {
		b.Close()
		return nil, "", 0, err
	}
	return b, md5, idx, nil
}

func (a *app) serveBookRead(w http.ResponseWriter, r *http.Request) {
	caller, ok := a.webGate(w, r)
	if !ok {
		return
	}
	tk := tenant(caller)
	key := r.URL.Query().Get("id")
	s, c := a.loadSource(r.Context(), tk), a.loadCatalog(r.Context(), tk)
	e := findEntry(c, key)
	if s == nil || e == nil {
		http.NotFound(w, r)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	b, md5, fidx, err := a.openBook(ctx, tk, s, e)
	p := a.page(booksPath)
	p.CSS += readerBooksCSS + readerReadCSS
	if err != nil {
		p.StartFor(w, r, e.Title)
		fmt.Fprintf(w, `<p class="hint rd-status" role="alert">%s</p>`, html.EscapeString(fill(ui.ErrOpen, "why", err.Error())))
		p.End(w)
		return
	}
	defer b.Close()

	n, frac := 0, 0.0
	if q := r.URL.Query().Get("n"); q != "" {
		v, perr := strconv.Atoi(q)
		if perr != nil || v < 0 || v >= len(b.spine) {
			http.Error(w, ui.ErrNoChapter, http.StatusNotFound)
			return
		}
		n = v
	} else if pr := a.loadProgress(ctx, tk, md5); pr != nil {
		if pr.Device == webDeviceName && pr.Chapter < len(b.spine) {
			n, frac = pr.Chapter, pr.Frac
		} else {
			n, frac = b.locate(pr.Percent) // a position from an e-reader is only a percentage
		}
	}
	readURL := func(ch int, frag string) string {
		u := fmt.Sprintf("%s/read?id=%s&n=%d", booksPath, url.QueryEscape(key), ch)
		if frag != "" {
			u += "#" + url.PathEscape(frag)
		}
		return u
	}
	resURL := func(zp string) string {
		return fmt.Sprintf("%s/read/res?id=%s&f=%d&p=%s", booksPath, url.QueryEscape(key), fidx, url.QueryEscape(zp))
	}
	body, err := b.chapterHTML(n, readURL, resURL)
	if err != nil {
		http.Error(w, fill(ui.ErrOpen, "why", err.Error()), http.StatusBadGateway)
		return
	}

	chTitle := b.chapterTitle(n)
	if chTitle == "" {
		chTitle = fill(ui.ChapterFallback, "n", fmt.Sprint(n+1))
	}
	p0, p1 := b.percent(n, 0), b.percent(n, 1)
	p.StartFor(w, r, e.Title)
	fmt.Fprint(w, `<div class="rd-shell" id="rd-shell">`)
	var toc strings.Builder
	toc.WriteString(`<details class="rd-toc"><summary>` + html.EscapeString(ui.ContentsHeading) + `</summary><ol>`)
	for i := range b.spine {
		t := b.chapterTitle(i)
		if t == "" {
			t = fill(ui.ChapterFallback, "n", fmt.Sprint(i+1))
		}
		cur := ""
		if i == n {
			cur = ` aria-current="page"`
		}
		fmt.Fprintf(&toc, `<li><a href="%s"%s%s>%s</a></li>`, html.EscapeString(readURL(i, "")), swapAttrs(readURL(i, "")), cur, html.EscapeString(t))
	}
	toc.WriteString(`</ol></details>`)

	nav := func(i int, label string) string {
		if i < 0 || i >= len(b.spine) {
			return ""
		}
		icon := "fa-solid fa-chevron-right"
		if i < n {
			icon = "fa-solid fa-chevron-left"
		}
		return string(webui.IconTextButton(icon, label, webui.BtnOpts{Kind: "secondary", Href: readURL(i, ""), Attrs: swapAttrs(readURL(i, ""))}))
	}
	fmt.Fprintf(w, `<div class="rd-bar">%s<span class="hint rd-pos" id="rd-pos" role="status">%s</span><span class="hint" id="rd-status" role="status"></span>%s</div>`,
		nav(n-1, ui.Prev), html.EscapeString(fill(ui.PercentRead, "percent", fmt.Sprintf("%.0f", p0))), nav(n+1, ui.Next))
	fmt.Fprint(w, toc.String())
	attrs := map[string]string{
		"book": md5, "chapter": fmt.Sprint(n), "title": e.Title, "authors": e.Authors, "chaptertitle": chTitle,
		"p0": fmt.Sprintf("%.4f", p0), "p1": fmt.Sprintf("%.4f", p1), "frac": fmt.Sprintf("%.4f", frac),
		"progress-url": booksPath + "/read/progress", "highlight-url": booksPath + "/read/highlight",
		"pctlabel": ui.PercentRead, "saved": ui.QuoteSaved, "failed": ui.QuoteFailed,
	}
	var da strings.Builder
	for k, v := range attrs {
		fmt.Fprintf(&da, ` data-%s="%s"`, k, html.EscapeString(v))
	}
	fmt.Fprintf(w, `<article class="rd-text" id="rd-text"%s><h2>%s</h2>%s</article>`, da.String(), html.EscapeString(chTitle), body)
	fmt.Fprintf(w, `<div class="rd-quote" id="rd-quote">%s</div>`,
		webui.IconTextButton("fa-solid fa-quote-left", ui.SaveQuote, webui.BtnOpts{Kind: "primary", Type: "button"}))
	fmt.Fprint(w, readerReadJS, `</div>`)
	p.End(w)
}

// serveBookRes answers an image inside a book. Only images the book's own manifest lists are
// served, and with a policy that lets nothing in them run.
func (a *app) serveBookRes(w http.ResponseWriter, r *http.Request) {
	caller, ok := a.webGate(w, r)
	if !ok {
		return
	}
	tk := tenant(caller)
	s, c := a.loadSource(r.Context(), tk), a.loadCatalog(r.Context(), tk)
	e := findEntry(c, r.URL.Query().Get("id"))
	if s == nil || e == nil {
		http.NotFound(w, r)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	b, _, _, err := a.openBook(ctx, tk, s, e)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer b.Close()
	it, ok := b.items[path.Clean(r.URL.Query().Get("p"))]
	if !ok || !strings.HasPrefix(it.Type, "image/") {
		http.NotFound(w, r)
		return
	}
	data, err := b.read(it.Href)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", it.Type)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; sandbox")
	w.Header().Set("Cache-Control", "private, max-age=3600")
	_, _ = w.Write(data)
}

// readCall runs a capability handler for a JSON body posted by the reader page.
func (a *app) readCall(w http.ResponseWriter, r *http.Request, h func(context.Context, string, dblib.TenantKey, map[string]any) map[string]any) {
	caller, ok := a.webGate(w, r)
	if !ok {
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	var meta map[string]any
	if err := json.NewDecoder(r.Body).Decode(&meta); err != nil || meta == nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	out := h(r.Context(), caller, tenant(caller), meta)
	if out["error"] != nil {
		writeJSON(w, http.StatusUnprocessableEntity, out)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (a *app) serveReadProgress(w http.ResponseWriter, r *http.Request) {
	a.readCall(w, r, a.handleProgressSet)
}

func (a *app) serveReadHighlight(w http.ResponseWriter, r *http.Request) {
	a.readCall(w, r, a.handleHighlightAdd)
}
