# demo/ccpulse.tape — reproducible README demo of the ccpulse TUI.
#
# Regenerate with:  make demo
# (builds a release binary, seeds a synthetic *-dev cache via
# scripts/seedyear, then runs this tape). Records ONLY synthetic, leak-free
# fixtures — never real ~/.claude data and no network calls. See issue #265.

Output demo/ccpulse.gif

# Synthetic, leak-free fixtures. Paths are repo-relative (vhs runs from the
# repo root via `make demo`). `make demo` seeds demo/.cache-dev first.
Env CCPULSE_CACHE_DIR "demo/.cache-dev"
Env CCPULSE_PROJECTS_ROOT "demo/.projects"
Env CCPULSE_FAKE_QUOTA "55,42"

Set Shell bash
Set Theme "Solarized Dark"
Set FontSize 16
Set Width 1200
Set Height 800
Set Padding 20
Set Framerate 24

Type "./ccpulse"
Enter
Sleep 2.5s

# Cycle zoom: 15m -> 1h -> 24h -> back to 15m
Type "z"
Sleep 2s
Type "z"
Sleep 2s
Type "z"
Sleep 1.5s

# Scroll the chart left, then back right
Left
Sleep 400ms
Left
Sleep 400ms
Left
Sleep 1s
Right
Sleep 400ms
Right
Sleep 1s

# Full-help overlay, then close it
Type "?"
Sleep 2.5s
Type "?"
Sleep 800ms

# Quit
Type "q"
Sleep 800ms
