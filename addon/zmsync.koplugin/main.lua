--[[
zmsync — send KOReader's reading statistics to your own Zeitgeist mesh.

WHAT THIS READS, AND WHAT IT NEVER WRITES
-----------------------------------------
It opens KOReader's own statistics database READ-ONLY and never writes to it.
KOReader owns that file; a plugin that writes to it is a plugin that can corrupt a
reading history nobody can reconstruct.

WHY IT REPORTS ITS OWN SCHEMA
-----------------------------
The statistics schema has moved between KOReader versions — the per-page table has
been `page_stat` and `page_stat_data`, with a compatibility VIEW at various points —
and annotations moved from separate `highlight`/`bookmarks` tables into a unified
`annotations` table in the sidecar. Guessing produces a plugin that silently syncs
nothing, which is the worst possible failure: it looks like "you have not read
anything".

So this detects what it is actually looking at and SENDS THAT with every sync. The
first sync's real job is to tell the server what this device is.

THE CURSOR IS WHAT THE SERVER ACKNOWLEDGED
------------------------------------------
Not what we read. An acknowledgement that never arrived must cost a resend, not a
gap — and because every event id is derived from its own content, a resend is free.
]]

local DataStorage = require("datastorage")
local Dispatcher = require("dispatcher")
local InfoMessage = require("ui/widget/infomessage")
local InputDialog = require("ui/widget/inputdialog")
local UIManager = require("ui/uimanager")
local WidgetContainer = require("ui/widget/container/widgetcontainer")
local LuaSettings = require("luasettings")
local logger = require("logger")
local md5 = require("ffi/sha2").md5
local _ = require("gettext")
local T = require("ffi/util").template

local ZmSync = WidgetContainer:extend{
    name = "zmsync",
    is_doc_only = false,
}

local SETTINGS_FILE = DataStorage:getSettingsDir() .. "/zmsync.lua"
local DEFAULT_SERVER = "https://koreader.zm.is"
local BATCH = 500

-- ANNOTATIONS_CAP bounds one annotation sweep. Highlights are counted in hundreds, not
-- tens of thousands like page rows, so the whole set travels on every sync rather than
-- being paged behind a cursor — see collectAnnotations for why that is the right trade.
local ANNOTATIONS_CAP = 5000

-- The capture budget: a TIME limit with a generous count behind it.
--
-- PAGE_BUDGET was 12, picked before there was any evidence of what a capture costs. There
-- now is: a real sync captured 12 of 12 with no complaint from the reader. A count was the
-- wrong shape anyway — twelve scanned PDFs and twelve small epubs are not the same work —
-- so the limit is now the thing actually being protected, which is how long the reader is
-- unresponsive. The count remains as a backstop against a pathological book returning
-- instantly and looping.
local PAGE_SECONDS = 45
local PAGE_BUDGET = 200


function ZmSync:init()
    self.settings = LuaSettings:open(SETTINGS_FILE)
    self.ui.menu:registerToMainMenu(self)
    self:onDispatcherRegisterActions()
end

function ZmSync:onDispatcherRegisterActions()
    Dispatcher:registerAction("zmsync_now",
        { category = "none", event = "ZmSyncNow", title = _("Sync reading to Zeitgeist"), general = true })
end

function ZmSync:addToMainMenu(menu_items)
    menu_items.zmsync = {
        text = _("Zeitgeist reading sync"),
        sorting_hint = "tools",
        sub_item_table = {
            {
                text_func = function()
                    if self:isPaired() then
                        return T(_("Paired as: %1"), self.settings:readSetting("device_name") or _("this device"))
                    end
                    return _("Pair this device…")
                end,
                keep_menu_open = true,
                callback = function() self:showPairDialog() end,
            },
            {
                text = _("Sync now"),
                keep_menu_open = true,
                enabled_func = function() return self:isPaired() end,
                callback = function() self:syncNow(true) end,
            },
            {
                -- THE ESCAPE HATCH FOR A CURSOR THAT IS AHEAD OF THE DATA.
                --
                -- The cursor only moves forward, which is what stops an empty or retried
                -- batch restarting the history. The cost is that a cursor which got ahead
                -- of itself — a server bug, a restored backup, a device whose statistics
                -- database was replaced — can never come back on its own, and the reading
                -- below it is unreachable for ever.
                --
                -- Re-sending is CHEAP AND SAFE because every id is derived from its own
                -- content: the server recognises what it already holds and counts it as a
                -- duplicate. So the recovery is simply "ask again from the beginning",
                -- and it needs to be a button rather than a hand-edited settings file.
                text = _("Re-sync all reading"),
                keep_menu_open = true,
                enabled_func = function() return self:isPaired() end,
                callback = function()
                    self.settings:saveSetting("high_water", 0)
                    self.settings:flush()
                    UIManager:show(InfoMessage:new{
                        text = _("Sending your whole reading history again. Anything already stored is recognised and skipped."),
                        timeout = 6 })
                    self:syncNow(true)
                end,
            },
            {
                text_func = function() return self:statusLine() end,
                keep_menu_open = true,
                callback = function() self:showStatus() end,
            },
            {
                text = _("Server…"),
                keep_menu_open = true,
                callback = function() self:showServerDialog() end,
            },
        },
    }
end

-- ── state ────────────────────────────────────────────────────────────────────

function ZmSync:server()
    return self.settings:readSetting("server") or DEFAULT_SERVER
end

function ZmSync:token()
    return self.settings:readSetting("token")
end

function ZmSync:isPaired()
    local t = self:token()
    return t ~= nil and t ~= ""
end

-- statusLine is deliberately specific. "Last sync failed" with no reason sends
-- somebody to the wrong problem; the two that actually happen are "no network" and
-- "this device was revoked", and they need different actions.
function ZmSync:statusLine()
    if not self:isPaired() then return _("Not paired") end
    local last = self.settings:readSetting("last_result")
    if not last then return _("Never synced") end
    return T(_("Last sync: %1"), last)
end

function ZmSync:showStatus()
    local lines = {
        T(_("Server: %1"), self:server()),
        T(_("Paired: %1"), self:isPaired() and _("yes") or _("no")),
        T(_("Statistics: %1"), self:describeStats()),
        T(_("Cursor: %1"), tostring(self.settings:readSetting("high_water") or 0)),
        T(_("Last sync: %1"), self.settings:readSetting("last_result") or _("never")),
    }
    UIManager:show(InfoMessage:new{ text = table.concat(lines, "\n"), timeout = 12 })
end

-- ── pairing ──────────────────────────────────────────────────────────────────

function ZmSync:showPairDialog()
    local dialog
    dialog = InputDialog:new{
        title = _("Pair with Zeitgeist"),
        description = T(_("Open %1/pair on a computer or phone, then type the six-character code here."), self:server()),
        input = "",
        input_hint = "ABC123",
        buttons = {{
            { text = _("Cancel"), id = "close", callback = function() UIManager:close(dialog) end },
            {
                text = _("Pair"),
                is_enter_default = true,
                callback = function()
                    local code = dialog:getInputText()
                    UIManager:close(dialog)
                    self:pair(code)
                end,
            },
        }},
    }
    UIManager:show(dialog)
    dialog:onShowKeyboard()
end

function ZmSync:pair(code)
    local body = { code = code, device_name = self:deviceName() }
    local ok, res = self:post("/api/pair", body, nil)
    if not ok then
        UIManager:show(InfoMessage:new{ text = T(_("Pairing failed: %1"), res or _("no reply")) })
        return
    end
    if not res.token then
        UIManager:show(InfoMessage:new{ text = res.message or _("Pairing failed.") })
        return
    end
    self.settings:saveSetting("token", res.token)
    self.settings:saveSetting("device_name", res.device_name or self:deviceName())
    self.settings:flush()
    UIManager:show(InfoMessage:new{ text = _("Paired. Syncing your reading now…") })
    self:syncNow(true)
end

function ZmSync:showServerDialog()
    local dialog
    dialog = InputDialog:new{
        title = _("Zeitgeist server"),
        input = self:server(),
        buttons = {{
            { text = _("Cancel"), id = "close", callback = function() UIManager:close(dialog) end },
            {
                text = _("Save"),
                is_enter_default = true,
                callback = function()
                    local v = dialog:getInputText()
                    UIManager:close(dialog)
                    if v and v ~= "" then
                        self.settings:saveSetting("server", (v:gsub("/+$", "")))
                        -- CHANGING THE SERVER UNPAIRS. A token minted by one server
                        -- is meaningless to another, and keeping it would produce
                        -- 401s that look like a revoked device.
                        self.settings:delSetting("token")
                        self.settings:flush()
                        UIManager:show(InfoMessage:new{ text = _("Server changed. Pair this device again.") })
                    end
                end,
            },
        }},
    }
    UIManager:show(dialog)
    dialog:onShowKeyboard()
end

-- ── the statistics database ──────────────────────────────────────────────────

function ZmSync:statsPath()
    return DataStorage:getSettingsDir() .. "/statistics.sqlite3"
end

-- openStats returns a read-only handle, or nil.
function ZmSync:openStats()
    local ok, SQ3 = pcall(require, "lua-ljsqlite3/init")
    if not ok then
        logger.warn("zmsync: no sqlite binding available")
        return nil
    end
    local path = self:statsPath()
    local lfs = require("libs/libkoreader-lfs")
    if not lfs.attributes(path, "mode") then
        -- LOGGED, because this returned nil silently. A missing statistics database and
        -- a working one with nothing new to report both ended as an empty batch and a
        -- cheerful "Synced.", so the one arrangement that can never work looked fine.
        logger.warn("zmsync: no statistics database at", path)
        return nil
    end
    local conn_ok, conn = pcall(SQ3.open, path, "ro")
    if not conn_ok then
        logger.warn("zmsync: cannot open statistics:", conn)
        return nil
    end
    return conn
end

-- pageTable finds the per-page table THIS device actually has.
--
-- Ordered most-specific first. `page_stat` exists as a compatibility VIEW on newer
-- versions, so preferring it would work everywhere and silently read through a view
-- on the very versions we most want to identify.
function ZmSync:pageTable(conn)
    for _, name in ipairs({ "page_stat_data", "page_stat" }) do
        local ok, rows = pcall(function()
            return conn:exec("SELECT name FROM sqlite_master WHERE name = '" .. name .. "' LIMIT 1")
        end)
        if ok and rows and rows[1] and rows[1][1] then
            return name
        end
    end
    return nil
end

-- describeStats is what gets reported to the server on every sync. This is the
-- single most valuable thing the first slice produces: it answers, from a REAL
-- device, which schema the reader on the sofa is actually writing.
function ZmSync:describeStats()
    local conn = self:openStats()
    if not conn then return "absent" end
    local t = self:pageTable(conn)
    local cols = {}
    if t then
        local ok, rows = pcall(function() return conn:exec("PRAGMA table_info(" .. t .. ")") end)
        if ok and rows and rows[2] then
            for _, name in ipairs(rows[2]) do cols[#cols + 1] = name end
        end
    end
    conn:close()
    if not t then return "no page table" end
    return t .. "(" .. table.concat(cols, ",") .. ")"
end

-- asArray marks a table so the encoder emits `[]` rather than `{}`.
--
-- LuaJSON decides array-vs-object by inspecting the table, and an EMPTY Lua table is
-- ambiguous — it has no sequential keys, so it encodes as `{}`. The server's `events`
-- is a []ReadEvent, so an empty sync arrived as `"events":{}` and the whole batch was
-- refused with "cannot unmarshal object into Go struct field ingestRequest.events".
--
-- That broke the heartbeat precisely where it was designed to matter: a sync with
-- nothing to send is the one that proves the device is alive and reports its schema,
-- and it was the only kind of sync that could never succeed. Measured on the device:
-- JSON.encode({}) is "{}", JSON.encode(InitArray({})) is "[]".
--
-- Lua cannot express "empty list" without a marker, so EVERY return path that yields
-- events must go through this — which is why collect() has no bare `{}` left in it.
local function asArray(t)
    return require("json").util.InitArray(t)
end

-- collect reads events strictly AFTER the acknowledged cursor.
--
-- The second return value is the SCHEMA STRING, and it doubles as the reason when no
-- events come back. It reaches the server on every sync, so "absent", "no page table"
-- and "<table> (query failed)" are distinguishable there from an honest empty batch —
-- which is the whole point, because from the sofa they all look like "Synced.".
function ZmSync:collect(since)
    local conn = self:openStats()
    if not conn then return asArray({}), "absent" end
    local t = self:pageTable(conn)
    if not t then conn:close(); return asArray({}), "no page table" end

    -- Joined to `book` so each event carries the title and the md5 that identifies
    -- the book across renames. LIMIT because a first sync from a long history is
    -- genuinely large and the server pages rather than refusing.
    local sql = ([[
        SELECT b.md5, b.title, b.authors, b.pages, s.page, s.start_time, s.duration
        FROM %s s JOIN book b ON b.id = s.id_book
        WHERE s.start_time > %d
        ORDER BY s.start_time ASC
        LIMIT %d
    ]]):format(t, tonumber(since) or 0, BATCH)

    local ok, rows = pcall(function() return conn:exec(sql) end)
    conn:close()
    -- A FAILED QUERY AND AN EMPTY ONE ARE NOT THE SAME ANSWER and were collapsed into
    -- one branch here. "Everything is already acknowledged" is the normal steady state;
    -- "the query threw" means this device has never synced a row and never will. Both
    -- produced an identical empty batch, so the failure was invisible.
    if not ok then
        logger.warn("zmsync: statistics query failed:", rows)
        return asArray({}), t .. " (query failed)"
    end
    if not rows or not rows[1] then
        return asArray({}), t
    end

    local events = {}
    for i = 1, #rows[1] do
        local bmd5      = rows[1][i] or ""
        local title     = rows[2][i] or ""
        local authors   = rows[3][i] or ""
        local pages     = tonumber(rows[4][i]) or 0
        local page      = tonumber(rows[5][i]) or 0
        local start     = tonumber(rows[6][i]) or 0
        local duration  = tonumber(rows[7][i]) or 0
        events[#events + 1] = {
            -- THE ID IS DERIVED FROM CONTENT, so a resend overwrites itself. The
            -- server deduplicates on exactly this and counts duplicates as normal.
            id = md5(bmd5 .. ":" .. tostring(page) .. ":" .. tostring(start)),
            kind = "read",
            bookmd5 = bmd5, title = title, authors = authors, pages = pages,
            page = page, starttime = start, duration = duration,
        }
    end
    return events, t
end

-- ── annotations ──────────────────────────────────────────────────────────────
--
-- Highlights and bookmarks do NOT live in statistics.sqlite3. They live per-book, in the
-- sidecar KOReader keeps beside each file: `<book>.sdr/metadata.<ext>.lua`, which is a
-- plain Lua table, so reading it is `dofile` rather than a parser.
--
-- TWO SCHEMAS, BOTH LIVE ON ONE DEVICE. Recent KOReader unified highlights and bookmarks
-- into a single `annotations` array; older versions kept `highlight` (a page-keyed map of
-- maps) and `bookmarks` (an array) separately. The migration happens per book, WHEN THAT
-- BOOK IS NEXT OPENED — so a device carries whatever mix its reading history produced,
-- and a reader that handles only one shape silently finds nothing for the rest. Measured
-- on this device: 103 sidecars new, 18 legacy, 8 carrying both.
--
-- `partial_md5_checksum` in the sidecar is the SAME id as `book.md5` in the statistics
-- database, which is what lets an annotation and a reading session name the same book.

-- annotationTime converts KOReader's "YYYY-MM-DD HH:MM:SS" to epoch seconds.
--
-- It is LOCAL time as the device wrote it, and os.time interprets a table as local time,
-- so the two agree. isdst is left nil deliberately: forcing it either way shifts every
-- summer highlight by an hour.
local function annotationTime(s)
    if type(s) ~= "string" then return 0 end
    local y, mo, d, h, mi, sec = s:match("^(%d+)-(%d+)-(%d+)%s+(%d+):(%d+):(%d+)")
    if not y then return 0 end
    return os.time({ year = tonumber(y), month = tonumber(mo), day = tonumber(d),
                     hour = tonumber(h), min = tonumber(mi), sec = tonumber(sec) }) or 0
end

-- annotationKind: an entry carrying highlighted text is a highlight; one that only marks
-- a place is a bookmark. The distinction matters downstream — only a highlight has a
-- quote worth turning into a card.
local function annotationKind(text)
    if type(text) == "string" and text:gsub("%s", "") ~= "" then return "highlight" end
    return "bookmark"
end

-- bookTitle picks a title worth showing: the first of the candidates that is a real one,
-- else the file name without its extension.
--
-- doc_props IS EMPTY FOR A PDF WITH NO METADATA, so annotations from such a book went up
-- with no title at all and every quote from it showed as "Untitled"; the sidecar's
-- `stats.title` and the file name both still have it. A placeholder author slot in a file
-- name ("Unknown - The Slight Edge") is dropped, matching the server's displayTitle.
local function bookTitle(candidates, file)
    candidates = { candidates[1], candidates[2], candidates[3] }
    if type(file) == "string" then
        candidates[4] = ((file:match("([^/]+)$") or file):gsub("%.[^.]*$", ""))
    end
    for i = 1, 4 do
        local c = candidates[i]
        if type(c) == "string" then
            c = c:match("^%s*(.-)%s*$")
            if c ~= "" and c ~= "N/A" then
                local slot, rest = c:match("^(.-)%s+%-%s+(.+)$")
                if slot and ({ unknown = true, ["unknown author"] = true, ["n/a"] = true, anonymous = true })[slot:lower()] then
                    return rest
                end
                return c
            end
        end
    end
    return ""
end

-- sidecarAnnotations flattens ONE sidecar table into a list of {kind,text,note,chapter,
-- page,datetime} regardless of which schema it uses.
local function sidecarAnnotations(t)
    local out = {}
    -- New unified schema: a plain array.
    if type(t.annotations) == "table" then
        for _, a in ipairs(t.annotations) do
            if type(a) == "table" then
                -- A HIGHLIGHT HAS A DRAWER; A BOOKMARK HAS NONE. That is KOReader's own
                -- test, and "has text" is not: a bookmark carries an automatic label,
                -- "in <chapter>", in `text`. Reading that as a quote sent 37 bookmarks as
                -- highlights reading "in Untitled", and their pages were never captured.
                -- A drawn highlight with no text (a scanned PDF) is treated as a bookmark
                -- too, so its page is captured instead of an empty quote.
                local kind = a.drawer and annotationKind(a.text) or "bookmark"
                out[#out + 1] = {
                    kind = kind, text = kind == "highlight" and a.text or "", note = a.note or "",
                    chapter = a.chapter or "", page = a.pageno or a.page or 0,
                    datetime = a.datetime or "",
                }
            end
        end
        return out
    end
    -- Legacy: `highlight` is keyed BY PAGE, each holding a numbered map of entries. Not
    -- an array, so ipairs would walk none of it — this is the shape that silently syncs
    -- nothing if you assume the new one.
    if type(t.highlight) == "table" then
        for page, entries in pairs(t.highlight) do
            if type(entries) == "table" then
                for _, a in pairs(entries) do
                    if type(a) == "table" then
                        out[#out + 1] = {
                            kind = annotationKind(a.text), text = a.text or "", note = a.note or "",
                            chapter = a.chapter or "", page = tonumber(page) or 0,
                            datetime = a.datetime or "",
                        }
                    end
                end
            end
        end
    end
    if type(t.bookmarks) == "table" then
        for _, a in pairs(t.bookmarks) do
            if type(a) == "table" then
                -- A legacy bookmark row duplicates a highlight when it has `highlighted`;
                -- the id is content-derived, so a duplicate costs nothing and dropping it
                -- here would risk losing a note the highlight row does not carry.
                -- The same label trap: a plain legacy bookmark's `notes` is "in <chapter>",
                -- and only a `highlighted` one carries the highlighted text there.
                local hl = a.highlighted or a.pos0 ~= nil
                local kind = hl and annotationKind(a.notes or a.text) or "bookmark"
                out[#out + 1] = {
                    kind = kind, text = kind == "highlight" and (a.notes or a.text or "") or "",
                    note = a.text or "", chapter = a.chapter or "",
                    page = tonumber(a.page) or 0, datetime = a.datetime or "",
                }
            end
        end
    end
    return out
end

-- eachSidecar walks the library for `*.sdr/metadata.*.lua` and calls fn(table).
local function eachSidecar(root, fn)
    local lfs = require("libs/libkoreader-lfs")
    if not root or lfs.attributes(root, "mode") ~= "directory" then return 0 end
    local seen = 0
    local function walk(dir, depth)
        if depth > 8 then return end
        -- BOTH RETURN VALUES. lfs.dir yields an iterator AND the directory object it
        -- walks, and the iterator is useless without its state: capturing only the first
        -- gives "directory metatable expected, got nil" on the first step. Caught by
        -- dry-running this walk on the device before shipping it, which is the only place
        -- the failure appears — there is no directory to walk in a unit test.
        local ok, iter, dirobj = pcall(lfs.dir, dir)
        if not ok or not iter then return end
        for name in iter, dirobj do
            if name ~= "." and name ~= ".." then
                local path = dir .. "/" .. name
                local mode = lfs.attributes(path, "mode")
                if mode == "directory" then
                    walk(path, depth + 1)
                elseif mode == "file" and name:match("^metadata%..*%.lua$") then
                    local good, t = pcall(dofile, path)
                    if good and type(t) == "table" then
                        seen = seen + 1
                        fn(t)
                    end
                end
            end
        end
    end
    walk(root, 1)
    return seen
end

-- bookPaths maps partial_md5_checksum -> the book's file, read out of the sidecars.
-- The sidecar carries both, which is the same join the annotations already rely on.
local function bookPathsFrom(root)
    local paths = {}
    eachSidecar(root, function(t)
        local md5 = t.partial_md5_checksum
        if type(md5) == "string" and md5 ~= "" and type(t.doc_path) == "string" then
            paths[md5] = t.doc_path
        end
    end)
    return paths
end

-- collectAnnotations returns EVERY annotation on the device, every time.
--
-- No cursor, unlike the page rows. Two reasons, and they are worth stating because the
-- asymmetry looks like an oversight:
--
--  * there are hundreds of these, not tens of thousands — the whole set is one small
--    request, where the reading history is forty;
--  * an annotation is EDITABLE. A note added to a highlight months later changes the
--    record without changing its datetime, so a cursor keyed on time would never send it
--    again. Re-sending everything is how the server stays current, and it is free because
--    the id is derived from content: an unchanged annotation is recognised as a duplicate,
--    and a changed one overwrites itself.
function ZmSync:collectAnnotations()
    local root = self:libraryRoot()
    local events, files = {}, 0

    -- A bookmark has no text of its own, so the page it marks is fetched and becomes its
    -- text — see the page-capture section. Resolved once for the whole sweep: the path map
    -- costs a walk of the library and the ledger costs a settings read, and doing either
    -- per bookmark would turn a linear pass into a quadratic one.
    local paths = bookPathsFrom(root)
    local captured = self:capturedPages()
    local budget, newlyCaptured = PAGE_BUDGET, 0
    -- Attempts and the first failure REACH THE SERVER (see syncNow). logger.warn goes
    -- nowhere observable from here — no zmsync line has ever reached crash.log — so a
    -- capture that fails on every book would otherwise be perfectly silent, which is
    -- exactly how this went unnoticed for two syncs.
    local attempted, failed, firstError = 0, 0, nil
    local deadline = os.time() + PAGE_SECONDS

    files = eachSidecar(root, function(t)
        -- NOT named md5: the module-level `md5` is the hash FUNCTION, and shadowing it
        -- here would make the id derivation below call a string.
        local bmd5 = t.partial_md5_checksum
        if type(bmd5) ~= "string" or bmd5 == "" then return end
        local props = type(t.doc_props) == "table" and t.doc_props or {}
        local stats = type(t.stats) == "table" and t.stats or {}
        local title = bookTitle({ props.title, stats.title }, paths[bmd5])
        for _, a in ipairs(sidecarAnnotations(t)) do
            if #events >= ANNOTATIONS_CAP then return end
            local when = annotationTime(a.datetime)
            if when > 0 then
                local text, note = a.text, a.note
                -- A BOOKMARK GETS THE WHOLE PAGE. Only once per (book, page), and only
                -- while the budget holds; whatever is left is picked up next sync.
                if a.kind == "bookmark" and (text == nil or text == "") and a.page and a.page > 0 then
                    local key = bmd5 .. ":" .. tostring(a.page)
                    local held = captured[key]
                    if type(held) == "string" and held ~= "" then
                        -- Captured on an earlier sync: re-send what we sent then, so the
                        -- server sees identical content rather than an erasing blank.
                        text = held
                    elseif held == nil and budget > 0 and os.time() < deadline then
                        budget = budget - 1
                        attempted = attempted + 1
                        local page, err = self:pageText(paths[bmd5], a.page)
                        if page and page ~= "" then
                            text = page
                            captured[key] = page
                            newlyCaptured = newlyCaptured + 1
                        else
                            -- Recorded as done so a page that CANNOT be read is not
                            -- retried on every sync for ever. It costs a book-open each
                            -- time and the answer will not change on its own.
                            captured[key] = false
                            failed = failed + 1
                            if not firstError then firstError = tostring(err) end
                            logger.warn("zmsync: bookmark page", a.page, "of", bmd5, "->", tostring(err))
                        end
                    end
                end
                events[#events + 1] = {
                    -- The plan's FALLBACK id, which turns out to be the only one: a
                    -- KOReader annotation carries no id of its own.
                    id = md5(bmd5 .. ":" .. a.datetime .. ":" .. tostring(a.page)),
                    kind = a.kind,
                    bookmd5 = bmd5, title = title, authors = props.authors or "",
                    page = a.page, starttime = when, duration = 0,
                    text = text, note = note, chapter = a.chapter,
                }
            end
        end
    end)
    -- SAVED ON ANY ATTEMPT, not only on success. Guarding this on newlyCaptured meant a
    -- sync where every capture failed wrote no ledger at all — so the same twelve books
    -- were reopened on the next sync, for ever, and the absent ledger looked exactly like
    -- "the capture never ran".
    if attempted > 0 then
        self.settings:saveSetting("captured_pages", captured)
        self.settings:flush()
    end
    local report = ""
    if attempted > 0 then
        report = string.format("%d attempted, %d captured, %d failed", attempted, newlyCaptured, failed)
        if os.time() >= deadline then report = report .. " (time budget reached; more next sync)" end
        if firstError then report = report .. ": " .. firstError end
    end
    return events, files, report
end

-- libraryRoot is where the books are. KOReader's own home_dir when it is set, because
-- that is the directory the user actually chose; otherwise the mount point, which is
-- right on a Kobo and harmless elsewhere (a missing directory just yields nothing).
function ZmSync:libraryRoot()
    local configured = self.settings:readSetting("library_root")
    if type(configured) == "string" and configured ~= "" then return configured end
    if G_reader_settings then
        local home = G_reader_settings:readSetting("home_dir")
        if type(home) == "string" and home ~= "" then return home end
    end
    return "/mnt/onboard"
end

-- ── page capture ─────────────────────────────────────────────────────────────
--
-- A BOOKMARK BECOMES A QUOTE. A highlight carries the sentence you chose; a bookmark
-- carries only a position, which is useless to read back and impossible to revise from.
-- So the whole bookmarked page is captured here and sent as that annotation's text.
--
-- It happens on the DEVICE and at sync time, not on request from the web, for two reasons
-- that are not about convenience. An epub is reflowable, so "page 82" is a function of the
-- font settings currently in force and only the reader knows what it means. And the server
-- has never seen the book: the text has to come from the only machine that holds it.
--
-- CAPTURED ONCE, EVER. Opening a book and rendering a page is the most expensive thing
-- this plugin does, and a bookmark's page does not change. The ledger below is what stops
-- a hundred bookmarks becoming a hundred book-opens on every single sync.


-- capturedPages is the local ledger, keyed "md5:page" and holding THE PAGE TEXT ITSELF.
--
-- It held a boolean, and that was the bug that cost twelve captured pages. The sidecar
-- never gains the text — the capture lives only in what we sent — so on the next sweep a
-- ledger that says "done" meant "do not capture, and send the bookmark blank". The server
-- then had a stored page and an incoming blank for the same id, and overwrote.
--
-- Holding the text makes every sweep re-send exactly what it sent before, which is what
-- "re-send everything, ids are content-derived" promised all along. A page of text is a
-- couple of kilobytes and there are dozens, not thousands.
--
-- A page that could not be read stores `false`: still a "do not retry" marker, and
-- distinguishable from text.
function ZmSync:capturedPages()
    local c = self.settings:readSetting("captured_pages")
    if type(c) ~= "table" then c = {} end
    return c
end

-- pageText extracts one page of one book as plain text.
--
-- getPageText returns the backend's own structure — for mupdf and djvu a list of LINES,
-- each a list of word boxes — so it is flattened here rather than assumed to be a string.
--
-- Every failure path returns a REASON. It is logged rather than shown, because this runs
-- unattended during a sync, but a bookmark that never becomes a quote must leave a trail
-- saying why rather than just staying blank for ever.
function ZmSync:pageText(path, pageno)
    if type(path) ~= "string" or path == "" then
        return nil, "the book is not on this reader"
    end
    local lfs = require("libs/libkoreader-lfs")
    if not lfs.attributes(path, "mode") then
        return nil, "file missing: " .. path
    end
    local ok, DocumentRegistry = pcall(require, "document/documentregistry")
    if not ok then return nil, "no document registry" end

    local opened, doc = pcall(function() return DocumentRegistry:openDocument(path) end)
    if not opened or not doc then
        return nil, "could not open: " .. tostring(doc)
    end
    local got, res = pcall(function() return doc:getPageText(pageno) end)
    pcall(function() doc:close() end)
    if not got then return nil, "getPageText failed: " .. tostring(res) end
    if type(res) == "string" then return res, nil end
    if type(res) ~= "table" then return nil, "this format exposes no page text" end

    local lines = {}
    for _, line in ipairs(res) do
        if type(line) == "string" then
            lines[#lines + 1] = line
        elseif type(line) == "table" then
            local words = {}
            for _, wbox in ipairs(line) do
                if type(wbox) == "table" and type(wbox.word) == "string" then
                    words[#words + 1] = wbox.word
                elseif type(wbox) == "string" then
                    words[#words + 1] = wbox
                end
            end
            if #words > 0 then lines[#lines + 1] = table.concat(words, " ") end
        end
    end
    if #lines == 0 then
        return nil, "no extractable text (a scanned page, perhaps)"
    end
    return table.concat(lines, "\n"), nil
end

-- ── syncing ──────────────────────────────────────────────────────────────────

function ZmSync:onZmSyncNow()
    self:syncNow(false)
    return true
end

-- syncNow sends one batch, and recurses while the reading backfill has more.
--
-- `continuing` marks a recursive call. It exists for the annotations: they ride the FIRST
-- request of a chain and no other.
-- openBook names the book open in the reader, if any: its md5 (the id the server keys
-- annotations on) and a title to show. Nil in the file manager.
--
-- IT ALSO FLUSHES THAT BOOK'S SIDECAR. Annotations are read from the sidecar on disk, and
-- KOReader writes it on close or on its autosave timer — so a highlight made a minute ago
-- in the open book was not yet there to send, and the message would have vouched for a
-- book whose newest highlight it never saw.
function ZmSync:openBook()
    local ui = self.ui
    if not (ui and ui.document and ui.doc_settings) then return nil end
    pcall(function() ui:saveSettings() end)
    local bmd5 = ui.doc_settings:readSetting("partial_md5_checksum")
    if type(bmd5) ~= "string" or bmd5 == "" then return nil end
    local props = ui.doc_props or {}
    local stats = ui.doc_settings:readSetting("stats") or {}
    return { md5 = bmd5, title = bookTitle({ props.title, stats.title, props.display_title }, ui.document.file) }
end

-- whenText is a time the reader recognises at a glance: "today 23:12", "yesterday 23:12",
-- or a date for anything older.
local function whenText(t)
    local today = os.date("*t")
    local d = os.date("*t", t)
    if d.year == today.year and d.yday == today.yday then
        return T(_("today %1"), os.date("%H:%M", t))
    end
    local y = os.date("*t", os.time() - 86400)
    if d.year == y.year and d.yday == y.yday then
        return T(_("yesterday %1"), os.date("%H:%M", t))
    end
    return os.date("%d %b %H:%M", t)
end

local function count(n, one, many)
    return tostring(n) .. " " .. (n == 1 and one or many)
end

-- syncMessage turns one reply into what the reader sees after pressing Sync, and a
-- one-line form for the status menu.
--
-- IT ANSWERS "IS MY STUFF SAFE?", NOT "WHAT DID THIS PRESS ADD?". The device also syncs
-- by itself every time wifi connects, so a press of Sync after a night of highlighting
-- usually finds nothing new — and "0 new, 150 already had" read as if the highlights were
-- lost when they had gone up hours earlier. So the message leads with the state, vouches
-- for the open book from the SERVER'S count of it, and says when new data last arrived
-- and by which route.
function ZmSync:syncMessage(res, annotations, book, events, schema, captureReport)
    local fresh = {}
    local annNew, annUpd, readNew = res.annotations_new or 0, res.annotations_updated or 0, res.reading_new or 0
    if res.annotations_new == nil then
        -- A server that predates the split counts: fall back to its totals.
        annNew, annUpd = (res.accepted or 0), (res.updated or 0)
    end
    if annNew > 0 then fresh[#fresh + 1] = count(annNew, _("new annotation"), _("new annotations")) end
    if annUpd > 0 then fresh[#fresh + 1] = count(annUpd, _("annotation updated"), _("annotations updated")) end
    if readNew > 0 then fresh[#fresh + 1] = count(readNew, _("new page read"), _("new page reads")) end
    local rejected = res.rejected or 0

    local head
    if rejected > 0 then
        head = T(_("Not everything was saved: %1"), res.message or "")
    elseif #fresh > 0 then
        head = T(_("Synced: %1."), table.concat(fresh, ", "))
    else
        head = _("Up to date. The server already had everything.")
    end
    local lines = { head }

    -- One per id: a sidecar holding both annotation schemas yields twins, and the server
    -- collapses them, so counting raw entries would never match its count.
    local ids, books, inBook = {}, {}, 0
    for _, a in ipairs(annotations) do
        if not ids[a.id] then
            ids[a.id] = true
            books[a.bookmd5] = true
            if book and a.bookmd5 == book.md5 then inBook = inBook + 1 end
        end
    end
    local nBooks = 0
    for _ in pairs(books) do nBooks = nBooks + 1 end
    local nAnn = 0
    for _ in pairs(ids) do nAnn = nAnn + 1 end

    if book then
        local r = type(res.book) == "table" and res.book or nil
        if inBook == 0 then
            lines[#lines + 1] = T(_("%1: no annotations yet."), book.title)
        elseif r and (r.held or 0) >= inBook then
            local line = T(_("%1: all %2 on the server"), book.title,
                count(inBook, _("annotation"), _("annotations")))
            if (r.newest_received or 0) > 0 then
                line = line .. T(_(", newest received %1"), whenText(r.newest_received))
            end
            lines[#lines + 1] = line .. "."
        elseif r then
            lines[#lines + 1] = T(_("%1: only %2 of %3 annotations on the server."),
                book.title, r.held or 0, inBook)
        end
    end

    -- When new data last arrived, and how. On a press that added nothing this is the
    -- line that explains where last night's highlights went.
    local last = self.settings:readSetting("last_delivery")
    if #fresh == 0 and type(last) == "table" and last.at then
        local what = {}
        if (last.annotations or 0) > 0 then what[#what + 1] = count(last.annotations, _("annotation"), _("annotations")) end
        if (last.reading or 0) > 0 then what[#what + 1] = count(last.reading, _("page read"), _("page reads")) end
        lines[#lines + 1] = T(_("Last new data: %1, %2 (%3)."), table.concat(what, ", "),
            whenText(last.at), last.auto and _("automatic sync when wifi connected") or _("Sync button"))
    end

    if nAnn > 0 and rejected == 0 then
        lines[#lines + 1] = T(_("Library: %1 from %2, all on the server."),
            count(nAnn, _("annotation"), _("annotations")), count(nBooks, _("book"), _("books")))
    end
    if captureReport ~= "" then
        lines[#lines + 1] = T(_("Bookmarked pages: %1"), captureReport)
    end
    if #events == 0 then
        lines[#lines + 1] = T(_("Nothing sent (statistics: %1)."), schema)
    end

    local oneLine = os.date("%d %b %H:%M") .. " — " .. head
    if res.message and rejected == 0 then oneLine = oneLine .. " (" .. res.message .. ")" end
    return table.concat(lines, "\n\n"), oneLine
end

function ZmSync:syncNow(interactive, continuing, origin)
    if not self:isPaired() then
        if interactive then
            UIManager:show(InfoMessage:new{ text = _("Pair this device first.") })
        end
        return
    end
    local since = self.settings:readSetting("high_water") or 0
    local events, schema = self:collect(since)

    -- ANNOTATIONS RIDE THE FIRST REQUEST OF A CHAIN, and no other.
    --
    -- The first version attached them to the first batch that was not FULL, reasoning that
    -- a full batch means more requests are coming and forty copies of the same few hundred
    -- highlights is waste. True, and it put them on the LAST request — so they arrived only
    -- if a forty-request backfill ran to completion. It did not: the chain stopped at 2023
    -- when the reader slept, and the highlights had never been the thing that was blocked.
    --
    -- Riding the first request costs exactly the same one copy per chain and owes nothing
    -- to how the reading backfill goes. The quotes are what the reader is waiting to see;
    -- they should not be behind seven years of page rows in the queue.
    local annotations, captureReport, book = {}, "", nil
    if not continuing then
        book = self:openBook()
        local _sidecars
        annotations, _sidecars, captureReport = self:collectAnnotations()
        for _, a in ipairs(annotations) do
            events[#events + 1] = a
        end
    end

    -- SENT EVEN WHEN EMPTY. "Nothing new" and "this device cannot reach the server"
    -- look identical from the sofa, and only one of them is fine. An empty sync is
    -- how the server knows the device is alive and what schema it has.
    local ok, res = self:post("/api/ingest", {
        koreader_version = self:koreaderVersion(),
        stats_schema = schema,
        device_model = self:deviceName(),
        utc_offset = self:utcOffset(),
        capture_report = captureReport,
        current_book = book and book.md5 or nil,
        events = asArray(events),
    }, self:token())

    if not ok then
        self.settings:saveSetting("last_result", T(_("failed: %1"), res or _("no reply")))
        self.settings:flush()
        if interactive then
            UIManager:show(InfoMessage:new{ text = T(_("Sync failed: %1"), res or _("no reply")) })
        end
        return
    end

    -- THE CURSOR MOVES TO WHAT THE SERVER ACKNOWLEDGED. Never to the largest row we
    -- read: if the reply was lost the rows must be sent again, and because ids are
    -- content-derived that costs nothing but bandwidth.
    --
    -- AND IT ONLY EVER MOVES FORWARD. The server reports the maximum start_time in THIS
    -- batch, which is the only value that is a contiguous watermark for ascending paging
    -- (see ingestReply.HighWater). That makes two ordinary replies carry a lower number
    -- than we already hold — an empty heartbeat reports 0, and a retry of an older batch
    -- reports an older maximum — and taking either at face value would restart the whole
    -- history. Holding the floor HERE is what lets the server stay honest about what one
    -- batch actually proved; the alternative, a server-side maximum over everything
    -- stored, is what silently skipped ~16,000 rows of this reader's history.
    local ack = tonumber(res.high_water)
    if ack and ack > (tonumber(self.settings:readSetting("high_water")) or 0) then
        self.settings:saveSetting("high_water", ack)
    end
    -- Remembered on every sync that brought something new, automatic ones included, so a
    -- later press of Sync that finds nothing can say when and how it got there. A paging
    -- chain adds up into one delivery.
    local got = (res.annotations_new or res.accepted or 0) + (res.annotations_updated or 0) + (res.reading_new or 0)
    if got > 0 then
        local prev = continuing and self.settings:readSetting("last_delivery") or nil
        if type(prev) ~= "table" then prev = {} end
        self.settings:saveSetting("last_delivery", {
            at = os.time(), auto = origin == "automatic",
            annotations = (prev.annotations or 0) + (res.annotations_new or res.accepted or 0) + (res.annotations_updated or 0),
            reading = (prev.reading or 0) + (res.reading_new or 0),
        })
    end
    -- The message is built only for the first request of a chain; a later page of the
    -- backfill has no annotations and no open book to report on, and would overwrite the
    -- status line with a less useful one.
    if not continuing then
        local text, oneLine = self:syncMessage(res, annotations, book, events, schema, captureReport)
        self.settings:saveSetting("last_result", oneLine)
        if interactive then
            UIManager:show(InfoMessage:new{ text = text })
        end
    end
    self.settings:flush()

    -- More to send? Keep going. A first sync of a long history is many batches and
    -- should not need the reader to press the button once per five hundred pages.
    -- KEEP PAGING ON THE READING COUNT, not on #events — the annotations appended above
    -- would otherwise make a short final batch look full and recurse for ever.
    if #events - #annotations >= BATCH then
        self:syncNow(false, true, origin)
    end
end

-- ── plumbing ─────────────────────────────────────────────────────────────────

function ZmSync:deviceName()
    local Device = require("device")
    return (Device.model or "KOReader")
end

-- utcOffset is how far this device's clock is from UTC, in seconds.
--
-- It decides WHICH DAY the server files a page turn under, because the day a reader
-- remembers is the one the e-reader showed them. An unconfigured Kobo runs and displays
-- UTC and returns 0 here, which is exactly right: the server must then use UTC too, not
-- the timezone of the account, or every late-night session moves to the next morning.
--
-- os.difftime between the same instant read as local and as UTC. The !*t form is UTC; the
-- isdst flag is cleared on the UTC side so a summer offset is not counted twice.
function ZmSync:utcOffset()
    local now = os.time()
    local utc = os.date("!*t", now)
    utc.isdst = false
    local ok, offset = pcall(function() return os.difftime(now, os.time(utc)) end)
    if not ok or type(offset) ~= "number" then return 0 end
    return math.floor(offset)
end

function ZmSync:koreaderVersion()
    local ok, Version = pcall(require, "version")
    if ok and Version and Version.getCurrentRevision then
        return tostring(Version:getCurrentRevision())
    end
    return "unknown"
end

-- post sends JSON and returns ok, decoded-or-message.
function ZmSync:post(path, body, token)
    local http = require("socket.http")
    local ltn12 = require("ltn12")
    local socket = require("socket")
    local socketutil = require("socketutil")
    local JSON = require("json")

    local url = self:server() .. path
    local payload = JSON.encode(body)
    local sink = {}
    local headers = {
        ["Content-Type"] = "application/json",
        ["Content-Length"] = tostring(#payload),
    }
    if token then
        headers["Authorization"] = "Bearer " .. token
        -- The same value under a second name. KOReader's luasocket has been known to
        -- drop Authorization across a redirect, and a sync that fails only sometimes
        -- is far worse to diagnose than one that fails always.
        headers["X-KOReader-Token"] = token
    end

    -- THE SINK FORM OF http.request RETURNS 1 FIRST, NOT THE STATUS: `1, code, headers,
    -- status` on success and `nil, err` on failure. Reading it as `code, _, status` put
    -- the constant 1 in `code`, so the >= 400 check below never fired, and a dropped
    -- connection's error landed in `_` and was thrown away. The empty sink then decoded
    -- to nil WITHOUT raising, and post returned true, nil — which crashed syncNow on
    -- `res.high_water` the first time the reader's wifi went away mid-sync.
    socketutil:set_timeout(10, 60)
    local ok, r, code, _headers, status = pcall(function()
        return http.request{
            url = url, method = "POST", headers = headers,
            source = ltn12.source.string(payload),
            sink = ltn12.sink.table(sink),
        }
    end)
    socketutil:reset_timeout()

    if not ok then return false, tostring(r) end
    if r == nil then return false, tostring(code or "no reply") end
    local raw = table.concat(sink)
    local decoded_ok, decoded = pcall(JSON.decode, raw)
    -- A reply that is not a JSON object (empty body, a proxy's HTML page, `null`) is a
    -- failure, never a success the caller has to guess about.
    if not decoded_ok or type(decoded) ~= "table" then
        return false, T("HTTP %1", tostring(code or status or "?"))
    end
    if type(code) == "number" and code >= 400 then
        return false, decoded.message or T("HTTP %1", tostring(code))
    end
    return true, decoded
end

-- A NETWORK-AWARE SYNC. An e-reader is offline most of the time, and that is the
-- normal state rather than an error; the moment wifi comes up is the one moment a
-- sync is certain to be able to work.
function ZmSync:onNetworkConnected()
    if self:isPaired() then
        self:syncNow(false, false, "automatic")
    end
end

return ZmSync
