import re, sys
# A crude but effective guard: every `local NAME`/`local function NAME` at file scope must
# be DECLARED before any line that references it. Lua resolves a later local as a global,
# which is nil at runtime — a class of bug that compiles and syntax-checks cleanly.
src = open(sys.argv[1]).read().split('\n')
decl = {}
for i, line in enumerate(src):
    m = re.match(r'^local (?:function )?([A-Za-z_][A-Za-z0-9_]*)', line)
    if m and m.group(1) not in decl:
        decl[m.group(1)] = i
bad = []
for name, dline in decl.items():
    for i, line in enumerate(src[:dline]):
        if re.search(r'(?<![\w.:])' + re.escape(name) + r'\s*[({]', line) and not line.lstrip().startswith('--'):
            bad.append((name, i + 1, dline + 1)); break
if bad:
    for n, use, d in bad:
        print(f"USED BEFORE DECLARED: {n} used line {use}, declared line {d}")
    sys.exit(1)
print(f"scope check OK — {len(decl)} file-scope locals, none used before declaration")
