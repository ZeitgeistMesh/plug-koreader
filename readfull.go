package main

// readfull.go — the reader's distraction-free full screen mode (owner, plan 26856ccd): a Full screen
// button in the reader's bar puts the browser in full screen and hides everything but the page; a small
// Previous / Next / exit strip, fixed to the foot and see-through until you hover over it or tap the page,
// turns pages and leaves. The same for an EPUB and a PDF.
//
// The mode is a class on <html> and the Fullscreen API on <html> too, never on #rd-shell: an EPUB chapter
// swaps #rd-shell in place, which would drop the browser out of full screen at every page turn. Where the
// API is missing (an iPhone's Safari) the class alone still hides the chrome. Reading position saving is
// the reader scripts' own and does not change.

import (
	"html"

	"xf/controller/pkg/webui"
)

// Copy, written by Opus.
const (
	fullScreen         = "Full screen"
	fullScreenExit     = "Leave full screen"
	fullScreenTip      = "Hides everything but the page until you press Esc."
	fullScreenControls = "Page controls"
)

const readerFullCSS = `
.rd-fs{display:none}
html.rd-full body>*:not(main){display:none!important}
html.rd-full main{padding:0!important;margin:0!important;max-width:none!important}
html.rd-full .rd-shell{height:100dvh!important;min-height:0}
html.rd-full .rd-bar,html.rd-full .rd-toc{display:none}
html.rd-full .rd-shell>.rd-text{padding-block:var(--sp-4)}
html.rd-full .rd-fs{display:flex;position:fixed;z-index:30;left:50%;bottom:0;transform:translateX(-50%);width:min(30rem,100%);box-sizing:border-box;
  justify-content:center;gap:var(--sp-3);padding:var(--sp-4) var(--sp-3);background:linear-gradient(to top,var(--bg) 40%,transparent);opacity:0;transition:opacity .2s}
html.rd-full .rd-fs:hover,html.rd-full .rd-fs:focus-within,html.rd-full .rd-fs.on{opacity:1}
`

// readerFullJS is loaded once per page (guarded, as the chapter swaps re-run nothing but the reader's own
// script): one delegated click handler, so the buttons that a swap redraws keep working.
const readerFullJS = `<script>
(function(){
if(window.__rdFull)return;window.__rdFull=1;
var root=document.documentElement,tm=null;
function on(){return root.classList.contains('rd-full');}
function refit(){if(window.__rdFit)window.__rdFit();window.dispatchEvent(new Event('resize'));}
function set(v){root.classList.toggle('rd-full',v);document.querySelectorAll('[data-rd-full=toggle]').forEach(function(b){b.setAttribute('aria-pressed',v?'true':'false');});setTimeout(refit,60);}
function enter(){set(true);if(root.requestFullscreen)root.requestFullscreen().catch(function(){});}
function leave(){set(false);if(document.fullscreenElement&&document.exitFullscreen)document.exitFullscreen().catch(function(){});}
document.addEventListener('click',function(e){
var b=e.target.closest('[data-rd-full]');
if(b){e.preventDefault();if(b.dataset.rdFull==='exit'||on())leave();else enter();return;}
var nv=e.target.closest('[data-rd-nav]');
if(nv){e.preventDefault();var real=document.querySelector(nv.dataset.rdNav==='prev'?'#rd-prev,[data-rd=prev]':'#rd-next,[data-rd=next]');if(real&&!real.disabled)real.click();return;}
if(on()&&e.target.closest('#rd-text')){var s=window.getSelection();if(s&&!s.isCollapsed)return;var c=document.getElementById('rd-fs');if(!c)return;
c.classList.add('on');clearTimeout(tm);tm=setTimeout(function(){c.classList.remove('on');},3000);}
});
document.addEventListener('fullscreenchange',function(){if(!document.fullscreenElement&&on())set(false);});
document.addEventListener('keydown',function(e){if(e.key==='Escape'&&on()&&!document.fullscreenElement)set(false);});
})();
</script>`

// fullButton is the reader bar's Full screen button.
func fullButton() string {
	return string(webui.IconTextButton("fa-solid fa-expand", fullScreen, webui.BtnOpts{Kind: "secondary", Type: "button",
		Attrs: ` id="rd-full" data-rd-full="toggle" aria-pressed="false" data-tip="` + html.EscapeString(fullScreenTip) + `"`}))
}

// fullStrip is the small strip full screen shows: Previous, Next and the way out.
func fullStrip() string {
	b := func(icon, label, attrs string) string {
		return string(webui.IconTextButton(icon, label, webui.BtnOpts{Kind: "secondary", Size: "sm", Type: "button", Attrs: attrs}))
	}
	return `<div class="rd-fs" id="rd-fs" role="toolbar" aria-label="` + html.EscapeString(fullScreenControls) + `">` +
		b("fa-solid fa-chevron-left", ui.Prev, ` data-rd-nav="prev"`) +
		b("fa-solid fa-compress", fullScreenExit, ` data-rd-full="exit"`) +
		b("fa-solid fa-chevron-right", ui.Next, ` data-rd-nav="next"`) + `</div>`
}
