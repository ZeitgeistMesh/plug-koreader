package main

// pdfread.go — /books/read for a PDF: the same reader page as an EPUB (the toolbar with Previous and
// Next, the position, the contents, the save-a-quote button, the place saved as you read), drawn by
// pdf.js in the browser instead of by chapterHTML on the server.
//
// A PDF has fixed pages, so a "chapter" here is a page: the position saved is the page and how far
// down it you are, and the percentage is across all pages, which is also what a KOReader device
// sends, so a place made on either is found on the other. What a PDF cannot do the way an EPUB does:
// the text does not reflow or change size, and a scanned PDF with no text in it has nothing to
// select, so it can be read but not quoted.
//
// pdf.js is a pinned, versioned file from cdnjs; if it cannot load, the page says so and offers the
// download, rather than an empty box.

import (
	"context"
	"fmt"
	"html"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"xf/controller/pkg/dblib"
	"xf/controller/pkg/webui"
)

const (
	pdfJSVersion = "3.11.174"
	pdfJSURL     = "https://cdnjs.cloudflare.com/ajax/libs/pdf.js/" + pdfJSVersion + "/pdf.min.js"
	pdfWorkerURL = "https://cdnjs.cloudflare.com/ajax/libs/pdf.js/" + pdfJSVersion + "/pdf.worker.min.js"
)

// pdfFile is the PDF of a catalog entry, with the index the file cache names it by.
func pdfFile(e *CatalogEntry) (int, bool) {
	for i, f := range e.Files {
		if f.Type == "application/pdf" {
			return i, true
		}
	}
	return 0, false
}

// readableFile is whether the browser reader can open the entry: an EPUB it reads itself, a PDF
// through pdf.js.
func readableFile(e *CatalogEntry) bool {
	if _, ok := epubFile(e); ok {
		return true
	}
	_, ok := pdfFile(e)
	return ok
}

const readerPDFCSS = `
.rd-text.rd-pdf{max-width:60rem;line-height:normal;font-size:1rem}
.rd-pdfpage{position:relative;margin-inline:auto;width:max-content;max-width:100%}
.rd-pdfpage canvas{display:block;max-width:100%;background:#fff;box-shadow:0 0 0 1px var(--border)}
.rd-pdfpage .rd-textlayer{position:absolute;inset:0;overflow:hidden;line-height:1;text-size-adjust:none}
.rd-pdfpage .rd-textlayer span,.rd-pdfpage .rd-textlayer br{color:transparent;position:absolute;white-space:pre;cursor:text;transform-origin:0 0}
.rd-pdfpage .rd-textlayer ::selection{background:color-mix(in srgb,var(--accent) 35%,transparent)}
.rd-toc[hidden]{display:none}
.rd-toc li[data-depth="1"]{margin-inline-start:var(--sp-4)}
.rd-toc li[data-depth="2"]{margin-inline-start:var(--sp-6)}
.rd-toc a{cursor:pointer}
`

// readerPDFJS draws one page at a time, keeps the place as the page scrolls and as the page turns,
// and offers a selected passage to the same highlight route the EPUB reader uses.
const readerPDFJS = `<script>
(function(){
var t=document.getElementById('rd-text');if(!t||!window.pdfjsLib){var m=document.getElementById('rd-pdfmsg');if(m)m.hidden=false;return;}
var d=t.dataset,box=document.getElementById('rd-pdfpage'),pos=document.getElementById('rd-pos'),st=document.getElementById('rd-status'),qb=document.getElementById('rd-quote');
var prev=document.getElementById('rd-prev'),next=document.getElementById('rd-next'),toc=document.getElementById('rd-toc'),msg=document.getElementById('rd-pdfmsg');
function fit(){var s=document.getElementById('rd-shell');if(!s)return;s.style.setProperty('--rd-chrome','0px');
var top=s.getBoundingClientRect().top+window.scrollY,below=document.documentElement.scrollHeight-top-s.offsetHeight;
s.style.setProperty('--rd-chrome',Math.max(top+below,0)+'px');}
if(window.__rdFit)window.removeEventListener('resize',window.__rdFit);window.__rdFit=fit;window.addEventListener('resize',fit);fit();
if(document.readyState!=='complete')window.addEventListener('load',fit);setTimeout(fit,0);
pdfjsLib.GlobalWorkerOptions.workerSrc=d.worker;
var doc=null,N=0,cur=0,timer=null,lastKey='',token=0,wantFrac=0,opened=false;
function room(){return Math.max(t.scrollHeight-t.clientHeight,1);}
function now(){return Math.min(Math.max(t.scrollTop/room(),0),1);}
function pct(f){return N?Math.min(100,(cur+f)/N*100):0;}
function show(f){if(pos)pos.textContent=d.pctlabel.replace('{percent}',pct(f).toFixed(0));if(prev)prev.disabled=cur<=0;if(next)next.disabled=cur>=N-1;}
function post(url,obj,keep){return fetch(url,{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify(obj),credentials:'same-origin',keepalive:!!keep});}
function send(){if(!N)return;var f=now(),key=cur+':'+f.toFixed(3);if(key===lastKey)return;lastKey=key;
post(d.progressUrl,{book:d.book,chapter:cur,frac:f,percent:pct(f),title:d.title},true).catch(function(){});}
function later(){show(now());clearTimeout(timer);timer=setTimeout(send,1500);}
function fail(){if(msg)msg.hidden=false;if(box)box.hidden=true;}
function draw(n,frac){var my=++token;wantFrac=frac||0;
doc.getPage(n+1).then(function(pg){if(my!==token)return;
var w=Math.max(t.clientWidth-parseFloat(getComputedStyle(t).paddingInline)*2,200),v0=pg.getViewport({scale:1}),sc=w/v0.width,vp=pg.getViewport({scale:sc}),r=window.devicePixelRatio||1;
var cv=document.createElement('canvas');cv.width=Math.floor(vp.width*r);cv.height=Math.floor(vp.height*r);cv.style.width=Math.floor(vp.width)+'px';cv.style.height=Math.floor(vp.height)+'px';
var tl=document.createElement('div');tl.className='rd-textlayer';tl.style.setProperty('--scale-factor',vp.scale);
return pg.render({canvasContext:cv.getContext('2d'),viewport:vp,intent:document.hidden?'print':'display',transform:r!==1?[r,0,0,r,0,0]:null}).promise.then(function(){
if(my!==token)return;box.replaceChildren(cv,tl);
return pg.getTextContent().then(function(tc){if(my!==token)return;pdfjsLib.renderTextLayer({textContentSource:tc,container:tl,viewport:vp,textDivs:[]});});
}).then(function(){if(my!==token)return;t.scrollTop=wantFrac*room();show(now());if(!opened){opened=true;lastKey=cur+':'+now().toFixed(3);}else send();});
}).catch(function(){if(my===token)fail();});}
function go(n,frac){if(n<0||n>=N)return;cur=n;t.dataset.chapter=String(n);t.dataset.chaptertitle=d.pagelabel.replace('{n}',String(n+1));draw(n,frac||0);show(frac||0);
document.querySelectorAll('#rd-toc a[aria-current]').forEach(function(a){a.removeAttribute('aria-current');});
if(msg)msg.hidden=true;}
if(prev)prev.addEventListener('click',function(){go(cur-1,0);});
if(next)next.addEventListener('click',function(){go(cur+1,0);});
t.addEventListener('scroll',later,{passive:true});
if(window.__rdPageHide)window.removeEventListener('pagehide',window.__rdPageHide);
window.__rdPageHide=function(){send();};window.addEventListener('pagehide',window.__rdPageHide);
if(window.__rdVis)document.removeEventListener('visibilitychange',window.__rdVis);window.__rdVis=function(){if(document.hidden)send();};document.addEventListener('visibilitychange',window.__rdVis);
var rt=null;window.addEventListener('resize',function(){clearTimeout(rt);rt=setTimeout(function(){if(doc)draw(cur,now());},250);});
function outline(items,depth,ol){(items||[]).forEach(function(it){
var li=document.createElement('li');li.setAttribute('data-depth',String(Math.min(depth,2)));var a=document.createElement('a');a.textContent=it.title||'';
a.addEventListener('click',function(e){e.preventDefault();pageOf(it.dest).then(function(i){if(i>=0)go(i,0);});});li.appendChild(a);ol.appendChild(li);outline(it.items,depth+1,ol);});}
function pageOf(dest){if(!dest)return Promise.resolve(-1);
var p=typeof dest==='string'?doc.getDestination(dest):Promise.resolve(dest);
return p.then(function(a){if(!a)return -1;var ref=a[0];return typeof ref==='object'?doc.getPageIndex(ref):ref;}).catch(function(){return -1;});}
pdfjsLib.getDocument({url:d.fileUrl}).promise.then(function(pdf){
doc=pdf;N=pdf.numPages;var start=parseInt(d.page,10),frac=parseFloat(d.frac)||0;
if(isNaN(start)||start<0){var p=parseFloat(d.percent)||0;var x=p/100*N;start=Math.min(Math.floor(x),N-1);frac=x-Math.floor(x);}
if(start>=N){start=N-1;frac=0;}
go(start,frac);
pdf.getOutline().then(function(o){if(!o||!o.length||!toc)return;var ol=toc.querySelector('ol');outline(o,0,ol);toc.hidden=false;}).catch(function(){});
},function(){fail();});
var sel='';
if(window.__rdSelect)document.removeEventListener('selectionchange',window.__rdSelect);
window.__rdSelect=function(){
var s=window.getSelection();if(!s||s.isCollapsed||!t.contains(s.anchorNode)){qb.classList.remove('on');return;}
sel=s.toString().trim();if(!sel){qb.classList.remove('on');return;}
var r=s.getRangeAt(0).getBoundingClientRect();qb.style.top=Math.max(r.top-48,8)+'px';qb.style.left=Math.max(Math.min(r.left,window.innerWidth-160),8)+'px';qb.classList.add('on');};
document.addEventListener('selectionchange',window.__rdSelect);
qb.addEventListener('mousedown',function(e){e.preventDefault();});
qb.addEventListener('click',function(){var text=sel;if(!text)return;
post(d.highlightUrl,{book:d.book,text:text,chapter:t.dataset.chaptertitle,title:d.title,authors:d.authors}).then(function(r){
st.textContent=r.ok?d.saved:d.failed;if(r.ok){window.getSelection().removeAllRanges();qb.classList.remove('on');}}).catch(function(){st.textContent=d.failed;});});
})();
</script>`

// servePDFRead is the reader page for an entry that has a PDF and no EPUB.
func (a *app) servePDFRead(w http.ResponseWriter, r *http.Request, tk dblib.TenantKey, s *Source, e *CatalogEntry, fidx int) {
	key := entryKey(e.ID)
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	p := a.page(booksPath)
	p.CSS += readerBooksCSS + readerReadCSS + readerPDFCSS
	path, err := a.fetchToCache(ctx, tk, s, key+"-"+fmt.Sprint(fidx)+".pdf", e.Files[fidx].Href)
	var md5 string
	if err == nil {
		md5, err = koPartialMD5(path)
	}
	if err != nil {
		p.StartFor(w, r, e.Title)
		fmt.Fprintf(w, `<p class="hint rd-status" role="alert">%s</p>`, html.EscapeString(fill(ui.ErrOpen, "why", err.Error())))
		p.End(w)
		return
	}

	// Where the person left off: a page and how far down it, if this browser made the place; only a
	// percentage if a device did, which the page turns into a page once it knows how many there are.
	page, frac, percent := "-1", 0.0, 0.0
	if pr := a.loadProgress(ctx, tk, md5); pr != nil {
		if pr.Device == webDeviceName {
			page, frac = strconv.Itoa(pr.Chapter), pr.Frac
		}
		percent = pr.Percent
	}
	if q := r.URL.Query().Get("n"); q != "" {
		if v, perr := strconv.Atoi(q); perr == nil && v >= 0 {
			page, frac = strconv.Itoa(v), 0
		}
	}
	fileURL := fmt.Sprintf("%s/file?id=%s&f=%d", booksPath, url.QueryEscape(key), fidx)
	attrs := map[string]string{
		"book": md5, "chapter": "0", "title": e.Title, "authors": e.Authors, "chaptertitle": fill(ui.PdfPage, "n", "1"),
		"page": page, "frac": fmt.Sprintf("%.4f", frac), "percent": fmt.Sprintf("%.2f", percent),
		"file-url": fileURL, "worker": pdfWorkerURL, "pagelabel": ui.PdfPage,
		"progress-url": booksPath + "/read/progress", "highlight-url": booksPath + "/read/highlight",
		"pctlabel": ui.PercentRead, "saved": ui.QuoteSaved, "failed": ui.QuoteFailed,
	}
	var da strings.Builder
	for k, v := range attrs {
		fmt.Fprintf(&da, ` data-%s="%s"`, k, html.EscapeString(v))
	}
	btn := func(id, icon, label string) string {
		return string(webui.IconTextButton(icon, label, webui.BtnOpts{Kind: "secondary", Type: "button", Attrs: ` id="` + id + `"`}))
	}
	p.StartFor(w, r, e.Title)
	fmt.Fprint(w, `<div class="rd-shell" id="rd-shell">`)
	fmt.Fprintf(w, `<div class="rd-bar">%s<span class="hint rd-pos" id="rd-pos" role="status">%s</span><span class="hint" id="rd-status" role="status"></span>%s</div>`,
		btn("rd-prev", "fa-solid fa-chevron-left", ui.Prev), html.EscapeString(fill(ui.PercentRead, "percent", fmt.Sprintf("%.0f", percent))),
		btn("rd-next", "fa-solid fa-chevron-right", ui.Next))
	fmt.Fprintf(w, `<details class="rd-toc" id="rd-toc" hidden><summary>%s</summary><ol></ol></details>`, html.EscapeString(ui.ContentsHeading))
	fmt.Fprintf(w, `<article class="rd-text rd-pdf" id="rd-text"%s><p class="hint" id="rd-pdfmsg" role="alert" hidden>%s <a href="%s" hx-boost="false">%s</a></p><div class="rd-pdfpage" id="rd-pdfpage"><p class="hint">%s</p></div></article>`,
		da.String(), html.EscapeString(ui.PdfCantDraw), html.EscapeString(fileURL), html.EscapeString(strings.ToUpper(fileExt("application/pdf"))), html.EscapeString(ui.PdfLoading))
	fmt.Fprintf(w, `<div class="rd-quote" id="rd-quote">%s</div>`,
		webui.IconTextButton("fa-solid fa-quote-left", ui.SaveQuote, webui.BtnOpts{Kind: "primary", Type: "button"}))
	fmt.Fprintf(w, `<script src="%s" onerror="var m=document.getElementById('rd-pdfmsg');if(m)m.hidden=false"></script>`, pdfJSURL)
	fmt.Fprint(w, readerPDFJS, `</div>`)
	p.End(w)
}
