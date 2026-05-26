# demo/ccpulse.tape — reproducible README demo of the ccpulse TUI.
#
# Regenerate with:  make demo
# (builds a release binary, seeds a synthetic *-dev cache via
# scripts/seedyear, then runs this tape). Records ONLY synthetic, leak-free
# fixtures — never real ~/.claude data and no network calls. See issue #265.
#
# VHS command reference: https://github.com/charmbracelet/vhs#vhs-command-reference

Output demo/ccpulse.gif

Set Shell bash
Set Theme "rose-pine-moon"
Set FontSize 16
Set Width 1200
Set Height 800
Set Padding 20
Set Framerate 24

# Synthetic, leak-free fixtures. Paths are repo-relative (vhs runs from the
# repo root via `make demo`). `make demo` seeds demo/.cache-dev first.
Env CCPULSE_CACHE_DIR "demo/.cache-dev"
Env CCPULSE_PROJECTS_ROOT "demo/.projects"
Env CCPULSE_FAKE_QUOTA "55,42"

Type "./ccpulse"
Enter
Sleep 2.5s

# Switch to output view, zoom to 1h, switch to usage view
Type "u"
Sleep 2s

# Scroll left
Left@50ms 20
Sleep 400ms

Type "z"
Sleep 2s
Type "u"
Sleep 2s

# Scroll left
Left@50ms 20
Sleep 400ms

# Switch back to cost view
Type "u"
Sleep 2s

# Scroll right, zoom to 24h, switch to output view
Right@50ms 20
Sleep 400ms
Type "z"
Sleep 3s
Type "u"
Sleep 4s

# Quit
Type "q"
Sleep 800ms
