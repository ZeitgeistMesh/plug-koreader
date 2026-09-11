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

-- sidecarAnnotations flattens ONE sidecar table into a list of {kind,text,note,chapter,
-- page,datetime} regardless of which schema it uses.
local function sidecarAnnotations(t)
    local out = {}
    -- New unified schema: a plain array.
    if type(t.annotations) == "table" then
        for _, a in ipairs(t.annotations) do
            if type(a) == "table" then
                out[#out + 1] = {
                    kind = annotationKind(a.text), text = a.text or "", note = a.note or "",
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
                out[#out + 1] = {
                    kind = annotationKind(a.notes or a.text), text = a.notes or a.text or "",
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
        local ok, iter = pcall(lfs.dir, dir)
        if not ok then return end
        for name in iter do
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
    files = eachSidecar(root, function(t)
        -- NOT named md5: the module-level `md5` is the hash FUNCTION, and shadowing it
        -- here would make the id derivation below call a string.
        local bmd5 = t.partial_md5_checksum
        if type(bmd5) ~= "string" or bmd5 == "" then return end
        local props = type(t.doc_props) == "table" and t.doc_props or {}
        for _, a in ipairs(sidecarAnnotations(t)) do
            if #events >= ANNOTATIONS_CAP then return end
            local when = annotationTime(a.datetime)
            if when > 0 then
                events[#events + 1] = {
                    -- The plan's FALLBACK id, which turns out to be the only one: a
                    -- KOReader annotation carries no id of its own.
                    id = md5(bmd5 .. ":" .. a.datetime .. ":" .. tostring(a.page)),
                    kind = a.kind,
                    bookmd5 = bmd5, title = props.title or "", authors = props.authors or "",
                    page = a.page, starttime = when, duration = 0,
                    text = a.text, note = a.note, chapter = a.chapter,
                }
            end
        end
    end)
    return events, files
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

-- ── syncing ──────────────────────────────────────────────────────────────────

function ZmSync:onZmSyncNow()
    self:syncNow(false)
    return true
end

function ZmSync:syncNow(interactive)
    if not self:isPaired() then
        if interactive then
            UIManager:show(InfoMessage:new{ text = _("Pair this device first.") })
        end
        return
    end
    local since = self.settings:readSetting("high_water") or 0
    local events, schema = self:collect(since)

    -- ANNOTATIONS RIDE ALONG, and only on a batch that is not already full. A full batch
    -- means the reading backfill is still paging, and there will be dozens more requests
    -- behind this one — attaching the same few hundred highlights to every one of them
    -- would multiply the whole sweep by forty for no gain. They go with the final, short
    -- batch, and with every ordinary sync after that.
    local annotations, sidecars = {}, 0
    if #events < BATCH then
        annotations, sidecars = self:collectAnnotations()
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
    -- A SYNC THAT SENT NOTHING SAYS SO, and says what it found instead. The server can
    -- only report on what it received, so "0 accepted" reads as success; the reason the
    -- batch was empty is known HERE and nowhere else. `schema` carries it: "absent",
    -- "no page table", "<table> (query failed)", or the table name for an honest
    -- nothing-new.
    local summary = res.message or _("ok")
    if #annotations > 0 then
        summary = T(_("%1 — %2 annotations from %3 books"), summary, #annotations, sidecars)
    end
    if #events == 0 then
        summary = T(_("%1 — sent 0 events (statistics: %2)"), summary, schema)
    end
    self.settings:saveSetting("last_result", summary)
    self.settings:flush()

    if interactive then
        UIManager:show(InfoMessage:new{ text = summary })
    end
    -- More to send? Keep going. A first sync of a long history is many batches and
    -- should not need the reader to press the button once per five hundred pages.
    -- KEEP PAGING ON THE READING COUNT, not on #events — the annotations appended above
    -- would otherwise make a short final batch look full and recurse for ever.
    if #events - #annotations >= BATCH then
        self:syncNow(false)
    end
end

-- ── plumbing ─────────────────────────────────────────────────────────────────

function ZmSync:deviceName()
    local Device = require("device")
    return (Device.model or "KOReader")
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

    socketutil:set_timeout(10, 60)
    local ok, code, _, status = pcall(function()
        return http.request{
            url = url, method = "POST", headers = headers,
            source = ltn12.source.string(payload),
            sink = ltn12.sink.table(sink),
        }
    end)
    socketutil:reset_timeout()

    if not ok then return false, tostring(code) end
    local raw = table.concat(sink)
    local decoded_ok, decoded = pcall(JSON.decode, raw)
    if not decoded_ok then
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
        self:syncNow(false)
    end
end

return ZmSync
