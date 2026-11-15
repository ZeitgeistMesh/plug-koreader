package main

// readfull.go — the reader's distraction-free modes (owner, plan 26856ccd).
//
// Maximise, the button in the reader's bar, hides everything but the page while the browser stays a
// window. A small Previous / Full screen / Restore / Next strip, fixed to the foot and see-through until
// you hover over it or tap the page, turns pages and leaves; its Full screen button takes the maximised
// view to true full screen (the browser's own), and leaving true full screen lands back on the maximised
// view. Restore, or Esc in the windowed view, returns to the normal page. The same for an EPUB and a PDF.
//
// Maximised is a class on <html> (rd-full), true full screen the Fullscreen API on <html> too (and the
// class rd-true), never on #rd-shell: an EPUB chapter swaps #rd-shell in place, which would drop the
// browser out of full screen at every page turn. Where the API is missing (an iPhone's Safari) the
// Full screen button is left out and Maximise alone works. Reading position saving is the reader
// scripts' own and does not change.

import (
	"html"

	"xf/controller/pkg/webui"
)

// Copy, written by Opus.
const (
	maximise           = "Maximise"
	maximiseTip        = "Fills the window with just the page. Esc puts everything back."
	maximiseRestore    = "Restore"
	fullScreen         = "Full screen"
	fullScreenExit     = "Leave full screen"
	fullScreenControls = "Page controls"
)

const readerFullCSS = `
.rd-fs{display:none}
html.rd-full body>*:not(main){display:none!important}
html.rd-full main{padding:0!important;margin:0!important;max-width:none!important}
html.rd-full .rd-shell{height:100dvh!important;min-height:0}
html.rd-full .rd-bar,html.rd-full .rd-toc{display:none}
html.rd-full .rd-shell>.rd-text{padding-block:var(--sp-4)}
html.rd-full .rd-fs{display:flex;position:fixed;z-index:30;left:50%;bottom:0;transform:translateX(-50%);width:min(36rem,100%);box-sizing:border-box;
  justify-content:center;gap:var(--sp-3);padding:var(--sp-4) var(--sp-3);background:linear-gradient(to top,var(--bg) 40%,transparent);opacity:0;transition:opacity .2s}
html.rd-full .rd-fs:hover,html.rd-full .rd-fs:focus-within,html.rd-full .rd-fs.on{opacity:1}
.rd-fs .rd-fs-leave,html.rd-true .rd-fs .rd-fs-enter{display:none}
html.rd-true .rd-fs .rd-fs-leave{display:inline-flex}
`

// readerFullJS is loaded once per page (guarded, as the chapter swaps re-run nothing but the reader's own
// script): one delegated click handler, so the buttons that a swap redraws keep working.
const readerFullJS = `<script>
(function(){
if(window.__rdFull)return;window.__rdFull=1;
var root=document.documentElement,tm=null;
function on(){return root.classList.contains('rd-full');}
function refit(){if(window.__rdFit)window.__rdFit();window.dispatchEvent(new Event('resize'));}
function set(v){root.classList.toggle('rd-full',v);if(!v)root.classList.remove('rd-true');document.querySelectorAll('[data-rd-full=toggle]').forEach(function(b){b.setAttribute('aria-pressed',v?'true':'false');});setTimeout(refit,60);}
function enterFs(){if(root.requestFullscreen)root.requestFullscreen().catch(function(){});}
function leaveFs(){if(document.fullscreenElement&&document.exitFullscreen)document.exitFullscreen().catch(function(){});}
if(!root.requestFullscreen)document.querySelectorAll('.rd-fs-enter,.rd-fs-leave').forEach(function(b){b.remove();});
document.addEventListener('click',function(e){
var b=e.target.closest('[data-rd-full]');
if(b){e.preventDefault();var k=b.dataset.rdFull;
if(k==='enterfs'){set(true);enterFs();}
else if(k==='leavefs'){leaveFs();}
else if(k==='exit'||(k==='toggle'&&on())){set(false);leaveFs();}
else{set(true);}
return;}
var nv=e.target.closest('[data-rd-nav]');
if(nv){e.preventDefault();var real=document.querySelector(nv.dataset.rdNav==='prev'?'#rd-prev,[data-rd=prev]':'#rd-next,[data-rd=next]');if(real&&!real.disabled)real.click();return;}
if(on()&&e.target.closest('#rd-text')){var s=window.getSelection();if(s&&!s.isCollapsed)return;var c=document.getElementById('rd-fs');if(!c)return;
c.classList.add('on');clearTimeout(tm);tm=setTimeout(function(){c.classList.remove('on');},3000);}
});
document.addEventListener('fullscreenchange',function(){root.classList.toggle('rd-true',!!document.fullscreenElement);setTimeout(refit,60);});
document.addEventListener('keydown',function(e){if(e.key==='Escape'&&on()&&!document.fullscreenElement)set(false);});
})();
</script>`

// fullButton is the reader bar's Maximise button.
func fullButton() string {
	return string(webui.IconTextButton("fa-solid fa-expand", maximise, webui.BtnOpts{Kind: "secondary", Type: "button",
		Attrs: ` id="rd-full" data-rd-full="toggle" aria-pressed="false" data-tip="` + html.EscapeString(maximiseTip) + `"`}))
}

// fullStrip is the small strip the maximised view shows: Previous, Full screen (or Leave full screen once
// there), Restore and Next.
func fullStrip() string {
	b := func(icon, label, class, attrs string) string {
		return string(webui.IconTextButton(icon, label, webui.BtnOpts{Kind: "secondary", Size: "sm", Type: "button", Class: class, Attrs: attrs}))
	}
	return `<div class="rd-fs" id="rd-fs" role="toolbar" aria-label="` + html.EscapeString(fullScreenControls) + `">` +
		b("fa-solid fa-chevron-left", ui.Prev, "", ` data-rd-nav="prev"`) +
		b("fa-solid fa-expand", fullScreen, "rd-fs-enter", ` data-rd-full="enterfs"`) +
		b("fa-solid fa-compress", fullScreenExit, "rd-fs-leave", ` data-rd-full="leavefs"`) +
		b("fa-solid fa-down-left-and-up-right-to-center", maximiseRestore, "", ` data-rd-full="exit"`) +
		b("fa-solid fa-chevron-right", ui.Next, "", ` data-rd-nav="next"`) + `</div>`
}
